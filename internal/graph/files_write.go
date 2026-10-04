package graph

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// This file wraps the Phase 4 file uploads, which is the port of the MCP's
// file-upload.ts (refs/teams-mcp/src/utils/file-upload.ts) with the documented
// caps kept in the comments:
//
//   - a small file goes up in one PUT, and the documented ceiling for that is
//     250 MB (refs/graph/api-reference/v1.0/api/driveitem-put-content.md:15).
//     4 MiB is our own policy, not an API limit: it bounds how much a single
//     request has to buffer and how much a failed attempt costs to repeat, and
//     it is the threshold the MCP chose (file-upload.ts:6, PLAN.md:221);
//   - a bigger one uses an upload session, whose chunks must be multiples of
//     320 KiB, must arrive in order, and must be under 60 MiB each
//     (refs/graph/api-reference/v1.0/api/driveitem-createuploadsession.md:166-173).
//     3.2 MiB (10 x 320 KiB) is what the MCP sends and sits inside the
//     documented 5-10 MiB recommendation.
//
// The 4 MB figure that shows up elsewhere in the docs is a different limit: it
// is the inline-image (hosted content) cap, and it is enforced where the
// hostedContents[] entry is built, not here (PLAN.md:221).

// SimpleUploadMaxSize is the size at or below which a file is uploaded in one
// PUT. Exactly this size is still simple, which is the MCP's comparison
// (size <= SIMPLE_UPLOAD_MAX_SIZE, refs/teams-mcp/src/utils/file-upload.ts:228).
const SimpleUploadMaxSize = 4 << 20

// UploadChunkSize is the chunk size of a large upload: ten 320 KiB blocks, the
// documented granularity of an upload session.
const UploadChunkSize = 10 * 320 * 1024

// chatFilesFolder is the OneDrive folder Teams puts chat attachments in
// (refs/teams-mcp/src/utils/file-upload.ts:243-247).
const chatFilesFolder = "Microsoft Teams Chat Files"

// Drive is the drive resource subset an upload needs
// (refs/graph/api-reference/v1.0/resources/drive.md).
type Drive struct {
	ID        string `json:"id"`
	DriveType string `json:"driveType,omitempty"`
	Name      string `json:"name,omitempty"`
	WebURL    string `json:"webUrl,omitempty"`
}

// UploadTarget says where a file goes.
//
// A channel attachment is stored in the team's SharePoint drive, in the
// channel's files folder; a chat attachment goes into the signed-in user's own
// OneDrive, which is what makes Files.ReadWrite enough for one and
// Files.ReadWrite.All necessary for the other (PLAN.md:57).
type UploadTarget struct {
	// TeamID and ChannelID identify a channel's files folder.
	TeamID    string
	ChannelID string
	// Chat marks a chat attachment: the file goes into the caller's OneDrive,
	// under the Teams chat folder.
	Chat bool
}

// IsChannel reports whether the target is a channel's drive.
func (t UploadTarget) IsChannel() bool { return !t.Chat && t.TeamID != "" && t.ChannelID != "" }

// UploadResult is an uploaded file, in the shape the attachment needs.
type UploadResult struct {
	// WebURL is the URL the attachment's contentUrl carries. For a channel that
	// is the SharePoint URL of the item; for a chat it is a sharing link, which
	// is what recipient access needs (refs/teams-mcp/src/utils/file-upload.ts:270-272).
	WebURL string
	// AttachmentID is the GUID inside the item's eTag, which is the id the body's
	// <attachment id="..."> reference and the attachments[] entry share
	// (refs/INDEX.md:88).
	AttachmentID string
	// Name, Size and ContentType describe the uploaded file.
	Name        string
	Size        int64
	ContentType string
	// ItemID is the drive item's id, needed for the sharing link of a chat
	// upload and reported with --json.
	ItemID string
	// DriveID is the drive the file landed in.
	DriveID string
}

// GetDefaultDrive returns the signed-in user's drive (GET /me/drive,
// refs/graph/api-reference/v1.0/api/drive-get.md:38), which is where a chat
// attachment goes.
func (c *Client) GetDefaultDrive(ctx context.Context) (Drive, error) {
	var drive Drive
	err := c.Get(ctx, "/me/drive", &drive, WithQuery(url.Values{"$select": {"id,driveType,name,webUrl"}}))
	return drive, err
}

// UploadFile uploads a file to a channel's files folder or to the caller's
// OneDrive (see UploadTarget), and returns what the attachment needs.
//
// The contentType is the caller's decision (internal/format detects it from the
// bytes and the name) so that a --dry-run can report the same value without a
// network call.
func (c *Client) UploadFile(ctx context.Context, target UploadTarget, name, contentType string, data []byte) (UploadResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return UploadResult{}, usageError("graph: an upload needs a file name")
	}
	driveID, parentRef, remotePath, err := c.uploadLocation(ctx, target, name)
	if err != nil {
		return UploadResult{}, err
	}

	item, err := c.uploadBytes(ctx, driveID, parentRef, remotePath, contentType, data)
	if err != nil {
		return UploadResult{}, err
	}
	result := UploadResult{
		WebURL:       item.WebURL,
		AttachmentID: attachmentIDFromETag(item.ETag),
		Name:         firstNonEmptyTrimmed(item.Name, name),
		Size:         int64(len(data)),
		ContentType:  firstNonEmptyTrimmed(item.MimeType(), contentType),
		ItemID:       item.ID,
		DriveID:      driveID,
	}
	// A chat attachment needs a sharing link, not the raw file URL: the docs
	// note is the MCP's, and it says a direct webUrl gives recipients
	// "permission denied" (refs/teams-mcp/src/utils/file-upload.ts:270-303).
	if target.Chat {
		result.WebURL = c.shareLink(ctx, driveID, item, result.WebURL)
	}
	if result.AttachmentID == "" {
		return UploadResult{}, fmt.Errorf("graph: the upload of %q returned no eTag, so the attachment has no id", name)
	}
	if result.WebURL == "" {
		return UploadResult{}, fmt.Errorf("graph: the upload of %q returned no URL for the attachment", name)
	}
	return result, nil
}

// uploadLocation resolves the drive, the folder reference and the in-drive path
// a file goes to.
func (c *Client) uploadLocation(ctx context.Context, target UploadTarget, name string) (driveID, parentRef, remotePath string, err error) {
	if target.IsChannel() {
		folder, ferr := c.GetFilesFolder(ctx, target.TeamID, target.ChannelID)
		if ferr != nil {
			return "", "", "", ferr
		}
		driveID = folder.DriveID()
		if driveID == "" || folder.ID == "" {
			return "", "", "", usageError("graph: the channel filesFolder did not report a drive and folder id")
		}
		return driveID, folder.ID, url.PathEscape(name), nil
	}
	if !target.Chat {
		return "", "", "", usageError("graph: an upload needs a channel (team and channel) or a chat target")
	}
	drive, derr := c.GetDefaultDrive(ctx)
	if derr != nil {
		return "", "", "", derr
	}
	if drive.ID == "" {
		return "", "", "", usageError("graph: GET /me/drive did not report a drive id")
	}
	// "root" is the drive root reference the colon-addressed route takes; the
	// folder and the name are separate segments so the path stays readable
	// (refs/graph/api-reference/v1.0/api/driveitem-put-content.md:81).
	return drive.ID, "root", url.PathEscape(chatFilesFolder) + "/" + url.PathEscape(name), nil
}

// uploadBytes performs the simple PUT or the upload session, and returns the
// resulting drive item.
func (c *Client) uploadBytes(ctx context.Context, driveID, parentRef, remotePath, contentType string, data []byte) (DriveItem, error) {
	if len(data) <= SimpleUploadMaxSize {
		// PUT /drives/{drive-id}/items/{parent-id}:/{filename}:/content — the
		// documented way to create a file (driveitem-put-content.md:48). It is
		// also a replace when the name exists, which is what conflictBehavior
		// "replace" (the documented PUT default) means.
		path := driveItemAddress(driveID, parentRef, remotePath) + "/content"
		var item DriveItem
		resp, err := c.Do(ctx, Request{
			Method:      http.MethodPut,
			Path:        path,
			Raw:         data,
			ContentType: contentType,
		})
		if err != nil {
			return DriveItem{}, err
		}
		if err := resp.Decode(&item); err != nil {
			return DriveItem{}, err
		}
		return item, nil
	}
	return c.uploadSession(ctx, driveID, parentRef, remotePath, data)
}

// uploadSession uploads a large file through an upload session: create it, then
// PUT each chunk to the pre-authorized URL it returns.
func (c *Client) uploadSession(ctx context.Context, driveID, parentRef, remotePath string, data []byte) (DriveItem, error) {
	// The session request carries the documented conflict behavior, which is
	// what re-uploading a name does (refs/teams-mcp/src/utils/file-upload.ts:147-153;
	// refs/graph/api-reference/v1.0/resources/driveitem.md:147).
	sessionBody := struct {
		Item struct {
			ConflictBehavior string `json:"@microsoft.graph.conflictBehavior"`
		} `json:"item"`
	}{}
	sessionBody.Item.ConflictBehavior = "rename"

	path := driveItemAddress(driveID, parentRef, remotePath) + "/createUploadSession"
	var session uploadSessionResponse
	if err := c.Post(ctx, path, sessionBody, &session); err != nil {
		return DriveItem{}, err
	}
	if session.UploadURL == "" {
		return DriveItem{}, fmt.Errorf("graph: the upload session returned no uploadUrl")
	}

	var (
		last     DriveItem
		gotFinal bool
	)
	for offset := 0; offset < len(data); {
		end := min(offset+UploadChunkSize, len(data))
		chunk := data[offset:end]
		resp, err := c.Do(ctx, Request{
			Method:        http.MethodPut,
			Path:          session.UploadURL,
			Raw:           chunk,
			PreAuthorized: true,
			Header: http.Header{
				// The documented chunk contract: a byte range whose end is
				// inclusive, against the file's total size
				// (refs/graph/api-reference/v1.0/api/driveitem-createuploadsession.md:163-173).
				"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", offset, end-1, len(data))},
			},
		})
		if err != nil {
			return DriveItem{}, err
		}
		// An intermediate answer is 202 with the next expected range; the last
		// chunk answers with the item's metadata (the same response the simple
		// PUT returns).
		if resp.StatusCode == http.StatusAccepted {
			last, gotFinal = DriveItem{}, false
		} else {
			if err := resp.Decode(&last); err != nil {
				return DriveItem{}, err
			}
			gotFinal = true
		}
		offset = end
	}
	if !gotFinal {
		return DriveItem{}, fmt.Errorf("graph: the upload session finished without returning the file's metadata")
	}
	return last, nil
}

// uploadSessionResponse is the documented createUploadSession response
// (refs/graph/api-reference/v1.0/api/driveitem-createuploadsession.md:150-160).
type uploadSessionResponse struct {
	UploadURL          string   `json:"uploadUrl"`
	ExpirationDateTime string   `json:"expirationDateTime,omitempty"`
	NextExpectedRanges []string `json:"nextExpectedRanges,omitempty"`
}

// shareLink returns a sharing link for an uploaded item, or fallback when the
// tenant does not allow one.
//
// The chain is the MCP's: "organization" first, then "users" for tenants that
// block org-wide links, and the upload's own URL as the last resort
// (refs/teams-mcp/src/utils/file-upload.ts:274-303). A failure here does not
// fail the upload: the file exists, and a link that only the owner can open is
// still better than losing it.
func (c *Client) shareLink(ctx context.Context, driveID string, item DriveItem, fallback string) string {
	if item.ID == "" {
		return fallback
	}
	for _, scope := range []string{"organization", "users"} {
		link, err := c.CreateShareLink(ctx, driveID, item.ID, scope)
		if err != nil {
			c.logf("createLink (%s) failed for %s: %v", scope, item.ID, err)
			continue
		}
		if link != "" {
			return link
		}
	}
	return fallback
}

// CreateShareLink asks for a view link to a drive item and returns its URL.
// The request is the documented one
// (refs/graph/api-reference/v1.0/api/driveitem-createlink.md:37).
func (c *Client) CreateShareLink(ctx context.Context, driveID, itemID, scope string) (string, error) {
	body := struct {
		Type  string `json:"type"`
		Scope string `json:"scope,omitempty"`
	}{Type: "view", Scope: scope}
	var out struct {
		Link struct {
			WebURL string `json:"webUrl"`
		} `json:"link"`
	}
	path := "/drives/" + segment(driveID) + "/items/" + segment(itemID) + "/createLink"
	if err := c.Post(ctx, path, body, &out); err != nil {
		return "", err
	}
	return out.Link.WebURL, nil
}

// driveItemAddress renders the colon-addressed path of a drive item:
// /drives/{drive-id}/items/{parent-id}:/{path}:/content is the documented way to
// create a file that does not exist yet
// (refs/graph/api-reference/v1.0/api/driveitem-put-content.md:48,81).
//
// remotePath is already escaped, segment by segment: a slash in it separates
// folders, and a space or a non-ASCII character in a name must be escaped.
func driveItemAddress(driveID, parentRef, remotePath string) string {
	return "/drives/" + segment(driveID) + "/items/" + segment(parentRef) + ":/" + remotePath + ":"
}

// attachmentIDFromETag extracts the GUID a drive item's eTag carries, which is
// the id the message attachment uses (refs/teams-mcp/src/utils/file-upload.ts:94-101).
//
// The documented form is `"{GUID},version"`; the fallback takes whatever comes
// before the comma, because some drives version differently, and the raw eTag
// is used when nothing else is left.
func attachmentIDFromETag(etag string) string {
	etag = strings.TrimSpace(etag)
	if etag == "" {
		return ""
	}
	if start := strings.IndexByte(etag, '{'); start >= 0 {
		if end := strings.IndexByte(etag[start:], '}'); end > 1 {
			return etag[start+1 : start+end]
		}
	}
	raw, _, _ := strings.Cut(etag, ",")
	trimmed := strings.Trim(raw, `"{}`)
	if trimmed != "" {
		return trimmed
	}
	return etag
}

// base64Bytes encodes a file's bytes for a hostedContents[] entry.
func base64Bytes(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// HostedContentUploadFor builds the `hostedContents[]` entry for an inline image.
//
// The temporaryId is what the body must reference as
// ../hostedContents/{temporaryId}/$value, so the two are built together by the
// caller, and Graph replaces the temporary id with the real one
// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:731).
func HostedContentUploadFor(temporaryID, contentType string, data []byte) HostedContentUpload {
	return HostedContentUpload{
		TemporaryID:  temporaryID,
		ContentBytes: base64Bytes(data),
		ContentType:  contentType,
	}
}

// firstNonEmptyTrimmed returns the first non-blank value, trimmed.
func firstNonEmptyTrimmed(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
