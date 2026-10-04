package tokenstore

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// SystemKeyring adapts zalando/go-keyring to our Keyring interface and
// normalises its "not found" sentinel, so the envelope store can tell "no key
// yet" apart from "the keychain is unreachable".
//
// go-keyring v0.2.8 is deliberately cgo-free: macOS runs /usr/bin/security with
// the secret on stdin (never argv), Linux speaks D-Bus to the Secret Service,
// and Windows uses wincred. That is what keeps CGO_ENABLED=0 shipping.
type SystemKeyring struct{}

// Get implements Keyring.
func (SystemKeyring) Get(service, user string) (string, error) {
	secret, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNotFound
	}
	return secret, err
}

// Set implements Keyring.
func (SystemKeyring) Set(service, user, secret string) error {
	return keyring.Set(service, user, secret)
}

// Delete implements Keyring.
func (SystemKeyring) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

var _ Keyring = SystemKeyring{}
