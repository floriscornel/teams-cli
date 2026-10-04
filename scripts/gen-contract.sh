#!/usr/bin/env bash
#
# gen-contract.sh — regenerate the committed Layer 6 contract inputs.
#
#   internal/testing/testdata/openapi/graph-v1.0-trimmed.yaml
#   internal/testing/testdata/openapi/routes.txt
#   internal/testing/testdata/openapi/provenance.json
#
# What it does
# ------------
# It runs the generator in internal/testing/contract/cmd/gencontract, which
#
#   1. parses refs/graph/api-reference/v1.0/api/*.md and checks that every
#      committed route is documented on the page it cites (routes.go +
#      parse.go). This runs FIRST, because a route that no page documents must
#      never reach the committed list: the OpenAPI description declares
#      operations with no api-reference page at all — CreateHostedContents, the
#      call teams-mcp gets wrong — so deriving the routes from spec operations
#      would bless that bug (refs/INDEX.md, PLAN.md Layer 6);
#   2. trims refs/openapi/openapi/v1.0/openapi.yaml by the $ref closure of the
#      routes we use, re-keys the paths to the api-reference spelling, and
#      applies the repair rules in repair.go (the @odata.type "required"
#      stripping and the discriminator-mapping pruning);
#   3. writes the three files above, deterministically.
#
# Read internal/testing/testdata/openapi/README.md for the full trim rules, the
# recorded divergences from Microsoft's copy, and the endpoints that are
# deliberately left out.
#
# Inputs
# ------
# refs/ is a gitignored mirror and must be present. Reproduce it with
#
#   scripts/fetch-refs.sh
#
#   refs/openapi/openapi/v1.0/openapi.yaml         (microsoftgraph/msgraph-metadata)
#   refs/graph/api-reference/v1.0/api/*.md         (microsoftgraph/microsoft-graph-docs-contrib)
#   refs/MANIFEST.md                               (the pinned SHAs, recorded in provenance.json)
#
# The generator exits with a clear message when any of them is missing.
#
# Environment
# -----------
# A sandboxed runner cannot write the global Go cache, so the caches default to
# the checkout, exactly like the Makefile does. Override any of them.
#
# Usage
# -----
#   scripts/gen-contract.sh              regenerate the committed files
#   scripts/gen-contract.sh --verify     fail (exit 1) when a re-run would change them
#   scripts/gen-contract.sh --list       print the route list and exit
#   scripts/gen-contract.sh --stdout     print the trimmed spec to stdout
#
# In CI this runs as the `refs-check` job (PLAN.md "CI/CD"), followed by
# `git diff --exit-code`. The same check also runs as the in-process test
# TestCommittedArtifactsAreCurrent, which skips when refs/ is absent.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT="$PWD"

GO="${GO:-go}"

export GOPATH="${GOPATH:-$ROOT/.cache/gopath}"
export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"

command -v "$GO" >/dev/null 2>&1 || {
    echo "gen-contract: '$GO' is not on the PATH" >&2
    exit 1
}

if [[ ! -f "$ROOT/refs/openapi/openapi/v1.0/openapi.yaml" ]]; then
    echo "gen-contract: refs/openapi/openapi/v1.0/openapi.yaml is missing." >&2
    echo "gen-contract: refs/ is a gitignored mirror; run scripts/fetch-refs.sh first." >&2
    exit 1
fi

case "${1:-}" in
    --verify)
        generated="$(mktemp -d)"
        trap 'rm -rf "$generated"' EXIT
        cp "$ROOT/internal/testing/testdata/openapi/graph-v1.0-trimmed.yaml" "$generated/spec"
        cp "$ROOT/internal/testing/testdata/openapi/routes.txt" "$generated/routes"
        cp "$ROOT/internal/testing/testdata/openapi/provenance.json" "$generated/provenance"
        "$GO" run ./internal/testing/contract/cmd/gencontract -root "$ROOT"
        git diff --exit-code -- internal/testing/testdata/openapi || {
            echo "gen-contract: the committed contract inputs are stale (see the diff above)." >&2
            echo "gen-contract: re-run scripts/gen-contract.sh and commit the result." >&2
            exit 1
        }
        echo "gen-contract: committed contract inputs are current"
        ;;
    --list)
        "$GO" run ./internal/testing/contract/cmd/gencontract -root "$ROOT" -list
        ;;
    --stdout)
        "$GO" run ./internal/testing/contract/cmd/gencontract -root "$ROOT" -stdout
        ;;
    "")
        "$GO" run ./internal/testing/contract/cmd/gencontract -root "$ROOT"
        ;;
    *)
        echo "gen-contract: unknown argument '$1' (see the header of this script)" >&2
        exit 2
        ;;
esac
