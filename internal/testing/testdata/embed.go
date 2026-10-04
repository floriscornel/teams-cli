// Package testdata carries the committed Layer 6 contract inputs — the trimmed
// Microsoft Graph v1.0 OpenAPI description and the api-reference route list —
// and embeds them so that `go test ./...` needs no local refs/ mirror and no
// network access (PLAN.md: "Tests never need refs/").
//
// It is a Go package only because //go:embed cannot reach into a parent
// directory: internal/testing/contract needs these files, and the committed
// location is fixed by the task's scope (internal/testing/testdata/openapi/).
// Nothing here is production code, and internal/testing/... is excluded from
// the repo-wide coverage floor (the `mise run cover` exclusion list in
// mise.toml).
package testdata

import "embed"

// OpenAPI holds the committed contract artifacts:
//
//	openapi/graph-v1.0-trimmed.yaml  the trimmed, deterministic OpenAPI subset
//	openapi/routes.txt               the route list, generated from the
//	                                 api-reference and not from the description
//	openapi/provenance.json          the source SHA, trim rules and divergences
//	openapi/README.md                the human-readable version of the above
//
//go:embed openapi
var OpenAPI embed.FS
