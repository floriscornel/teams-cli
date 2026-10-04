package fakegraph

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file serves the drive endpoints the CLI needs: a channel's filesFolder,
// a folder's children, a file's metadata and bytes, and the upload session the
// Phase 4 attachments use.
//
// The shapes are refs/graph/api-reference/v1.0/resources/driveitem.md, and the
// download behaviour is the documented one: GET .../content answers a 302
// redirect to a pre-authenticated URL, which needs no Authorization header
// (refs/graph/api-reference/v1.0/api/driveitem-get-content.md:24-31).

// uploadSession is one in-progress upload. Chunks are appended in order and
// every chunk must carry a Content-Range header, which is the documented
// contract for the large-file path
// (refs/graph/api-reference/v1.0/api/driveitem-createuploadsession.md).
type uploadSession struct {
	id        string
	driveID   string
	parentID  string
	itemID    string
	name      string
	received  int
	nextStart int
	done      bool
}

var uploadSessionMu sync.Mutex

// handleDefaultDrive serves GET /me/drive, the drive a chat file attachment
// goes into (refs/graph/api-reference/v1.0/api/drive-get.md:38).
func handleDefaultDrive(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	// ensureRootLocked creates the drive on demand, so a test that wants to
	// upload into the user's OneDrive needs no seed for it.
	st.ensureRootLocked(st.myDrive)
	c.json(http.StatusOK, map[string]any{
		"id":        st.myDrive,
		"driveType": "personal",
		"name":      "OneDrive",
		"webUrl":    c.s.url + "/me/drive",
	})
}

// handlePutNewDriveItemContent serves the colon-addressed simple upload,
// PUT /drives/{drive-id}/items/{parent-ref}:/{filename}:/content — or, with a
// folder in the path, .../{parent-ref}:/{folder}/{filename}:/content. It is how
// a file that does not exist yet is created, and what both the channel and the
// chat upload use
// (refs/graph/api-reference/v1.0/api/driveitem-put-content.md:48; the channel
// path from refs/teams-mcp/src/utils/file-upload.ts:125, the chat path from
// :265).
//
// The answer is 201 Created with the item, which is what the docs show for a
// new file (:95-105); replacing an existing name answers 200 with the same
// shape, because the documented default conflict behavior of a PUT is replace
// (refs/graph/api-reference/v1.0/resources/driveitem.md:147).
func handlePutNewDriveItemContent(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	parent, name, err := c.driveAddress()
	if err != nil {
		c.fail(err)
		return
	}
	driveID := c.param("drive-id")
	created := c.s.now()
	item := st.findChildByNameLocked(driveID, parent.id, name)
	status := http.StatusOK
	if item == nil {
		item = st.addDriveItemLocked(driveID, &driveItemRec{
			id:       st.newIDLocked(),
			name:     name,
			parentID: parent.id,
			created:  created,
		})
		status = http.StatusCreated
	}
	if ct := c.r.Header.Get("Content-Type"); ct != "" {
		item.contentType = ct
	}
	item.content = append([]byte(nil), c.body...)
	item.modified = created
	c.json(status, st.renderDriveItem(driveID, item, c.s.url))
}

// handleCreateUploadSessionForPath serves the colon-addressed upload session,
// POST /drives/{drive-id}/items/{parent-ref}:/{filename}:/createUploadSession,
// which is how a large file is uploaded before it exists
// (refs/graph/api-reference/v1.0/api/driveitem-createuploadsession.md:43).
func handleCreateUploadSessionForPath(c *handlerCtx) {
	var in struct {
		Item struct {
			Name             string `json:"name"`
			ConflictBehavior string `json:"@microsoft.graph.conflictBehavior"`
		} `json:"item"`
	}
	if len(c.body) > 0 && !c.decodeBody(&in) {
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	parent, name, err := c.driveAddress()
	if err != nil {
		c.fail(err)
		return
	}
	session := &uploadSession{
		id:       "upload-" + st.newIDLocked(),
		driveID:  c.param("drive-id"),
		parentID: parent.id,
		itemID:   st.newIDLocked(),
		name:     name,
	}
	st.beginUploadLocked(session)
	c.json(http.StatusOK, map[string]any{
		"@odata.type":        "#microsoft.graph.uploadSession",
		"uploadUrl":          c.s.url + "/_upload/" + session.id,
		"expirationDateTime": graphTime(c.s.now().Add(uploadSessionTTL)),
		"nextExpectedRanges": []string{"0-"},
	})
}

// handleCreateLink serves POST /drives/{drive-id}/items/{driveItem-id}/createLink
// and answers the documented sharing link
// (refs/graph/api-reference/v1.0/api/driveitem-createlink.md:37,162-165).
//
// "organization" is the scope the CLI asks for first and "users" is its
// fallback (refs/teams-mcp/src/utils/file-upload.ts:274-303); the fake serves
// both, and refuses the scopes the docs do not list.
func handleCreateLink(c *handlerCtx) {
	var in struct {
		Type  string `json:"type"`
		Scope string `json:"scope"`
	}
	if !c.decodeBody(&in) {
		return
	}
	switch in.Type {
	case "view", "edit", "embed":
	default:
		c.fail(badRequestf("'type' must be view, edit or embed."))
		return
	}
	switch in.Scope {
	case "", "anonymous", "organization", "users":
	default:
		c.fail(badRequestf("'scope' must be anonymous, organization or users."))
		return
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	if item, ok := st.driveItem(c.param("drive-id"), c.param("driveItem-id")); !ok {
		c.fail(notFoundf("The drive item %q was not found.", c.param("driveItem-id")))
		return
	} else if item.folder {
		c.fail(badRequestf("The drive item %q is a folder and cannot be shared as a file.", item.id))
		return
	}
	link := c.s.url + "/_download/" + c.param("drive-id") + "/" + c.param("driveItem-id")
	c.json(http.StatusOK, map[string]any{
		"link": map[string]any{
			"type":   in.Type,
			"scope":  firstNonEmpty(in.Scope, "organization"),
			"webUrl": link,
		},
	})
}

// driveAddress resolves the parent folder and the file name of a
// colon-addressed upload route.
//
// A path segment named {folder-name} is an intermediate folder that the fake
// creates on demand. That is a convenience, not a documented rule: Teams itself
// creates "Microsoft Teams Chat Files" in the user's drive when the first chat
// file is sent, and the CLI relies on that (refs/teams-mcp/src/utils/file-upload.ts:262).
func (c *handlerCtx) driveAddress() (*driveItemRec, string, *apiError) {
	st := c.s.st
	driveID := c.param("drive-id")
	parent, ok := st.driveItem(driveID, c.param("parent-ref"))
	if !ok {
		return nil, "", notFoundf("The drive item %q was not found.", c.param("parent-ref"))
	}
	if !parent.folder {
		return nil, "", badRequestf("The drive item %q is not a folder.", parent.id)
	}
	name := c.param("file-name")
	if name == "" {
		return nil, "", badRequestf("The upload path does not name a file.")
	}
	if folderName := c.param("folder-name"); folderName != "" {
		parent = st.childFolderLocked(driveID, parent.id, folderName, c.s.now())
	}
	return parent, name, nil
}

// handleFilesFolder serves GET /teams/{id}/channels/{id}/filesFolder. The
// channel's drive folder is derived by the seed unless it was overridden
// (internal/testing/fakegraph/model.go, Channel.DriveID/FilesFolderID).
func handleFilesFolder(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	ch, ok := st.channel(c.param("team-id"), c.param("channel-id"))
	if !ok {
		c.fail(notFoundf("The channel %q was not found.", c.param("channel-id")))
		return
	}
	item, ok := st.driveItem(ch.driveID, ch.filesFolderID)
	if !ok {
		c.fail(notFoundf("The channel %q has no filesFolder.", c.param("channel-id")))
		return
	}
	c.json(http.StatusOK, st.renderDriveItem(ch.driveID, item, c.s.url))
}

// handleGetDriveItem serves GET /drives/{drive-id}/items/{driveItem-id}.
func handleGetDriveItem(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	item, ok := st.driveItem(c.param("drive-id"), c.param("driveItem-id"))
	if !ok {
		c.fail(notFoundf("The drive item %q was not found.", c.param("driveItem-id")))
		return
	}
	c.json(http.StatusOK, st.renderDriveItem(c.param("drive-id"), item, c.s.url))
}

// handleDriveItemChildren serves GET .../items/{id}/children, which is how
// `teams channel files` lists a channel folder
// (refs/graph/api-reference/v1.0/api/driveitem-list-children.md).
func handleDriveItemChildren(c *handlerCtx) {
	p, perr := parsePage(c.query, defaultTopChildren, maxTopChildren)
	if perr != nil {
		c.fail(perr)
		return
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	driveID := c.param("drive-id")
	parent, ok := st.driveItem(driveID, c.param("driveItem-id"))
	if !ok {
		c.fail(notFoundf("The drive item %q was not found.", c.param("driveItem-id")))
		return
	}
	all := st.childrenOf(driveID, parent.id)
	items, next := window(all, p)
	out := make([]driveItemWire, 0, len(items))
	for _, item := range items {
		out = append(out, st.renderDriveItem(driveID, item, c.s.url))
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(all)))
}

// handleGetDriveItemContent serves GET .../items/{id}/content with the
// documented 302 to a pre-authenticated URL
// (refs/graph/api-reference/v1.0/api/driveitem-get-content.md:24).
func handleGetDriveItemContent(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	item, ok := st.driveItem(c.param("drive-id"), c.param("driveItem-id"))
	if !ok {
		c.fail(notFoundf("The drive item %q was not found.", c.param("driveItem-id")))
		return
	}
	if item.folder {
		c.fail(badRequestf("The drive item %q is a folder and has no content.", item.id))
		return
	}
	c.w.Header().Set("Location", c.s.downloadURL(c.param("drive-id"), item.id))
	c.w.WriteHeader(http.StatusFound)
}

// handleDownload serves the pre-authenticated URL the /content redirect points
// at. It is deliberately not in the contract route list: Graph serves that URL
// from a different host, so the CLI never builds it by hand.
func handleDownload(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	item, ok := st.driveItem(c.param("drive-id"), c.param("driveItem-id"))
	if !ok {
		c.fail(notFoundf("The drive item %q was not found.", c.param("driveItem-id")))
		return
	}
	if item.contentType != "" {
		c.w.Header().Set("Content-Type", item.contentType)
	}
	c.w.WriteHeader(http.StatusOK)
	_, _ = c.w.Write(item.content)
}

// handlePutDriveItemContent serves PUT .../items/{id}/content, the simple
// upload. The documented limit is 250 MB; the CLI keeps its own smaller
// threshold as policy (PLAN.md, "File upload").
func handlePutDriveItemContent(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	item, ok := st.driveItem(c.param("drive-id"), c.param("driveItem-id"))
	if !ok {
		c.fail(notFoundf("The drive item %q was not found.", c.param("driveItem-id")))
		return
	}
	if item.folder {
		c.fail(badRequestf("The drive item %q is a folder and cannot take content.", item.id))
		return
	}
	if ct := c.r.Header.Get("Content-Type"); ct != "" {
		item.contentType = ct
	}
	item.content = append([]byte(nil), c.body...)
	item.modified = c.s.now()
	c.json(http.StatusOK, st.renderDriveItem(c.param("drive-id"), item, c.s.url))
}

// handleCreateUploadSession serves POST .../createUploadSession. The response
// carries the upload URL and the ranges the service expects next
// (refs/graph/api-reference/v1.0/api/driveitem-createuploadsession.md).
func handleCreateUploadSession(c *handlerCtx) {
	var in struct {
		Item struct {
			Name           string `json:"name"`
			FileSystemInfo struct {
				ODataType string `json:"@odata.type,omitempty"`
			} `json:"fileSystemInfo,omitempty"`
		} `json:"item"`
	}
	if len(c.body) > 0 && !c.decodeBody(&in) {
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	parent, ok := st.driveItem(c.param("drive-id"), c.param("driveItem-id"))
	if !ok {
		c.fail(notFoundf("The drive item %q was not found.", c.param("driveItem-id")))
		return
	}
	// A folder upload target is the parent; the session creates a child item.
	if !parent.folder {
		c.fail(badRequestf("createUploadSession needs a folder as its parent."))
		return
	}
	session := &uploadSession{
		id:       "upload-" + st.newIDLocked(),
		driveID:  c.param("drive-id"),
		parentID: parent.id,
		itemID:   st.newIDLocked(),
		name:     firstNonEmpty(in.Item.Name, "upload.bin"),
	}
	st.beginUploadLocked(session)
	c.json(http.StatusOK, map[string]any{
		"@odata.type":        "#microsoft.graph.uploadSession",
		"uploadUrl":          c.s.url + "/_upload/" + session.id,
		"expirationDateTime": graphTime(c.s.now().Add(uploadSessionTTL)),
		"nextExpectedRanges": []string{"0-"},
	})
}

// handleUploadChunk serves the uploadUrl the session points at. Every chunk
// must carry Content-Range and must arrive in order, which is what the docs
// require; a gap is a 400 rather than a silently corrupt file.
func handleUploadChunk(c *handlerCtx) {
	id := c.param("upload-id")
	uploadSessionMu.Lock()
	defer uploadSessionMu.Unlock()
	session := lookupUploadSession(c.s, id)
	if session == nil {
		c.fail(notFoundf("The upload session %q was not found.", id))
		return
	}
	start, total, err := parseContentRange(c.r.Header.Get("Content-Range"))
	if err != nil {
		c.fail(badRequestf("Invalid Content-Range header %q.", c.r.Header.Get("Content-Range")))
		return
	}
	if start != session.nextStart {
		c.fail(badRequestf("The upload chunk starts at %d but the next expected range starts at %d.", start, session.nextStart))
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	item, ok := st.driveItem(session.driveID, session.itemID)
	if !ok {
		item = st.addDriveItemLocked(session.driveID, &driveItemRec{
			id:       session.itemID,
			name:     session.name,
			parentID: session.parentID,
		})
	}
	item.content = append(item.content, c.body...)
	item.modified = c.s.now()
	session.received = len(item.content)
	session.nextStart = session.received
	if total > 0 && session.received >= total {
		session.done = true
	}
	// The documented answer differs by chunk: 202 Accepted with the next
	// expected range while ranges are missing, and 200 (or 201) with the
	// finished driveItem on the last one
	// (refs/graph/api-reference/v1.0/api/driveitem-createuploadsession.md:216,250).
	if session.done {
		c.json(http.StatusOK, st.renderDriveItem(session.driveID, item, c.s.url))
		return
	}
	c.json(http.StatusAccepted, map[string]any{
		"expirationDateTime": graphTime(c.s.now().Add(uploadSessionTTL)),
		"nextExpectedRanges": []string{strconv.Itoa(session.nextStart) + "-"},
	})
}

// findChildByNameLocked returns a child of parent with that name, if it exists.
// Names are compared case-insensitively, the way OneDrive treats them.
func (s *store) findChildByNameLocked(driveID, parentID, name string) *driveItemRec {
	d := s.drives[driveID]
	if d == nil {
		return nil
	}
	for _, id := range d.order {
		item := d.items[id]
		if item.parentID == parentID && !item.folder && strings.EqualFold(item.name, name) {
			return item
		}
	}
	return nil
}

// childFolderLocked finds or creates a folder child of parent, which is how a
// colon-addressed path with a folder in it resolves (see driveAddress).
func (s *store) childFolderLocked(driveID, parentID, name string, created time.Time) *driveItemRec {
	d := s.drives[driveID]
	if d != nil {
		for _, id := range d.order {
			item := d.items[id]
			if item.parentID == parentID && item.folder && strings.EqualFold(item.name, name) {
				return item
			}
		}
	}
	return s.addDriveItemLocked(driveID, &driveItemRec{
		id:       "folder-" + s.newIDLocked(),
		name:     name,
		parentID: parentID,
		folder:   true,
		created:  created,
		modified: created,
	})
}

// downloadURL builds the pre-authenticated URL for a drive item.
func (s *Server) downloadURL(driveID, itemID string) string {
	return s.url + "/_download/" + driveID + "/" + itemID
}

// renderDriveItem converts a record to its wire shape.
func (s *store) renderDriveItem(driveID string, item *driveItemRec, baseURL string) driveItemWire {
	w := driveItemWire{
		ID:                   item.id,
		Name:                 item.name,
		Size:                 int64(len(item.content)),
		CreatedDateTime:      graphTime(item.created),
		LastModifiedDateTime: graphTime(item.modified),
		WebURL:               firstNonEmpty(item.webURL, "https://contoso.sharepoint.com/"+driveID+"/"+item.name),
		ETag:                 `"` + item.id + `"`,
		ParentReference:      &itemReference{DriveID: driveID, ID: item.parentID, Path: "/drive/root:"},
	}
	if item.folder {
		w.Folder = &folderFacet{ChildCount: s.childCount(driveID, item.id)}
	} else {
		mime := item.contentType
		if mime == "" {
			mime = "application/octet-stream"
		}
		w.File = &fileFacet{MimeType: mime}
		w.DownloadURL = ownerURL(baseURL, driveID, item.id)
	}
	return w
}

// ownerURL builds the @microsoft.graph.downloadUrl an item metadata response
// carries, which is the same URL /content redirects to.
func ownerURL(baseURL, driveID, itemID string) string {
	if baseURL == "" {
		return ""
	}
	return baseURL + "/_download/" + driveID + "/" + itemID
}

// driveItem resolves a drive item.
func (s *store) driveItem(driveID, itemID string) (*driveItemRec, bool) {
	d := s.drives[driveID]
	if d == nil {
		return nil, false
	}
	item := d.items[itemID]
	if item == nil {
		return nil, false
	}
	return item, true
}

// childrenOf returns a folder's children in seed order.
func (s *store) childrenOf(driveID, parentID string) []*driveItemRec {
	d := s.drives[driveID]
	if d == nil {
		return nil
	}
	out := make([]*driveItemRec, 0, len(d.order))
	for _, id := range d.order {
		item := d.items[id]
		if item.parentID == parentID {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}

func (s *store) childCount(driveID, parentID string) int {
	return len(s.childrenOf(driveID, parentID))
}

// addDriveItemLocked inserts an item into a drive.
func (s *store) addDriveItemLocked(driveID string, item *driveItemRec) *driveItemRec {
	s.ensureRootLocked(driveID)
	d := s.drives[driveID]
	item.order = len(d.order)
	d.items[item.id] = item
	d.order = append(d.order, item.id)
	return item
}

// beginUploadLocked registers an upload session.
func (s *store) beginUploadLocked(session *uploadSession) {
	if s.uploads == nil {
		s.uploads = map[string]*uploadSession{}
	}
	s.uploads[session.id] = session
}

func lookupUploadSession(srv *Server, id string) *uploadSession {
	srv.st.mu.RLock()
	defer srv.st.mu.RUnlock()
	return srv.st.uploads[id]
}

// parseContentRange parses "bytes 0-319487/319488".
func parseContentRange(raw string) (start, total int, err error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "bytes ")
	if raw == "" {
		return 0, 0, errNotImplemented
	}
	spec, totalPart, ok := strings.Cut(raw, "/")
	if !ok {
		return 0, 0, errNotImplemented
	}
	startPart, _, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, errNotImplemented
	}
	if start, err = strconv.Atoi(strings.TrimSpace(startPart)); err != nil {
		return 0, 0, err
	}
	if totalPart != "*" {
		if total, err = strconv.Atoi(strings.TrimSpace(totalPart)); err != nil {
			return 0, 0, err
		}
	}
	return start, total, nil
}

// uploadSessionTTL is the lifetime the fake advertises for an upload session.
const uploadSessionTTL = 4 * time.Hour
