package format

import (
	"strings"
)

// This file detects a file's content type for the upload path, as a port of the
// MCP's content-type.ts and its extension map
// (refs/teams-mcp/src/utils/content-type.ts:5-51,
// refs/teams-mcp/src/utils/file-upload.ts:12-47).
//
// The MCP only uses the extension map on the write path and leaves the
// magic-byte sniffer unreachable ("detectContentType is not referenced by any
// tool"); PLAN.md:223 asks for the sniffing, so both are used here: the bytes
// win when they are recognisable, and the extension decides otherwise. That
// order matters for the file that arrives with the wrong name - a PNG called
// notes.txt is still an image, and a .png that is really a zip must not be sent
// as one.

// magicPrefix is one entry of the sniffing table: the bytes a file starts with,
// and the type they identify. WebP is the exception that needs a second check
// (see DetectContentType).
type magicPrefix struct {
	prefix []byte
	kind   string
}

// magicPrefixes is the MCP's table, in its order. The order is not important
// for these signatures (no prefix is a prefix of another), and it is kept so a
// reader can diff the two.
var magicPrefixes = []magicPrefix{
	{[]byte{0x89, 'P', 'N', 'G'}, "image/png"},
	{[]byte{0xFF, 0xD8, 0xFF}, "image/jpeg"},
	{[]byte{'G', 'I', 'F', '8'}, "image/gif"},
	{[]byte{'R', 'I', 'F', 'F'}, "image/webp"}, // confirmed by the WEBP tag at offset 8
	{[]byte{'B', 'M'}, "image/bmp"},
	{[]byte{'%', 'P', 'D', 'F'}, "application/pdf"},
}

// webpTag is the "WEBP" fourcc a RIFF container must carry at offset 8 to be a
// WebP image (refs/teams-mcp/src/utils/content-type.ts:26-36).
const webpTag = "WEBP"

// extensionTypes is the MCP's extension-to-type map
// (refs/teams-mcp/src/utils/file-upload.ts:12-47), keyed by lower-case
// extension including the dot.
var extensionTypes = map[string]string{
	".pdf":  "application/pdf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".zip":  "application/zip",
	".7z":   "application/x-7z-compressed",
	".rar":  "application/vnd.rar",
	".tar":  "application/x-tar",
	".gz":   "application/gzip",
	".txt":  "text/plain",
	".csv":  "text/csv",
	".json": "application/json",
	".xml":  "application/xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".mp4":  "video/mp4",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".html": "text/html",
	".htm":  "text/html",
	".css":  "text/css",
	".js":   "application/javascript",
	".ts":   "application/typescript",
	".py":   "text/x-python",
	".md":   "text/markdown",
	".log":  "text/plain",
}

// DefaultContentType is what an unrecognised file is sent as, which is also what
// the MCP falls back to (refs/teams-mcp/src/utils/content-type.ts:50).
const DefaultContentType = "application/octet-stream"

// DetectContentType names the type of a file, from its bytes and then its name.
//
// The magic bytes are checked first, on the first 12 bytes at most; an
// unrecognised file falls back to its extension and then to
// DefaultContentType. Nothing here is a security decision: the type only says
// what a reader should do with the file, and Graph stores whatever it is told.
func DetectContentType(name string, data []byte) string {
	if kind := detectByMagic(data); kind != "" {
		return kind
	}
	if kind := detectByExtension(name); kind != "" {
		return kind
	}
	return DefaultContentType
}

// detectByMagic matches the magic-byte table.
func detectByMagic(data []byte) string {
	for _, entry := range magicPrefixes {
		if !hasPrefix(data, entry.prefix) {
			continue
		}
		if entry.kind == "image/webp" {
			// A RIFF file is only a WebP when the fourcc at offset 8 says so:
			// RIFF is also WAV and AVI.
			if len(data) < 12 || string(data[8:12]) != webpTag {
				continue
			}
		}
		return entry.kind
	}
	return ""
}

// hasPrefix reports whether data starts with prefix.
func hasPrefix(data, prefix []byte) bool {
	if len(data) < len(prefix) {
		return false
	}
	for i := range prefix {
		if data[i] != prefix[i] {
			return false
		}
	}
	return true
}

// detectByExtension looks the file's extension up in the extensionTypes map.
func detectByExtension(name string) string {
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return ""
	}
	return extensionTypes[strings.ToLower(name[dot:])]
}

// imageTypes are the image types a Teams message can carry inline as hosted
// content. The list is the MCP's isValidImageType
// (refs/teams-mcp/src/utils/attachments.ts:69-81).
var imageTypes = map[string]bool{
	"image/jpeg":    true,
	"image/jpg":     true,
	"image/png":     true,
	"image/gif":     true,
	"image/webp":    true,
	"image/bmp":     true,
	"image/svg+xml": true,
}

// IsImageContentType reports whether a type can be sent as an inline image
// (hosted content) rather than as a file attachment.
func IsImageContentType(contentType string) bool {
	kind, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(contentType)), ";")
	return imageTypes[strings.TrimSpace(kind)]
}

// ImageExtension returns the file extension for an image type, used to name an
// inline image that arrived without a usable name
// (refs/teams-mcp/src/utils/attachments.ts:86-98).
func ImageExtension(contentType string) string {
	kind, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(contentType)), ";")
	switch strings.TrimSpace(kind) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/bmp":
		return "bmp"
	case "image/svg+xml":
		return "svg"
	default:
		return "img"
	}
}
