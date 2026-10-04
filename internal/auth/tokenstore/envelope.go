package tokenstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/floriscornel/teams-cli/internal/store"
)

// Keyring is the slice of github.com/zalando/go-keyring we use. It is an
// interface so tests can use an in-memory fake (and so a future platform
// backend is a build-tag swap, not a rewrite).
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

// KeyringService is the keychain service name every teams CLI item lives under.
const KeyringService = "teams-cli"

// EnvNoKeychain forces the plaintext 0600 file store. It exists for containers
// and CI runners whose environment has no keychain at all, and for tests: asking
// the OS keychain to *create* an item where no keychain exists makes macOS show
// a blocking "Keychain Not Found" dialog.
const EnvNoKeychain = "TEAMS_NO_KEYCHAIN"

// Envelope is the default store: AES-256-GCM ciphertext in a 0600 file, with a
// random 32-byte data key in the OS keychain. go-keyring is cgo-free on all
// three platforms (macOS shells out to /usr/bin/security and writes the secret
// on stdin, Linux talks D-Bus to the Secret Service, Windows uses wincred), so
// the release build stays CGO_ENABLED=0.
type Envelope struct {
	path      string
	plainPath string
	user      string
	keyring   Keyring
	// fallback is set when the keychain turned out to be unavailable; the store
	// then behaves like a File store.
	fallback *File
	warning  string
	// probed records that the keychain has been consulted at least once, and
	// keyErr keeps the first failure so repeated calls agree.
	probed bool
	keyErr error
	// key is the resolved data key; it is never created except by a write.
	key []byte
}

// NewEnvelope prepares the envelope store. The keychain is not consulted here:
// talking to the OS keychain costs about a second on macOS, and most invocations
// (`teams version`, `teams cache info`, a first `teams auth status`) never touch
// token material at all. Probe (or the first Read/Write/Delete) does that work.
func NewEnvelope(ciphertextPath, plaintextPath, keyUser string, kr Keyring) (*Envelope, error) {
	if kr == nil {
		kr = SystemKeyring{}
	}
	return &Envelope{path: ciphertextPath, plainPath: plaintextPath, user: keyUser, keyring: kr}, nil
}

// Probe checks whether the keychain is usable, without creating anything: a
// read-only lookup is what tells us "reachable, no key yet" apart from
// "unreachable". Creating a key here would make an untouched profile ask macOS
// to create a keychain item, which pops a blocking dialog on a machine whose
// keychain is missing (a container, a CI runner, a different HOME).
//
// A cache whose key is gone while its ciphertext remains is reported as
// ErrKeyMissing: silently switching to plaintext would look like a sign-out
// while the refresh token sits on disk. `teams doctor` calls Probe to report
// keychain reachability.
func (e *Envelope) Probe() error {
	if e.probed {
		return e.keyErr
	}
	e.probed = true
	e.warning = ""
	if truthyEnv(os.Getenv(EnvNoKeychain)) {
		e.degrade(errors.New(EnvNoKeychain + " is set"))
		return nil
	}
	key, err := e.existingKey()
	switch {
	case err == nil && key != nil:
		e.key = key
	case errors.Is(err, errMalformedKey):
		e.keyErr = fmt.Errorf("%w (at %s): %w", ErrKeyMissing, e.path, err)
	case e.CiphertextExists():
		// The ciphertext is there but its key is not (a keychain reset, another
		// machine, a deleted item): that is a re-login, never a plaintext
		// downgrade. A read cannot do anything with these bytes.
		cause := err
		if cause == nil {
			cause = errors.New("the keychain has no item for this profile")
		}
		e.keyErr = fmt.Errorf("%w (at %s): %w", ErrKeyMissing, e.path, cause)
	case err != nil:
		e.degrade(err)
	default:
		// Reachable keychain, no key yet, nothing stored: stay in envelope mode.
	}
	return e.keyErr
}

// degrade switches this store to the plaintext file fallback, keeping the reason
// for the one-time warning.
func (e *Envelope) degrade(cause error) {
	if e.fallback != nil {
		return
	}
	e.fallback = NewFileFallback(e.plainPath)
	e.warning = fmt.Sprintf("OS keychain unavailable (%v); storing the token cache unencrypted at %s", cause, e.plainPath)
}

func truthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// Empty implements Store: no ciphertext and no fallback file means nothing has
// ever been stored for this profile.
func (e *Envelope) Empty() bool {
	return !e.CiphertextExists() && !exists(e.plainPath)
}

// Empty implements Store for the plaintext fallback path.
func exists(path string) bool { return store.Scan(path, false).Exists }

// Kind implements Store. Before Probe it reports the intended kind, because
// asking the keychain how it feels is exactly the cost we are avoiding.
func (e *Envelope) Kind() string {
	if e.fallback != nil {
		return KindFile
	}
	return KindEnvelope
}

// Description implements Store.
func (e *Envelope) Description() string {
	if e.fallback != nil {
		return e.fallback.Description()
	}
	return e.path + " (key in the OS keychain)"
}

// Warning implements Store. It only reports something once the keychain has been
// probed, so an untouched profile produces no scary message.
func (e *Envelope) Warning() string { return e.warning }

// Encrypted reports whether the store is actually encrypting.
func (e *Envelope) Encrypted() bool { return e.fallback == nil }

// Read implements Store. Reading a profile that has never stored anything does
// not consult the keychain at all: there is nothing to decrypt, and this is the
// first thing every command does.
//
// When only a plaintext file exists (a previous run had no keychain, or the user
// chose token_store = "file"), that file is the cache: the store must read it
// even if a keychain has since become available, or the account would look
// signed out while its token sits on disk.
func (e *Envelope) Read(ctx context.Context) ([]byte, error) {
	if !e.CiphertextExists() {
		if exists(e.plainPath) {
			return NewFile(e.plainPath).Read(ctx)
		}
		if e.fallback != nil {
			return e.fallback.Read(ctx)
		}
		return append([]byte(nil), EmptyCache...), nil
	}
	if err := e.Probe(); err != nil {
		return nil, err
	}
	if e.fallback != nil {
		return e.fallback.Read(ctx)
	}
	data, err := store.ReadFile(e.path)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return append([]byte(nil), EmptyCache...), nil
	}
	if e.key == nil {
		return nil, fmt.Errorf("%w (at %s): no data key", ErrKeyMissing, e.path)
	}
	aead, err := e.aead(e.key)
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, fmt.Errorf("%s is truncated; run `teams auth login` again", e.path)
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(e.user))
	if err != nil {
		return nil, fmt.Errorf("the token cache failed authentication (tampered, or the key changed); run `teams auth login` again: %w", err)
	}
	return plain, nil
}

// Write implements Store. This is the only path that may create a keychain item,
// because a write means there is real token material to protect.
func (e *Envelope) Write(ctx context.Context, data []byte) error {
	if err := e.startFreshSessionIfNeeded(); err != nil {
		return err
	}
	if err := e.Probe(); err != nil {
		return err
	}
	if e.fallback == nil && e.key == nil {
		key, err := e.createKey()
		switch {
		case err == nil:
			e.key = key
		case errors.Is(err, errMalformedKey):
			return fmt.Errorf("%w: %w", ErrKeyMissing, err)
		default:
			// The keychain cannot hold a key here; keep the tokens, in the
			// plaintext 0600 fallback, and say so once.
			e.degrade(err)
		}
	}
	if e.fallback != nil {
		return e.fallback.Write(ctx, data)
	}
	aead, err := e.aead(e.key)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate a nonce: %w", err)
	}
	if err := store.WriteFile(e.path, aead.Seal(nonce, nonce, data, []byte(e.user))); err != nil {
		return err
	}
	// Once the ciphertext is authoritative, a stale plaintext cache from an
	// earlier no-keychain run must go: two copies of a refresh token are one too
	// many, and the plaintext one is the unprotected one.
	return store.RemoveIfExists(e.plainPath)
}

// Delete implements Store: it removes the ciphertext and the keychain item, so
// the next login mints a fresh key instead of reusing a stale one.
//
// A keychain item that cannot be removed is reported through Warning rather than
// as a failure: the ciphertext is gone, so the tokens are unreachable either way,
// and a locked or unreachable keychain must not make `auth logout` fail. A
// ProfileNotFound key can happen whenever the environment has no keychain at all,
// which is exactly where a plaintext fallback would have been used.
func (e *Envelope) Delete(ctx context.Context) error {
	if e.fallback != nil {
		return e.fallback.Delete(ctx)
	}
	if err := store.RemoveIfExists(e.path); err != nil {
		return err
	}
	if err := e.keyring.Delete(KeyringService, e.user); err != nil && !errors.Is(err, ErrNotFound) {
		if e.warning == "" {
			e.warning = fmt.Sprintf("the cached tokens are deleted, but the data key could not be removed from the OS keychain (%v); remove the %q item for %q by hand if you want it gone",
				err, KeyringService, e.user)
		}
	}
	return nil
}

// aead builds the AES-GCM cipher from an already resolved key.
func (e *Envelope) aead(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: the data key is %d bytes, want 32", errMalformedKey, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build the cache cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// ErrNotFound is what a Keyring implementation must return when it has no item
// for a profile. It is exported because the envelope store has to tell "no key
// yet" (mint one on write) apart from "the keychain is unreachable" (fall back to
// the plaintext file), and any Keyring implementation — the OS one, a fake in a
// test, a future platform backend — has to be able to say which it is.
var ErrNotFound = errors.New("no such item in the keyring")

// errMalformedKey means the keychain answered, but with something that is not a
// 32-byte data key: a corrupted item, or an item another program wrote.
var errMalformedKey = errors.New("the keychain holds a malformed data key")

// startFreshSessionIfNeeded lets a write recover from a cache whose key is gone
// or unusable (a keychain reset, a restored backup, a deleted item, my own test
// suite: it used to share the keychain item name).
//
// A read must keep failing — the bytes are undecryptable, and silently switching
// to a plaintext store would hide that — but a write is an explicit "start a new
// session": the old tokens are unreachable anyway, so minting a fresh key and
// overwriting the ciphertext is the only way a user can get back in without
// hand-deleting files. The reason is reported through Warning.
func (e *Envelope) startFreshSessionIfNeeded() error {
	err := e.Probe()
	if !errors.Is(err, ErrKeyMissing) {
		return err
	}
	e.keyErr = nil
	e.key = nil
	note := fmt.Sprintf("the previous token cache could not be decrypted with the key in your OS keychain, so this is a new session; %s has been overwritten", e.path)
	if key, keyErr := e.createKey(); keyErr == nil {
		e.key = key
		e.warning = note
	} else {
		// No keychain to hold a key either: fall back to the plaintext file,
		// which is what a profile without a keychain uses from the start. Keep
		// both facts in the warning.
		e.degrade(keyErr)
		e.warning = note + "; " + e.warning
	}
	return nil
}

// existingKey reads the data key, returning (nil, nil) when the keychain has no
// item for this profile yet. It never creates one.
func (e *Envelope) existingKey() ([]byte, error) {
	enc, err := e.keyring.Get(KeyringService, e.user)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	key, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if decErr != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: run `teams auth logout` and sign in again", errMalformedKey)
	}
	return key, nil
}

// createKey mints and stores a fresh 32-byte data key.
func (e *Envelope) createKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate a data key: %w", err)
	}
	if err := e.keyring.Set(KeyringService, e.user, base64.StdEncoding.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("store the data key in the keychain: %w", err)
	}
	return key, nil
}

// CiphertextExists reports whether an encrypted cache file is present. A key
// that is gone while ciphertext remains is the case that needs the re-login
// message.
func (e *Envelope) CiphertextExists() bool {
	_, err := os.Stat(e.path)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

var _ Store = (*Envelope)(nil)
