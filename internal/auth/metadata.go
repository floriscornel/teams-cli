package auth

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/floriscornel/teams-cli/internal/store"
)

// metadataVersion lets a future format change migrate instead of guessing.
const metadataVersion = 1

// RefreshTokenLifetime is the documented maximum age of a refresh token
// (refs/entra/docs/identity-platform/refresh-tokens.md: 90 days, sliding while
// it keeps being used). We warn well before it, because a dead bot token means a
// failed job rather than a prompt.
const (
	RefreshTokenLifetime = 90 * 24 * time.Hour
	refreshWarnAfter     = 75 * 24 * time.Hour
)

// Metadata is the bookkeeping we keep ourselves, because MSAL's cache blob is
// explicitly opaque and unsupported: the refresh-token age cannot be read out of
// it (PLAN.md:92). It lives beside the token cache with the same 0600/0700 rules.
type Metadata struct {
	Version       int       `json:"version"`
	Profile       string    `json:"profile,omitempty"`
	HomeAccountID string    `json:"home_account_id,omitempty"`
	Account       string    `json:"account,omitempty"`
	TenantID      string    `json:"tenant_id,omitempty"`
	LastRefresh   time.Time `json:"last_refresh,omitempty"`
	Scopes        []string  `json:"scopes,omitempty"`
}

// LoadMetadata reads the metadata file; a missing or unreadable file yields an
// empty struct, because the file only ever improves the output.
func LoadMetadata(path string) Metadata {
	data, err := store.ReadFile(path)
	if err != nil || len(data) == 0 {
		return Metadata{Version: metadataVersion}
	}
	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return Metadata{Version: metadataVersion}
	}
	return m
}

// SaveMetadata writes the metadata file atomically as 0600.
func SaveMetadata(path string, m Metadata) error {
	m.Version = metadataVersion
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth metadata: %w", err)
	}
	return store.WriteFile(path, append(data, '\n'))
}

// RefreshAge reports how long ago the refresh token was last renewed.
func (m Metadata) RefreshAge(now time.Time) time.Duration {
	if m.LastRefresh.IsZero() {
		return 0
	}
	age := now.Sub(m.LastRefresh)
	if age < 0 {
		return 0
	}
	return age
}

// RefreshWarning returns a warning when the refresh token is close to its
// documented 90-day lifetime, and "" when it is not (or when we have no
// timestamp yet).
func (m Metadata) RefreshWarning(now time.Time) string {
	age := m.RefreshAge(now)
	if age == 0 || age < refreshWarnAfter {
		return ""
	}
	days := int(age.Hours() / 24)
	return fmt.Sprintf("the refresh token has not been renewed for %d days; it expires after %d days, so run `teams auth refresh` or re-run the login",
		days, int(RefreshTokenLifetime.Hours()/24))
}
