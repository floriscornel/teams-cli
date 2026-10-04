package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
	"github.com/floriscornel/teams-cli/internal/store"
)

// `teams file download <message>` saves what a message carries: its file
// attachments and its inline images. A channel attachment is looked up in the
// channel's SharePoint folder, which is the only way to reach a driveItem: the
// attachment itself carries a SharePoint web URL, not a drive id
// (refs/graph/api-reference/v1.0/api/channel-get-filesfolder.md,
// refs/graph/api-reference/v1.0/api/driveitem-get-content.md). A chat attachment
// either embeds its content in the message or points at a SharePoint URL that
// carries no drive id, which the command says out loud rather than guessing.

// errFileExists marks a download that would overwrite a file. It is a user error
// rather than a warning, because silently skipping or replacing files is exactly
// what a script cannot afford.
var errFileExists = errors.New("file exists")

// downloadResult is the documented --json shape of `teams file download`.
type downloadResult struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int    `json:"size"`
	ContentType string `json:"contentType,omitempty"`
	Source      string `json:"source"`
}

func (a *App) newFileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "file",
		Short: "Download files attached to a message",
	}
	cmd.AddCommand(a.newFileDownloadCmd())
	return cmd
}

func (a *App) newFileDownloadCmd() *cobra.Command {
	var (
		out       string
		nameLike  string
		noImages  bool
		overwrite bool
	)
	cmd := &cobra.Command{
		Use:   "download <message>",
		Short: "Download a message's attachments and inline images",
		Long: "Download the files a message carries into a directory (the current one by\n" +
			"default). Files are written 0600, and an existing file is never replaced\n" +
			"unless --overwrite is passed. --name filters by file name, --no-images\n" +
			"skips the inline images, -o chooses the directory.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams file download", []string{"Files.Read", "Files.Read.All"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Message(ctx, args[0], "")
			if err != nil {
				return err
			}
			dir := firstNonEmptyString(out, ".")
			if err := store.EnsureDir(dir); err != nil {
				return output.Errorf("%v", err)
			}
			results, err := a.downloadMessage(ctx, resolver, resolved, dir, nameLike, !noImages, overwrite)
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(results)
			}
			if len(results) == 0 {
				a.Printer.Statusf("nothing to download from %s", resolved.Raw)
				return nil
			}
			for _, result := range results {
				a.Printer.Successf("saved %s (%s)", result.Path, store.HumanBytes(int64(result.Size)))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", ".", "directory to write the files into")
	cmd.Flags().StringVar(&nameLike, "name", "", "only download attachments whose name contains this text")
	cmd.Flags().BoolVar(&noImages, "no-images", false, "skip the message's inline images")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace files that already exist")
	return cmd
}

// downloadMessage fetches the message and saves every attachment and inline
// image it carries. A file that already exists stops the command; a source the
// CLI cannot address (a SharePoint URL with no drive behind it) is a warning,
// because the rest of the message is still worth saving.
func (a *App) downloadMessage(ctx context.Context, resolver *ref.Resolver, resolved ref.Ref, dir, nameLike string, images, overwrite bool) ([]downloadResult, error) {
	client := resolver.Client()
	var (
		msg         graph.Message
		messagePath string
		err         error
	)
	if resolved.InChat {
		msg, err = client.GetChatMessage(ctx, resolved.ChatID, resolved.MessageID)
		messagePath = graph.ChatMessagePath(resolved.ChatID, resolved.MessageID)
	} else {
		msg, err = client.GetChannelMessage(ctx, resolved.TeamID, resolved.ChannelID, resolved.MessageID)
		messagePath = graph.ChannelMessagePath(resolved.TeamID, resolved.ChannelID, resolved.MessageID)
	}
	if err != nil {
		return nil, err
	}

	results, err := a.downloadAttachments(ctx, resolver, resolved, msg, dir, nameLike, overwrite)
	if err != nil {
		return nil, err
	}
	if images {
		imageResults, err := a.downloadInlineImages(ctx, client, messagePath, dir, overwrite)
		if err != nil {
			return nil, err
		}
		results = append(results, imageResults...)
	}
	return results, nil
}

// downloadAttachments saves the message's file attachments.
func (a *App) downloadAttachments(ctx context.Context, resolver *ref.Resolver, resolved ref.Ref, msg graph.Message, dir, nameLike string, overwrite bool) ([]downloadResult, error) {
	results := make([]downloadResult, 0, len(msg.Attachments))
	if len(msg.Attachments) == 0 {
		return results, nil
	}
	var (
		folderItems []graph.DriveItem
		driveID     string
	)
	if !resolved.InChat {
		folder, items, err := resolver.Client().ListChannelFiles(ctx, resolved.TeamID, resolved.ChannelID)
		if err != nil {
			a.Printer.Warnf("cannot list the files of %s: %v", channelLabel(resolved), err)
		} else {
			driveID, folderItems = folder.DriveID(), items
		}
	}
	for _, attachment := range msg.Attachments {
		name := firstNonEmptyString(attachment.Name, attachment.ID)
		if nameLike != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(nameLike)) {
			continue
		}
		if strings.TrimSpace(attachment.Content) != "" {
			result, err := a.inlineAttachment(dir, name, attachment, overwrite)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
			continue
		}
		if resolved.InChat {
			a.Printer.Warnf("cannot download %q: it lives in SharePoint and the attachment carries no drive id", name)
			continue
		}
		item, ok := matchDriveItem(folderItems, attachment)
		if !ok {
			a.Printer.Warnf("cannot find %q in %s", name, channelLabel(resolved))
			continue
		}
		result, err := a.saveDriveItem(ctx, resolver.Client(), driveID, item, dir, overwrite)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

// downloadInlineImages saves a message's hostedContents, the inline images Teams
// stores with the message
// (refs/graph/api-reference/v1.0/api/chatmessage-list-hostedcontents.md). The
// list endpoint is used rather than the message's own hostedContents property,
// because that property is not part of every message read.
func (a *App) downloadInlineImages(ctx context.Context, client *graph.Client, messagePath, dir string, overwrite bool) ([]downloadResult, error) {
	hosted, err := client.ListMessageHostedContents(ctx, messagePath)
	if err != nil {
		a.Printer.Debugf("inline images of %s: %v", messagePath, err)
		return nil, nil
	}
	results := make([]downloadResult, 0, len(hosted))
	for _, content := range hosted {
		value, err := client.GetHostedContentValue(ctx, messagePath, content.ID)
		if err != nil {
			a.Printer.Warnf("cannot download inline image %s: %v", content.ID, err)
			continue
		}
		contentType := firstNonEmptyString(value.ContentType, content.ContentType)
		name := "hosted-" + sanitizeFileName(content.ID) + extensionFor(contentType)
		path, err := a.writeDownload(dir, name, value.Bytes, overwrite)
		if err != nil {
			return nil, err
		}
		results = append(results, downloadResult{
			Name: name, Path: path, Size: len(value.Bytes),
			ContentType: contentType, Source: "inline image",
		})
	}
	return results, nil
}

// inlineAttachment decodes an attachment whose content is embedded in the
// message (a contentType plus a base64 content body), which is how a small file
// arrives without a drive behind it.
func (a *App) inlineAttachment(dir, name string, attachment graph.Attachment, overwrite bool) (downloadResult, error) {
	data, err := base64.StdEncoding.DecodeString(attachment.Content)
	if err != nil {
		return downloadResult{}, output.Errorf("attachment %q is not valid base64: %v", name, err)
	}
	if name == "" {
		name = "attachment" + extensionFor(attachment.ContentType)
	}
	path, err := a.writeDownload(dir, name, data, overwrite)
	if err != nil {
		return downloadResult{}, err
	}
	return downloadResult{Name: name, Path: path, Size: len(data), ContentType: attachment.ContentType, Source: "attachment"}, nil
}

// saveDriveItem downloads a driveItem and writes it to dir.
func (a *App) saveDriveItem(ctx context.Context, client *graph.Client, driveID string, item graph.DriveItem, dir string, overwrite bool) (downloadResult, error) {
	content, err := client.DownloadDriveItemContent(ctx, driveID, item.ID)
	if err != nil {
		return downloadResult{}, err
	}
	name := firstNonEmptyString(item.Name, content.Name, "download")
	path, err := a.writeDownload(dir, name, content.Bytes, overwrite)
	if err != nil {
		return downloadResult{}, err
	}
	contentType := firstNonEmptyString(content.ContentType, item.MimeType())
	return downloadResult{Name: name, Path: path, Size: len(content.Bytes), ContentType: contentType, Source: "attachment"}, nil
}

// writeDownload writes one file into dir and returns its path. An existing file
// is never replaced unless --overwrite was passed.
func (a *App) writeDownload(dir, name string, data []byte, overwrite bool) (string, error) {
	path := filepath.Join(dir, sanitizeFileName(name))
	if !overwrite {
		switch _, err := os.Stat(path); {
		case err == nil:
			return "", output.WithHint(output.Errorf("%s: %v", path, errFileExists), "pass --overwrite")
		case !errors.Is(err, fs.ErrNotExist):
			return "", output.Errorf("%v", err)
		}
	}
	// Downloaded Teams content is private by default, exactly like every other
	// file the CLI writes (PLAN.md:259).
	if err := store.WriteFile(path, data); err != nil {
		return "", output.Errorf("%v", err)
	}
	return path, nil
}

// matchDriveItem finds the drive item behind an attachment: by name first, then
// by the URL the attachment points at (webUrl or contentUrl).
func matchDriveItem(items []graph.DriveItem, attachment graph.Attachment) (graph.DriveItem, bool) {
	name := firstNonEmptyString(attachment.Name, attachment.ID)
	for _, item := range items {
		if name != "" && strings.EqualFold(item.Name, name) {
			return item, true
		}
	}
	for _, item := range items {
		if item.WebURL == "" {
			continue
		}
		for _, url := range []string{attachment.ContentURL, attachment.Content} {
			if url != "" && strings.EqualFold(strings.TrimSuffix(item.WebURL, "/"), strings.TrimSuffix(url, "/")) {
				return item, true
			}
		}
	}
	return graph.DriveItem{}, false
}

// sanitizeFileName keeps a downloaded name usable on every platform.
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return '-'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}

// extensionFor maps a content type onto a file extension, for the files whose
// name does not carry one (inline images).
func extensionFor(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "application/json":
		return ".json"
	default:
		return ".bin"
	}
}
