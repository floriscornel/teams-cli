package tokenstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/store"
)

func TestFileStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "token.json")
	f := NewFile(path)

	if !f.Empty() {
		t.Error("a store that was never written is not reported as empty")
	}
	data, err := f.Read(ctx)
	if err != nil || string(data) != string(EmptyCache) {
		t.Fatalf("Read on a missing file = (%q, %v), want the empty cache", data, err)
	}
	if err := f.Write(ctx, []byte("cache")); err != nil {
		t.Fatal(err)
	}
	data, err = f.Read(ctx)
	if err != nil || string(data) != "cache" {
		t.Fatalf("Read = (%q, %v)", data, err)
	}
	if f.Kind() != KindFile || f.Description() != path {
		t.Errorf("Kind/Description = %q/%q", f.Kind(), f.Description())
	}
	if f.Warning() != "" {
		t.Errorf("explicit file store warned: %q", f.Warning())
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != store.FileMode {
			t.Errorf("mode = %v, want %v", perm, store.FileMode)
		}
	}
	if err := f.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Delete(ctx); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if !f.Empty() {
		t.Error("the store is not empty after Delete")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still present: %v", err)
	}
}

func TestFileFallbackWarns(t *testing.T) {
	f := NewFileFallback("/tmp/token.json")
	if !strings.Contains(f.Warning(), "unencrypted") {
		t.Errorf("Warning = %q, want a plaintext warning", f.Warning())
	}
	if !strings.Contains(f.Warning(), "0600") {
		t.Errorf("Warning = %q, want the file mode", f.Warning())
	}
}
