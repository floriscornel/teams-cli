package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// manifestPath is the tracked, generated list of pinned mirror SHAs
// (AGENTS.md: "refs/MANIFEST.md is generated: never hand-edit it").
const manifestPath = "refs/MANIFEST.md"

// provenanceRecord is the committed record of where the trimmed spec came from
// and precisely how it differs from Microsoft's original.
type provenanceRecord struct {
	Source struct {
		Path       string `json:"path"`
		Repository string `json:"repository"`
		SHA        string `json:"sha"`
		Mirror     string `json:"mirror"`
	} `json:"source"`
	Generator string `json:"generator"`
	Outputs   struct {
		TrimmedSpec string `json:"trimmedSpec"`
		RouteList   string `json:"routeList"`
	} `json:"outputs"`
	Counts struct {
		PathKeys            int `json:"pathKeysInSource"`
		PathsKept           int `json:"pathsKept"`
		SchemasKept         int `json:"schemasKept"`
		ResponsesKept       int `json:"responsesKept"`
		ParametersKept      int `json:"parametersKept"`
		RequestBodiesKept   int `json:"requestBodiesKept"`
		ODataTypeRequired   int `json:"odataTypeRequiredStripped"`
		DiscriminatorPruned int `json:"discriminatorMappingEntriesPruned"`
		AliasRewrites       int `json:"pathAliasRewrites"`
	} `json:"counts"`
	TrimRules   []string `json:"trimRules"`
	Divergences []string `json:"divergences"`
}

// trimRules documents, in order, what the generator does. Every entry names the
// refs/ path it is derived from where one exists.
var trimRules = []string{
	"Keep only the paths listed in internal/testing/contract/routes.go, which is generated from the api-reference (refs/graph/api-reference/v1.0/api/) and not from the description. The description declares operations with no api-reference page (CreateHostedContents), so deriving routes from spec operations would bless a bug (refs/INDEX.md, PLAN.md Layer 6).",
	"Keep only the HTTP methods the CLI uses on those paths: GET, POST, PATCH, PUT, DELETE.",
	"Match a route to a spec path by path shape (every {parameter} compared as a wildcard, because the two sources disagree on parameter names), preferring the candidate whose externalDocs URL names the api-reference page the route came from.",
	"Re-key every retained path to the api-reference spelling (/groups/{group-id}/team/channels/... becomes /teams/{team-id}/channels/...) and rename its path-parameter declarations to match, so the trimmed copy matches what internal/graph sends.",
	"Keep the whole components/$ref closure reachable from the retained paths: schemas, responses, parameters and requestBodies.",
	"Drop components/examples entirely (they are illustrative only and are the single largest block in the source).",
	"Drop every path and component the closure does not reach. The retained operations keep their x-ms-* vendor extensions, since those belong to the operation node.",
	"Validate the result with kin-openapi before writing it, so a malformed trim fails the generator instead of the first contract test.",
	"Sort path keys and component names lexicographically and emit block-style YAML with a 2-space indent, so a re-run produces a byte-identical file (PLAN.md: the refs-check job fails on git diff).",
}

// divergences documents the semantic changes: places where the trimmed copy no
// longer says what Microsoft's original says, and why.
var divergences = []string{
	"@odata.type is no longer required. The source marks it required in thousands of schemas, including microsoft.graph.entity and microsoft.graph.chatMessage, while the api-reference says only body is mandatory on a write (refs/graph/api-reference/v1.0/api/channel-post-messages.md, 'Request body': 'Only the body property is mandatory. All other properties are optional.'). The property stays declared, so sending it is still validated. See PLAN.md Layer 6 and stripODataTypeRequired.",
	"Discriminator mappings only list schemas that survived the trim. The source's microsoft.graph.entity mapping lists every entity type in the description; keeping it would pull the whole description back in through the $ref closure. kin-openapi only reads a mapping when the input carries the discriminator property, and the surviving entries are exactly the reachable ones.",
	"Path parameter names follow the api-reference (team-id, channel-id, message-id, reply-id, hosted-content-id), not the description (group-id, chatMessage-id, chatMessage-id1). The two sources spell the same endpoints differently.",
}

// provenanceJSON renders the committed provenance record.
func provenanceJSON(root string, st stats) ([]byte, error) {
	var rec provenanceRecord
	rec.Source.Path = openAPISource
	rec.Source.Repository = "microsoftgraph/msgraph-metadata"
	rec.Source.Mirror = "scripts/fetch-refs.sh (AGENTS.md); refs/ is gitignored and untrusted reference data"
	sha, err := pinnedSHA(root)
	if err != nil {
		return nil, err
	}
	rec.Source.SHA = sha
	rec.Generator = "internal/testing/contract (scripts/gen-contract.sh)"
	rec.Outputs.TrimmedSpec = trimmedSpecPath
	rec.Outputs.RouteList = routesPath
	rec.Counts.PathsKept = st.pathsKept
	rec.Counts.SchemasKept = st.schemasKept
	rec.Counts.ResponsesKept = st.responsesKept
	rec.Counts.ParametersKept = st.paramsKept
	rec.Counts.RequestBodiesKept = st.bodiesKept
	rec.Counts.ODataTypeRequired = st.odataStrip
	rec.Counts.DiscriminatorPruned = st.discriminatorPruned
	rec.Counts.AliasRewrites = st.aliasRewrites
	rec.Counts.PathKeys = st.pathKeysInSource
	rec.TrimRules = trimRules
	rec.Divergences = divergences
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("contract: marshal provenance: %w", err)
	}
	return append(b, '\n'), nil
}

// manifestOpenAPIRow matches the openapi row of refs/MANIFEST.md:
//
//	| openapi | https://github.com/microsoftgraph/msgraph-metadata | `7b2914c…` | sparse |
var manifestOpenAPIRow = regexp.MustCompile(`(?m)^\|\s*openapi\s*\|\s*(\S+)\s*\|\s*` + "`" + `([0-9a-f]{7,40})` + "`" + `\s*\|`)

// pinnedSHA reads the pinned OpenAPI commit from refs/MANIFEST.md.
func pinnedSHA(root string) (string, error) {
	path := filepath.Join(root, manifestPath)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("contract: read pinned SHA from %s: %w (run scripts/fetch-refs.sh)", path, err)
	}
	m := manifestOpenAPIRow.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("contract: %s has no 'openapi' row with a pinned SHA; fix scripts/fetch-refs.sh", path)
	}
	return string(m[2]), nil
}
