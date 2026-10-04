package tokenstore

import (
	"fmt"
	"strings"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/store"
)

// Open builds the store a profile selected.
//
//	"auto" (default)  envelope store: ciphertext at <state>/<profile>/token.bin,
//	                  data key in the OS keychain, plaintext fallback at
//	                  <state>/<profile>/token.json with a one-time warning
//	"file"            the 0600 plaintext file
//	"file://<path>"   the 0600 plaintext file at an explicit path (containers)
//	"keyvault://…"    one secret per profile, written back after every refresh
//
// keyUser names the keychain item; it includes the profile so two identities
// never share key material.
func Open(paths store.Paths, eff config.Effective, kr Keyring) (Store, error) {
	spec := strings.TrimSpace(eff.TokenStore)
	switch {
	case spec == "" || spec == config.TokenStoreAuto:
		if kr == nil {
			kr = SystemKeyring{}
		}
		return NewEnvelope(paths.TokenFile(), paths.TokenFilePlain(), keyUser(eff.Name), kr)
	case spec == config.TokenStoreFile:
		return NewFile(paths.TokenFilePlain()), nil
	case strings.HasPrefix(spec, config.TokenStoreFileURL):
		path := strings.TrimPrefix(spec, config.TokenStoreFileURL)
		if path == "" {
			return nil, fmt.Errorf("token_store %q needs a path", spec)
		}
		return NewFile(path), nil
	case strings.HasPrefix(spec, config.TokenStoreKeyVault):
		target, err := config.ParseKeyVaultURL(spec)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("token_store %s: %w (the Key Vault backend lands in Phase 7; use auto or file for now)", target, ErrNotImplemented)
	default:
		return nil, fmt.Errorf("unknown token_store %q", spec)
	}
}

// keyUser is the keychain account name for a profile's data key.
func keyUser(profile string) string { return profile + ":token-key" }
