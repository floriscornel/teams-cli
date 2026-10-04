// Package cloud holds the per-cloud endpoint bases PLAN.md's `cloud` setting
// selects (global, usgov, china). Everything else that needs a URL takes it
// from here, so a sovereign cloud is a config value and never a code path.
package cloud

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Endpoints is one Microsoft cloud.
type Endpoints struct {
	// Name is the config value: global, usgov or china.
	Name string
	// Graph is the Microsoft Graph base URL, without a trailing slash. An empty
	// value means the docs mirror does not state it for this cloud, so the
	// profile must supply graph_base_url rather than us guessing a host.
	Graph string
	// AuthorityHost is the Entra authority base, without a tenant segment.
	AuthorityHost string
	// KeyVaultSuffix is appended to a vault name to form its DNS name.
	KeyVaultSuffix string
	// Documented is the refs/ path that states these values, or "" when the
	// mirror does not state them (then the value is marked unverified below).
	Documented string
}

// Authority returns the MSAL authority for a tenant. The tenant path segment is
// mandatory: MSAL rejects an authority without one
// (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:533-536).
func (e Endpoints) Authority(tenant string) string {
	if tenant == "" {
		tenant = "common"
	}
	return strings.TrimSuffix(e.AuthorityHost, "/") + "/" + tenant
}

// GraphURL joins a path onto the cloud's Graph base URL.
func (e Endpoints) GraphURL(path string) string {
	return strings.TrimSuffix(e.Graph, "/") + "/" + strings.TrimPrefix(path, "/")
}

// KeyVaultURL returns the vault URL for a vault name in this cloud.
func (e Endpoints) KeyVaultURL(vault string) string {
	return "https://" + vault + e.KeyVaultSuffix + "/"
}

// Cloud table. The public-cloud values are the ones the Phase 1 spike used; the
// sovereign values come from the Entra national-cloud documentation mirrored
// under refs/entra/ (see Documented). Where the mirror does not state a value,
// Documented is empty and `teams doctor` reports it as unverified rather than
// pretending it was checked.
var table = map[string]Endpoints{
	"global": {
		Name:           "global",
		Graph:          "https://graph.microsoft.com/v1.0",
		AuthorityHost:  "https://login.microsoftonline.com",
		KeyVaultSuffix: ".vault.azure.net",
		Documented:     "refs/openapi/openapi/v1.0/openapi.yaml:10 (servers), refs/entra/docs/identity-platform/authentication-national-cloud.md:66",
	},
	"usgov": {
		Name:           "usgov",
		Graph:          "https://graph.microsoft.us/v1.0",
		AuthorityHost:  "https://login.microsoftonline.us",
		KeyVaultSuffix: ".vault.usgovcloudapi.net",
		Documented:     "refs/entra/docs/identity-platform/msal-national-cloud.md:83, refs/entra/docs/identity-platform/authentication-national-cloud.md:64",
	},
	"china": {
		Name: "china",
		// The mirror documents the China authority but NOT the Graph service
		// root: refs/entra/docs/identity-platform/authentication-national-cloud.md:81
		// defers to /graph/deployments, which is not vendored. Guessing a host
		// would be exactly the "never guess an API shape" failure AGENTS.md
		// forbids, so we ask the profile for it instead.
		Graph:          "",
		AuthorityHost:  "https://login.partner.microsoftonline.cn",
		KeyVaultSuffix: ".vault.azure.cn",
		Documented:     "refs/entra/docs/identity-platform/authentication-national-cloud.md:65 (authority only; see :81 for the missing Graph root)",
	},
}

// ErrGraphBaseUndocumented is returned when a cloud's Graph service root is not
// in the docs mirror and the profile did not supply one.
var ErrGraphBaseUndocumented = errors.New("the docs mirror does not document this cloud's Graph service root; set graph_base_url in the profile")

// DocumentedGraphBase reports whether the mirror states the Graph base URL.
func (e Endpoints) DocumentedGraphBase() bool { return e.Graph != "" }

// Lookup returns the endpoints for a cloud name.
func Lookup(name string) (Endpoints, bool) {
	if name == "" {
		name = "global"
	}
	e, ok := table[strings.ToLower(name)]
	return e, ok
}

// MustLookup panics on an unknown cloud; used for the compiled-in default.
func MustLookup(name string) Endpoints {
	e, ok := Lookup(name)
	if !ok {
		panic(fmt.Sprintf("unknown cloud %q", name))
	}
	return e
}

// Names lists the supported cloud names, sorted.
func Names() []string {
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
