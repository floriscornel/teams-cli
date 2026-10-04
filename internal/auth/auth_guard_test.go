package auth

import (
	"os"
	"testing"
)

// TestMain keeps the test process away from the developer's real OS keychain.
// The envelope store's keychain item name is fixed (teams-cli/<profile>:token-key),
// so a test that reached the real keychain could read — or delete — a user's live
// data key. Set TEAMS_TEST_ALLOW_KEYCHAIN=1 only for the integration test that
// intentionally exercises the real keychain.
func TestMain(m *testing.M) {
	if os.Getenv("TEAMS_TEST_ALLOW_KEYCHAIN") == "" {
		_ = os.Setenv("TEAMS_NO_KEYCHAIN", "1")
	}
	os.Exit(m.Run()) //nolint:forbidigo // TestMain is the one place a test binary may exit
}
