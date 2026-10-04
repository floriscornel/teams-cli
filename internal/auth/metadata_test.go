package auth

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/store"
)

func TestMetadataRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	want := Metadata{
		Profile:       "me",
		HomeAccountID: "uid.utid",
		Account:       "alice@example.com",
		TenantID:      "tenant",
		LastRefresh:   now,
		Scopes:        []string{"User.Read"},
	}
	if err := SaveMetadata(path, want); err != nil {
		t.Fatal(err)
	}
	got := LoadMetadata(path)
	if got.HomeAccountID != want.HomeAccountID || got.Account != want.Account || !got.LastRefresh.Equal(now) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if got.Version != metadataVersion {
		t.Errorf("Version = %d, want %d", got.Version, metadataVersion)
	}
}

func TestLoadMetadataToleratesGarbage(t *testing.T) {
	dir := t.TempDir()
	missing := LoadMetadata(filepath.Join(dir, "nope.json"))
	if !missing.LastRefresh.IsZero() || missing.Version != metadataVersion {
		t.Errorf("missing metadata = %+v", missing)
	}
	broken := filepath.Join(dir, "broken.json")
	if err := store.WriteFile(broken, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	got := LoadMetadata(broken)
	if !got.LastRefresh.IsZero() {
		t.Errorf("broken metadata = %+v", got)
	}
}

func TestRefreshAgeAndWarning(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	empty := Metadata{}
	if empty.RefreshAge(now) != 0 || empty.RefreshWarning(now) != "" {
		t.Error("empty metadata should report no age and no warning")
	}
	fresh := Metadata{LastRefresh: now.Add(-24 * time.Hour)}
	if age := fresh.RefreshAge(now); age != 24*time.Hour {
		t.Errorf("RefreshAge = %v", age)
	}
	if fresh.RefreshWarning(now) != "" {
		t.Errorf("a one-day-old refresh warned: %q", fresh.RefreshWarning(now))
	}
	old := Metadata{LastRefresh: now.Add(-80 * 24 * time.Hour)}
	warning := old.RefreshWarning(now)
	if !strings.Contains(warning, "80 days") || !strings.Contains(warning, "90 days") {
		t.Errorf("warning = %q", warning)
	}
	future := Metadata{LastRefresh: now.Add(time.Hour)}
	if future.RefreshAge(now) != 0 {
		t.Error("a future timestamp should clamp to zero")
	}
}
