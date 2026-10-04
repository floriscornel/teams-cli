package tokenstore

import (
	"context"

	"github.com/floriscornel/teams-cli/internal/store"
)

// File is the plaintext 0600 file store. PLAN.md keeps it as the explicit
// `token_store = "file"` choice and as the fallback wherever no keychain is
// reachable, so its tests matter more than the word "fallback" suggests.
//
// The file is written atomically, so MSAL's export can never leave a truncated
// cache behind: a half-written cache is a corrupt cache, and MSAL treats
// unparseable data as an error.
type File struct {
	path string
	// warnOptional marks the fallback case, where we tell the user once that the
	// cache is not encrypted.
	warnOptional bool
}

// NewFile returns a plaintext file store at path.
func NewFile(path string) *File { return &File{path: path} }

// NewFileFallback is NewFile with the "no keychain, tokens are unencrypted"
// warning attached (PLAN.md requires the one-time warning).
func NewFileFallback(path string) *File { return &File{path: path, warnOptional: true} }

// Read implements Store. A missing file is not an error: it yields the empty
// cache document, so MSAL sees an empty cache rather than corruption.
func (f *File) Read(context.Context) ([]byte, error) {
	data, err := store.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return append([]byte(nil), EmptyCache...), nil
	}
	return data, nil
}

// Empty implements Store.
func (f *File) Empty() bool { return !store.Scan(f.path, false).Exists }

// Write implements Store.
func (f *File) Write(_ context.Context, data []byte) error {
	return store.WriteFile(f.path, data)
}

// Delete implements Store.
func (f *File) Delete(context.Context) error { return store.RemoveIfExists(f.path) }

// Kind implements Store.
func (f *File) Kind() string { return KindFile }

// Description implements Store.
func (f *File) Description() string { return f.path }

// Warning implements Store.
func (f *File) Warning() string {
	if f.warnOptional {
		return "no OS keychain is reachable, so the token cache is stored unencrypted at " + f.path + " (0600)"
	}
	return ""
}

var _ Store = (*File)(nil)
