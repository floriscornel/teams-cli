//go:build keyring

// This file is the real-OS-keychain integration test PLAN.md asks for
// ("Real OS keychain: an integration test behind a build tag"). It is excluded
// from ordinary runs with `//go:build keyring`, so `go test ./...` never depends
// on a keychain being present:
//
//	TEAMS_TEST_ALLOW_KEYCHAIN=1 go test -tags keyring ./internal/auth/tokenstore/
//
// It uses a random item name and deletes it again, so it can never collide with
// a profile's data key — the item name in production is
// `teams-cli/<profile>:token-key`, and the envelope store's `Delete` removes it.
package tokenstore

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSystemKeyringRoundTrip(t *testing.T) {
	kr := SystemKeyring{}
	service := "teams-cli-integration-test"
	user := "keyring-integration"

	if err := kr.Set(service, user, "probe"); err != nil {
		t.Skipf("no usable OS keychain here: %v", err)
	}
	t.Cleanup(func() { _ = kr.Delete(service, user) })

	if err := kr.Set(service, user, "hello"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := kr.Get(service, user)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "hello" {
		t.Errorf("Get = %q, want %q", got, "hello")
	}
	if err := kr.Delete(service, user); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kr.Get(service, user); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	// Deleting again is a no-op rather than an error, which is what `auth logout`
	// relies on.
	if err := kr.Delete(service, user); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

func TestEnvelopeWithTheRealKeychain(t *testing.T) {
	kr := SystemKeyring{}
	service := "teams-cli-integration-test"
	user := "envelope-integration"
	if err := kr.Set(service, user, ""); err != nil {
		t.Skipf("no usable OS keychain here: %v", err)
	}
	_ = kr.Delete(service, user)

	dir := t.TempDir()
	cipherPath := dir + "/token.bin"
	plainPath := dir + "/token.json"
	e, err := NewEnvelope(cipherPath, plainPath, user, kr)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Probe(); err != nil {
		t.Skipf("no usable OS keychain here: %v", err)
	}
	if !e.Encrypted() {
		t.Skipf("the keychain is unreachable, so the store fell back: %s", e.Warning())
	}
	t.Cleanup(func() { _ = e.Delete(context.Background()) })

	ctx := context.Background()
	payload := []byte(`{"AccessToken":{"home_account_id":"integration"}}`)
	if err := e.Write(ctx, payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == string(payload) {
		t.Fatal("the cache was written in the clear")
	}
	got, err := e.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("Read = %q, want %q", got, payload)
	}
	if err := e.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kr.Get(service, user); !errors.Is(err, ErrNotFound) {
		t.Errorf("the data key survived Delete: %v", err)
	}
}
