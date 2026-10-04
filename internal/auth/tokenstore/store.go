// Package tokenstore holds the MSAL cache backends. Each store implements
// msal-ext's accessor.Accessor (Read/Write/Delete) so MSAL's own
// Replace/Export hooks drive it, plus two metadata methods the CLI needs to
// describe the store in `auth status` and `doctor`.
//
// PLAN.md's shape: the MSAL cache is envelope-encrypted on disk and only the
// 32-byte data key lives in the OS keychain, because go-keyring caps payloads
// (macOS about 3.9 KB, Windows 2.5 KB) well below the 8.2 KB cache the spike
// measured. Where no keychain is reachable — headless Linux without an unlocked
// Secret Service, containers — we fall back to a plaintext 0600 file after
// warning once.
package tokenstore

import (
	"context"
	"errors"
)

// EmptyCache is MSAL's cache contract with no entries. A store that has never
// been written returns this rather than no bytes, because msal-ext treats a
// failed Unmarshal as corruption: it retries every 10 ms until its 1 s context
// deadline and then fails the whole acquisition, which is what a fresh profile
// used to hit (refs/msal-ext/cache/cache.go:121-141).
var EmptyCache = []byte("{}")

// Store is a token-cache backend.
//
// A Keyring implementation returns ErrNotFound for "no item yet"; any other
// error means the keychain is unreachable and makes the envelope store fall back
// to its plaintext file.
type Store interface {
	// Read returns the cache bytes, or EmptyCache when nothing is stored yet.
	Read(ctx context.Context) ([]byte, error)
	// Write replaces the cache bytes.
	Write(ctx context.Context, data []byte) error
	// Delete removes the cache and any key material this store owns, so that a
	// later login starts from scratch.
	Delete(ctx context.Context) error
	// Empty reports whether anything has been stored yet. It must be cheap and
	// must not touch a keychain: commands use it to skip the token store (and
	// its keychain round trip) entirely on a profile that has never signed in.
	Empty() bool
	// Kind is the store type for display: "file", "envelope", "keyvault".
	Kind() string
	// Description is the location (path or URL) for display.
	Description() string
	// Warning is a one-time message to show the user, or "".
	Warning() string
}

// Kinds reported by Kind.
const (
	KindFile     = "file"
	KindEnvelope = "envelope"
	KindKeyVault = "keyvault"
)

// ErrNotImplemented marks a backend that is designed but arrives in a later
// phase; callers turn it into a clear usage error instead of a crash.
var ErrNotImplemented = errors.New("not implemented yet")

// ErrKeyMissing means the ciphertext exists but the encryption key is gone
// (a keychain reset, a different machine, or a deleted item). PLAN.md requires a
// clear error and a re-login, never a crash.
var ErrKeyMissing = errors.New("the encryption key for the cached tokens is missing")

// Memory is an in-process store for tests.
type Memory struct {
	Data       []byte
	Deleted    bool
	KindName   string
	Desc       string
	WriteCount int
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{KindName: KindFile, Desc: "memory"} }

// Read returns a copy of the stored bytes, or EmptyCache when nothing is stored.
func (m *Memory) Read(context.Context) ([]byte, error) {
	if m.Data == nil {
		return append([]byte(nil), EmptyCache...), nil
	}
	return append([]byte(nil), m.Data...), nil
}

// Empty implements Store.
func (m *Memory) Empty() bool { return m.Data == nil }

// Write stores a copy of data.
func (m *Memory) Write(_ context.Context, data []byte) error {
	m.Data = append([]byte(nil), data...)
	m.WriteCount++
	return nil
}

// Delete clears the store.
func (m *Memory) Delete(context.Context) error {
	m.Data = nil
	m.Deleted = true
	return nil
}

// Kind implements Store.
func (m *Memory) Kind() string { return m.KindName }

// Description implements Store.
func (m *Memory) Description() string { return m.Desc }

// Warning implements Store.
func (m *Memory) Warning() string { return "" }

var _ Store = (*Memory)(nil)
