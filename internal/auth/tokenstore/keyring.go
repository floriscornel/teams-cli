package tokenstore

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// SystemKeyring adapts zalando/go-keyring to our Keyring interface and
// normalises its "not found" sentinel, so the envelope store can tell "no key
// yet" apart from "the keychain is unreachable".
//
// Every method honours TEAMS_NO_KEYCHAIN, which is the one switch that keeps a
// process away from the OS keychain entirely: tests set it so a run can never
// read or (worse) delete a developer's real key, and containers use it because
// they have no keychain at all. Putting the check here rather than only in the
// envelope store means no future caller can bypass it.
//
// go-keyring v0.2.8 is deliberately cgo-free: macOS runs /usr/bin/security with
// the secret on stdin (never argv), Linux speaks D-Bus to the Secret Service,
// and Windows uses wincred. That is what keeps CGO_ENABLED=0 shipping.
type SystemKeyring struct{}

// noKeychainError is what the keychain methods report when TEAMS_NO_KEYCHAIN is
// set, so the envelope store degrades to its plaintext file exactly as it would
// on a machine without a keychain.
func noKeychainError() error {
	return fmt.Errorf("%s is set, so the OS keychain is not used", EnvNoKeychain)
}

// Get implements Keyring.
func (SystemKeyring) Get(service, user string) (string, error) {
	if truthyEnv(os.Getenv(EnvNoKeychain)) {
		return "", noKeychainError()
	}
	secret, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return secret, err
}

// Set implements Keyring.
func (SystemKeyring) Set(service, user, secret string) error {
	if truthyEnv(os.Getenv(EnvNoKeychain)) {
		return noKeychainError()
	}
	return keyring.Set(service, user, secret)
}

// Delete implements Keyring.
func (SystemKeyring) Delete(service, user string) error {
	if truthyEnv(os.Getenv(EnvNoKeychain)) {
		return nil
	}
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

var _ Keyring = SystemKeyring{}
