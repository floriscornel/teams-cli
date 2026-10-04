package graph

import (
	"context"
	"net/url"
	"strings"
)

// This file wraps the file reads of PLAN.md Phase 3 ("channel files",
// "file download"). Channel files live in the team's SharePoint drive, which
// the channel's filesFolder points at; the documented least-privileged
// permission for that lookup is Files.Read.All and Files.Read only reaches "the
// signed-in user's files" (refs/INDEX.md, "Scope sets vs. the MCP's").

// Download is a fetched file's bytes and content type.
type Download struct {
	Name        string
	ContentType string
	Bytes       []byte
}

// ListChannelFiles returns the children of a channel's files folder, which is
// the "channel files" listing PLAN.md specifies. It resolves the filesFolder
// (GET /teams/{team-id}/channels/{channel-id}/filesFolder,
// refs/graph/api-reference/v1.0/api/channel-get-filesfolder.md) and then lists
// its children (refs/graph/api-reference/v1.0/api/driveitem-list-children.md).
func (c *Client) ListChannelFiles(ctx context.Context, teamID, channelID string) (folder DriveItem, items []DriveItem, err error) {
	folder, err = c.GetFilesFolder(ctx, teamID, channelID)
	if err != nil {
		return DriveItem{}, nil, err
	}
	driveID := folder.DriveID()
	if driveID == "" {
		return DriveItem{}, nil, usageError("graph: the channel filesFolder did not report a drive id")
	}
	items, err = c.ListDriveItemChildren(ctx, driveID, folder.ID, 0)
	if err != nil {
		return DriveItem{}, nil, err
	}
	return folder, items, nil
}

// GetFilesFolder returns a channel's files folder
// (refs/graph/api-reference/v1.0/api/channel-get-filesfolder.md).
func (c *Client) GetFilesFolder(ctx context.Context, teamID, channelID string) (DriveItem, error) {
	var folder DriveItem
	err := c.Get(ctx, "/teams/"+segment(teamID)+"/channels/"+segment(channelID)+"/filesFolder", &folder)
	return folder, err
}

// DriveID is the drive a driveItem lives in, taken from its parentReference.
// The filesFolder response documents parentReference.driveId as the way to
// address the item afterwards (refs/graph/api-reference/v1.0/resources/driveitem.md).
func (d DriveItem) DriveID() string {
	if d.ParentReference == nil {
		return ""
	}
	return d.ParentReference.DriveID
}

// GetDriveItem returns one item's metadata
// (refs/graph/api-reference/v1.0/api/driveitem-get.md). withWebDavURL asks for
// $select=webDavUrl, which PLAN.md:246 prefers for channel file attachments.
func (c *Client) GetDriveItem(ctx context.Context, driveID, itemID string, withWebDavURL bool) (DriveItem, error) {
	var item DriveItem
	opts := []RequestOption{}
	if withWebDavURL {
		opts = append(opts, WithQuery(url.Values{
			"$select": {"id,name,size,webUrl,webDavUrl,file,folder,parentReference,createdDateTime,lastModifiedDateTime"},
		}))
	}
	err := c.Get(ctx, "/drives/"+segment(driveID)+"/items/"+segment(itemID), &item, opts...)
	return item, err
}

// ListDriveItemChildren returns a folder's children
// (refs/graph/api-reference/v1.0/api/driveitem-list-children.md).
func (c *Client) ListDriveItemChildren(ctx context.Context, driveID, itemID string, limit int) ([]DriveItem, error) {
	return ListAll[DriveItem](ctx, c, "/drives/"+segment(driveID)+"/items/"+segment(itemID)+"/children", nil, limit)
}

// filenameFromDisposition extracts filename or filename* from a
// Content-Disposition header. Graph's pre-authenticated download serves one,
// and it is the only place the original name appears when the caller did not ask
// for the item's metadata (refs/graph/api-reference/v1.0/api/driveitem-get-content.md).
func filenameFromDisposition(value string) string {
	if value == "" {
		return ""
	}
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		lower := strings.ToLower(part)
		switch {
		case strings.HasPrefix(lower, "filename*="):
			raw := part[len("filename*="):]
			if i := strings.LastIndex(raw, "''"); i >= 0 {
				raw = raw[i+2:]
			}
			if decoded, err := url.PathUnescape(strings.Trim(raw, "\"'")); err == nil {
				return decoded
			}
		case strings.HasPrefix(lower, "filename="):
			return strings.Trim(part[len("filename="):], "\"'")
		}
	}
	return ""
}

// DownloadDriveItemContent fetches a file's bytes
// (refs/graph/api-reference/v1.0/api/driveitem-get-content.md). Graph answers
// with a 302 to a pre-authenticated URL; Go's HTTP client follows it, and it
// drops the Authorization header when the redirect leaves the Graph host, which
// is exactly the documented behaviour of that URL.
func (c *Client) DownloadDriveItemContent(ctx context.Context, driveID, itemID string) (Download, error) {
	path := "/drives/" + segment(driveID) + "/items/" + segment(itemID) + "/content"
	resp, err := c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return Download{}, err
	}
	return Download{
		ContentType: resp.Header.Get("Content-Type"),
		Name:        filenameFromDisposition(resp.Header.Get("Content-Disposition")),
		Bytes:       resp.Body,
	}, nil
}
