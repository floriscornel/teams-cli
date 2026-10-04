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

// fakeKeyring is an in-memory keychain. go-keyring ships a global mock, but a
// local fake keeps tests parallel and lets us simulate an unreachable keychain.
type fakeKeyring struct {
	items        map[string]string
	getErr       error
	setErr       error
	deleteErr    error
	setCalls     int
	deletedItems []string
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{items: map[string]string{}} }

func (f *fakeKeyring) key(service, user string) string { return service + "/" + user }

func (f *fakeKeyring) Get(service, user string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.items[f.key(service, user)]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Set(service, user, secret string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.setCalls++
	f.items[f.key(service, user)] = secret
	return nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedItems = append(f.deletedItems, f.key(service, user))
	delete(f.items, f.key(service, user))
	return nil
}

func newEnvelope(t *testing.T, kr Keyring) (*Envelope, string, string) {
	t.Helper()
	// These tests drive a fake keyring, so the process-wide guard that keeps
	// tests away from the real OS keychain (see keyring_guard_test.go) must be
	// off; the store never reaches the system keyring here anyway.
	t.Setenv(EnvNoKeychain, "")
	dir := t.TempDir()
	cipherPath := filepath.Join(dir, "token.bin")
	plainPath := filepath.Join(dir, "token.json")
	e, err := NewEnvelope(cipherPath, plainPath, "me:token-key", kr)
	if err != nil {
		t.Fatal(err)
	}
	return e, cipherPath, plainPath
}

func TestEnvelopeEncryptsRoundTrip(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, _ := newEnvelope(t, kr)
	if !e.Empty() {
		t.Error("a store with no files is not reported as empty")
	}
	if err := e.Probe(); err != nil {
		t.Fatal(err)
	}
	if !e.Encrypted() || e.Kind() != KindEnvelope {
		t.Fatalf("store is not encrypting: kind=%q", e.Kind())
	}
	if e.Warning() != "" {
		t.Errorf("Warning = %q", e.Warning())
	}
	secret := []byte("an MSAL cache with a refresh token")
	if err := e.Write(ctx, secret); err != nil {
		t.Fatal(err)
	}
	// The plaintext must not appear on disk.
	raw, err := os.ReadFile(cipherPath) //nolint:gosec // the test owns this path
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "refresh token") {
		t.Fatal("ciphertext contains the plaintext")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(cipherPath)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != store.FileMode {
			t.Errorf("mode = %v, want %v", perm, store.FileMode)
		}
	}
	got, err := e.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(secret) {
		t.Errorf("Read = %q, want %q", got, secret)
	}
	if len(kr.items) != 1 {
		t.Errorf("keyring holds %d items, want 1", len(kr.items))
	}
	if e.Description() == "" || !strings.Contains(e.Description(), "keychain") {
		t.Errorf("Description = %q", e.Description())
	}
}

func TestEnvelopeProbeDoesNotCreateAKey(t *testing.T) {
	kr := newFakeKeyring()
	e, _, _ := newEnvelope(t, kr)
	if err := e.Probe(); err != nil {
		t.Fatal(err)
	}
	// The keychain answered "no such item"; that must not turn into a write,
	// because creating a keychain item pops a blocking dialog on a machine whose
	// keychain is missing (a container, a CI runner, another HOME).
	if kr.setCalls != 0 {
		t.Errorf("Probe created %d keychain items, want 0", kr.setCalls)
	}
	if !e.Encrypted() {
		t.Error("a reachable keychain with no key yet must stay in envelope mode")
	}
	if e.Warning() != "" {
		t.Errorf("Warning = %q", e.Warning())
	}
	if e.Kind() != KindEnvelope {
		t.Errorf("Kind = %q", e.Kind())
	}
	// A read of a never-written cache must not touch the keychain at all.
	got, err := e.Read(context.Background())
	if err != nil || string(got) != string(EmptyCache) {
		t.Fatalf("Read = (%q, %v)", got, err)
	}
	if kr.setCalls != 0 {
		t.Errorf("Read created %d keychain items, want 0", kr.setCalls)
	}
}

func TestEnvelopeWriteCreatesTheKeyOnce(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	e, _, _ := newEnvelope(t, kr)
	for range 3 {
		if err := e.Write(ctx, []byte("data")); err != nil {
			t.Fatal(err)
		}
	}
	if kr.setCalls != 1 {
		t.Errorf("keychain writes = %d, want exactly one key", kr.setCalls)
	}
}

func TestEnvelopeNoKeychainEnvForcesTheFallback(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, plainPath := newEnvelope(t, kr)
	// Set after construction: the store reads the variable when it probes.
	t.Setenv(EnvNoKeychain, "1")
	if err := e.Probe(); err != nil {
		t.Fatal(err)
	}
	if e.Encrypted() || e.Kind() != KindFile {
		t.Fatalf("store = %q, want the plaintext fallback", e.Kind())
	}
	if !strings.Contains(e.Warning(), EnvNoKeychain) {
		t.Errorf("Warning = %q, want it to name the variable", e.Warning())
	}
	if err := e.Write(ctx, []byte("plain")); err != nil {
		t.Fatal(err)
	}
	if kr.setCalls != 0 {
		t.Errorf("keychain was touched %d times despite %s", kr.setCalls, EnvNoKeychain)
	}
	if _, err := os.Stat(cipherPath); err == nil {
		t.Error("the fallback wrote the ciphertext path")
	}
	raw, err := os.ReadFile(plainPath)
	if err != nil || string(raw) != "plain" {
		t.Fatalf("fallback file = (%q, %v)", raw, err)
	}
}

func TestEnvelopeNoncesDifferPerWrite(t *testing.T) {
	ctx := context.Background()
	e, cipherPath, _ := newEnvelope(t, newFakeKeyring())
	if err := e.Write(ctx, []byte("same")); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Write(ctx, []byte("same")); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Error("the same plaintext produced identical ciphertext; the nonce is not random")
	}
}

func TestEnvelopeFallsBackWithoutKeychain(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	kr.getErr = errors.New("dbus: no session bus")
	kr.setErr = errors.New("dbus: no session bus")
	e, cipherPath, plainPath := newEnvelope(t, kr)
	if err := e.Probe(); err != nil {
		t.Fatal(err)
	}
	if e.Encrypted() {
		t.Fatal("store claims to encrypt without a keychain")
	}
	if e.Kind() != KindFile {
		t.Errorf("Kind = %q, want %q", e.Kind(), KindFile)
	}
	if !strings.Contains(e.Warning(), "unencrypted") {
		t.Errorf("Warning = %q, want the plaintext warning", e.Warning())
	}
	if err := e.Write(ctx, []byte("plain")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cipherPath); err == nil {
		t.Error("the fallback wrote the ciphertext path")
	}
	raw, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "plain" {
		t.Errorf("fallback stored %q", raw)
	}
	if e.Description() != plainPath {
		t.Errorf("Description = %q, want the plaintext path", e.Description())
	}
	if err := e.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}

// reopen simulates the next CLI invocation: same paths, same keychain.
func reopen(t *testing.T, cipherPath, plainPath string, kr Keyring) *Envelope {
	t.Helper()
	t.Setenv(EnvNoKeychain, "")
	e, err := NewEnvelope(cipherPath, plainPath, "me:token-key", kr)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEnvelopeMissingKeyIsAClearError(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, plainPath := newEnvelope(t, kr)
	if err := e.Write(ctx, []byte("data")); err != nil {
		t.Fatal(err)
	}
	// Simulate a keychain reset: the ciphertext stays, the key is gone.
	kr.items = map[string]string{}
	_, err := reopen(t, cipherPath, plainPath, kr).Read(ctx)
	if !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("Read = %v, want ErrKeyMissing", err)
	}
}

func TestEnvelopeTamperedCiphertextIsRejected(t *testing.T) {
	ctx := context.Background()
	e, cipherPath, _ := newEnvelope(t, newFakeKeyring())
	if err := e.Write(ctx, []byte("data")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cipherPath) //nolint:gosec // the test owns this path
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(cipherPath, raw, 0o600); err != nil { //nolint:gosec // the test owns this path
		t.Fatal(err)
	}
	if _, err := e.Read(ctx); err == nil || !strings.Contains(err.Error(), "failed authentication") {
		t.Fatalf("Read = %v, want an authentication failure", err)
	}
}

func TestEnvelopeTruncatedCiphertextIsRejected(t *testing.T) {
	ctx := context.Background()
	e, cipherPath, _ := newEnvelope(t, newFakeKeyring())
	if err := e.Write(ctx, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cipherPath, []byte{1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Read(ctx); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("Read = %v, want a truncation error", err)
	}
	if e.Empty() {
		t.Error("a truncated cache file must not count as empty")
	}
}

func TestEnvelopeMalformedKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, plainPath := newEnvelope(t, kr)
	if err := e.Write(ctx, []byte("x")); err != nil {
		t.Fatal(err)
	}
	// The keychain answered, but with something that is not a 32-byte key.
	kr.items[KeyringService+"/me:token-key"] = "not-base64!!"

	// A read must refuse: the ciphertext cannot be decrypted with a key we cannot
	// parse, and guessing would look like an empty cache.
	_, readErr := reopen(t, cipherPath, plainPath, kr).Read(ctx)
	if readErr == nil || !strings.Contains(readErr.Error(), "malformed data key") {
		t.Fatalf("Read = %v, want a malformed-key error", readErr)
	}
	if !errors.Is(readErr, ErrKeyMissing) {
		t.Errorf("Read = %v, want it to wrap ErrKeyMissing", readErr)
	}

	// A write is an explicit new session: it replaces the unusable key and says
	// so, so a user is never locked out of `auth login`.
	fresh := reopen(t, cipherPath, plainPath, kr)
	if err := fresh.Write(ctx, []byte("y")); err != nil {
		t.Fatalf("Write = %v, want the recovery path to succeed", err)
	}
	if !strings.Contains(fresh.Warning(), "new session") {
		t.Errorf("Warning = %q", fresh.Warning())
	}
	if kr.items[KeyringService+"/me:token-key"] == "not-base64!!" {
		t.Error("the malformed key was not replaced")
	}
	got, err := reopen(t, cipherPath, plainPath, kr).Read(ctx)
	if err != nil || string(got) != "y" {
		t.Fatalf("Read after recovery = (%q, %v)", got, err)
	}
}

func TestEnvelopeRefusesToFallBackOverExistingCiphertext(t *testing.T) {
	// The keychain is gone but an encrypted cache is still there: re-login is
	// the only safe answer.
	kr := newFakeKeyring()
	e, cipherPath, plainPath := newEnvelope(t, kr)
	if err := e.Write(context.Background(), []byte("data")); err != nil {
		t.Fatal(err)
	}
	kr.items = map[string]string{}
	kr.getErr = errors.New("keychain locked")
	reopened, err := NewEnvelope(cipherPath, plainPath, "me:token-key", kr)
	if err != nil {
		t.Fatal(err)
	}
	err = reopened.Probe()
	if !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("Probe = %v, want ErrKeyMissing", err)
	}
	if !strings.Contains(err.Error(), cipherPath) {
		t.Errorf("error should name the cache file: %v", err)
	}
	if _, readErr := reopened.Read(context.Background()); !errors.Is(readErr, ErrKeyMissing) {
		t.Errorf("Read = %v, want ErrKeyMissing", readErr)
	}
}

func TestEnvelopeKeyIsPerProfile(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	dir := t.TempDir()
	// A fake keyring: the process-wide guard is off for this test on purpose.
	t.Setenv(EnvNoKeychain, "")
	a, err := NewEnvelope(filepath.Join(dir, "a.bin"), filepath.Join(dir, "a.json"), "me:token-key", kr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewEnvelope(filepath.Join(dir, "b.bin"), filepath.Join(dir, "b.json"), "bot:token-key", kr)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Write(ctx, []byte("me-data")); err != nil {
		t.Fatal(err)
	}
	if !b.Empty() {
		t.Error("the bot store is not empty")
	}
	got, err := b.Read(ctx)
	if err != nil || string(got) != string(EmptyCache) {
		t.Fatalf("the bot store sees %q (%v), want the empty cache", got, err)
	}
	// Two profiles must not share a key, and reading an empty store must not
	// create one either.
	if len(kr.items) != 1 {
		t.Errorf("keyring holds %d keys, want exactly one for the profile that wrote", len(kr.items))
	}
}

func TestEnvelopeDeleteRemovesKeyAndFile(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, _ := newEnvelope(t, kr)
	if err := e.Write(ctx, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cipherPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ciphertext still present: %v", err)
	}
	if len(kr.items) != 0 {
		t.Errorf("key still in the keychain: %v", kr.items)
	}
	if err := e.Delete(ctx); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestEnvelopeReadMissingFileIsNotAnError(t *testing.T) {
	e, _, _ := newEnvelope(t, newFakeKeyring())
	got, err := e.Read(context.Background())
	if err != nil || string(got) != string(EmptyCache) {
		t.Fatalf("Read = (%q, %v), want the empty cache document", got, err)
	}
	if !e.Empty() {
		t.Error("reading an empty store created state")
	}
}

func TestEnvelopeWriteRecoversFromALostKey(t *testing.T) {
	// The user-visible recovery path: a cache whose key is gone must not lock a
	// profile out of `auth login` forever, and it must not do so silently.
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, _ := newEnvelope(t, kr)
	if err := e.Write(ctx, []byte("old session")); err != nil {
		t.Fatal(err)
	}
	oldKey := kr.items[KeyringService+"/me:token-key"]
	kr.items = map[string]string{} // a keychain reset, a restored backup, ...

	// Reading still refuses: the bytes cannot be decrypted and pretending
	// otherwise would look like an empty cache.
	if _, err := reopen(t, cipherPath, e.plainPath, kr).Read(ctx); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("Read = %v, want ErrKeyMissing", err)
	}

	// Writing starts a new session with a fresh key.
	fresh := reopen(t, cipherPath, e.plainPath, kr)
	if err := fresh.Write(ctx, []byte("new session")); err != nil {
		t.Fatalf("Write = %v, want the recovery path to succeed", err)
	}
	if fresh.Warning() == "" || !strings.Contains(fresh.Warning(), "new session") {
		t.Errorf("Warning = %q, want it to say a new session started", fresh.Warning())
	}
	if kr.items[KeyringService+"/me:token-key"] == oldKey {
		t.Error("the recovery reused the lost key")
	}
	got, err := reopen(t, cipherPath, e.plainPath, kr).Read(ctx)
	if err != nil || string(got) != "new session" {
		t.Fatalf("Read after recovery = (%q, %v)", got, err)
	}
}

func TestEnvelopeWriteFallsBackWhenNoNewKeyCanBeStored(t *testing.T) {
	// A keychain that answered once and can no longer store anything: the write
	// must still work, in the plaintext file, with the reason in Warning.
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, plainPath := newEnvelope(t, kr)
	if err := e.Write(ctx, []byte("old")); err != nil {
		t.Fatal(err)
	}
	kr.items = map[string]string{}
	kr.setErr = errors.New("keychain locked")

	fresh := reopen(t, cipherPath, plainPath, kr)
	if err := fresh.Write(ctx, []byte("new")); err != nil {
		t.Fatalf("Write = %v, want it to fall back to the plaintext file", err)
	}
	raw, err := os.ReadFile(plainPath)
	if err != nil || string(raw) != "new" {
		t.Fatalf("plaintext file = (%q, %v)", raw, err)
	}
	if !strings.Contains(fresh.Warning(), "keychain") {
		t.Errorf("Warning = %q", fresh.Warning())
	}
}

func TestEnvelopeDeleteReportsAnUnremovableKeyAsAWarning(t *testing.T) {
	// The ciphertext is the secret; a keychain item that cannot be removed must
	// not make `auth logout` fail, but it must be reported.
	ctx := context.Background()
	kr := newFakeKeyring()
	e, cipherPath, _ := newEnvelope(t, kr)
	if err := e.Write(ctx, []byte("data")); err != nil {
		t.Fatal(err)
	}
	kr.deleteErr = errors.New("keychain locked")
	if err := e.Delete(ctx); err != nil {
		t.Fatalf("Delete = %v, want success with a warning", err)
	}
	if _, err := os.Stat(cipherPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the ciphertext survived Delete: %v", err)
	}
	if !strings.Contains(e.Warning(), "keychain") {
		t.Errorf("Warning = %q, want the keychain problem", e.Warning())
	}
	if !strings.Contains(e.Warning(), "teams-cli") {
		t.Errorf("Warning = %q, want it to name the item", e.Warning())
	}
	// A second delete is a no-op and must not lose the warning.
	if err := e.Delete(ctx); err != nil {
		t.Fatalf("second Delete = %v", err)
	}
	if !strings.Contains(e.Warning(), "keychain") {
		t.Errorf("Warning after a second Delete = %q", e.Warning())
	}
}

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	if !m.Empty() {
		t.Error("a fresh memory store is not empty")
	}
	if got, err := m.Read(ctx); err != nil || string(got) != string(EmptyCache) {
		t.Fatalf("Read = (%q, %v)", got, err)
	}
	src := []byte("abc")
	if err := m.Write(ctx, src); err != nil {
		t.Fatal(err)
	}
	src[0] = 'z' // a copy must be stored
	got, err := m.Read(ctx)
	if err != nil || string(got) != "abc" {
		t.Fatalf("Read = (%q, %v)", got, err)
	}
	got[0] = 'y' // and a copy returned
	again, _ := m.Read(ctx)
	if string(again) != "abc" {
		t.Errorf("Read returned shared memory: %q", again)
	}
	if err := m.Delete(ctx); err != nil || !m.Deleted {
		t.Fatalf("Delete = %v, deleted=%v", err, m.Deleted)
	}
	if m.Kind() != KindFile || m.Description() != "memory" || m.Warning() != "" {
		t.Errorf("metadata = %q/%q/%q", m.Kind(), m.Description(), m.Warning())
	}
}
