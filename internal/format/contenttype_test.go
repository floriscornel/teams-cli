package format

import "testing"

// The fixtures are the MCP's own content-type tests
// (refs/teams-mcp/src/utils/__tests__/content-type.test.ts:1-50, whose byte
// arrays these repeat).

func TestDetectContentTypeByMagic(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, "image/png"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, "image/jpeg"},
		{"gif", []byte("GIF89a"), "image/gif"},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBPVP8 ")...), "image/webp"},
		{"bmp", []byte{'B', 'M', 0x00, 0x00}, "image/bmp"},
		{"pdf", []byte("%PDF-1.7"), "application/pdf"},
		// RIFF is also WAV and AVI: the fourcc decides.
		{"riff without the webp tag", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), DefaultContentType},
		{"too short to tell", []byte{0x89, 0x50}, DefaultContentType},
		{"unknown", []byte("hello world"), DefaultContentType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The name deliberately says nothing, so only the bytes can decide.
			if got := DetectContentType("file.bin", tc.data); got != tc.want {
				t.Errorf("DetectContentType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectContentTypeByExtension(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"notes.md", "text/markdown"},
		{"Report.DOCX", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"archive.tar.gz", "application/gzip"},
		{"noextension", DefaultContentType},
		{".hidden", DefaultContentType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectContentType(tc.name, []byte("plain text, no magic")); got != tc.want {
				t.Errorf("DetectContentType(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestDetectContentTypePrefersTheBytes(t *testing.T) {
	// A PNG that was renamed: the bytes win, because they are what a reader
	// will do with the file.
	if got := DetectContentType("notes.txt", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A}); got != "image/png" {
		t.Errorf("DetectContentType = %q, want image/png", got)
	}
	// And the reverse: a .png that is really a zip keeps the extension's type,
	// because no magic entry matches.
	if got := DetectContentType("archive.png", []byte("PK\x03\x04rest")); got != "image/png" {
		t.Errorf("DetectContentType = %q, want image/png", got)
	}
}

func TestIsImageContentType(t *testing.T) {
	images := []string{"image/png", "IMAGE/JPEG; charset=binary", "image/svg+xml", "image/webp"}
	for _, ct := range images {
		if !IsImageContentType(ct) {
			t.Errorf("IsImageContentType(%q) = false, want true", ct)
		}
	}
	notImages := []string{"", "text/plain", "application/pdf", "image/tiff"}
	for _, ct := range notImages {
		if IsImageContentType(ct) {
			t.Errorf("IsImageContentType(%q) = true, want false", ct)
		}
	}
}

func TestImageExtension(t *testing.T) {
	tests := map[string]string{
		"image/jpeg":    "jpg",
		"image/jpg":     "jpg",
		"image/png":     "png",
		"image/gif":     "gif",
		"image/webp":    "webp",
		"image/bmp":     "bmp",
		"image/svg+xml": "svg",
		"image/tiff":    "img",
	}
	for in, want := range tests {
		if got := ImageExtension(in); got != want {
			t.Errorf("ImageExtension(%q) = %q, want %q", in, got, want)
		}
	}
}
