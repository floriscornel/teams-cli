# Makefile — the single entry point for humans and CI (PLAN.md "Toolchain and
# local workflow"): every CI step runs a make target, so "works locally" and
# "works in CI" mean the same thing.
#
# Go caches are kept inside the checkout so a sandboxed runner (no write access
# to the global Go cache) works unchanged. Override with GOCACHE=/tmp/... if you
# prefer the global cache.

SHELL := /usr/bin/env bash
GO ?= go

export GOPATH      ?= $(CURDIR)/.cache/gopath
export GOMODCACHE  ?= $(GOPATH)/pkg/mod
export GOCACHE     ?= $(CURDIR)/.cache/go-build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Coverage scope (PLAN.md "Coverage scope"): only the thin main wrapper, the
# fakes/dev tooling under internal/testing and generated code are excluded.
# Changing this list needs review.
COVER_EXCLUDE := cmd/teams/main.go internal/testing
COVERAGE_MIN  ?= 80
COVERPROFILE  ?= coverage.out

GO_SOURCES := $(shell find cmd internal -name '*.go' 2>/dev/null)

# The lint/format scope: the repo's own packages. refs/ is a vendored mirror of
# other projects' Go sources and spike/ is throwaway, so neither is ours to
# format or lint.
PKGS := ./cmd/... ./internal/...

.PHONY: all
all: check

## Development

.PHONY: build
build: bin/teams ## Build the CLI into bin/teams

bin/teams: $(GO_SOURCES) go.mod go.sum
	@mkdir -p bin
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/teams ./cmd/teams

.PHONY: install
install: ## Install the CLI into GOBIN
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/teams

.PHONY: tidy
tidy: ## go mod tidy, then fail if it changed anything
	$(GO) mod tidy
	@git diff --exit-code -- go.mod go.sum || { \
		echo "go.mod/go.sum were dirty after 'go mod tidy'"; exit 1; }

.PHONY: fmt
fmt: ## Format the tree (gofumpt + goimports through golangci-lint)
	golangci-lint fmt $(PKGS)

.PHONY: fmt-check
fmt-check: ## Fail when the tree is not formatted
	golangci-lint fmt --diff $(PKGS)

## Verification

.PHONY: check
check: fmt-check tidy lint test cover ## Everything CI gates on

.PHONY: test
test: ## Unit, fake and testscript tests with the race detector
	$(GO) test -race -shuffle=on ./...

.PHONY: test-short
test-short: ## Tests without the race detector (fast local loop)
	$(GO) test -shuffle=on ./...

.PHONY: cover
cover: $(COVERPROFILE) ## Enforce the coverage floor on non-excluded code
	$(GO) run ./internal/testing/coveragecheck -profile $(COVERPROFILE) -min $(COVERAGE_MIN) -exclude '$(COVER_EXCLUDE)' 

$(COVERPROFILE): $(GO_SOURCES)
	$(GO) test -race -covermode=atomic -coverprofile=$(COVERPROFILE) ./...

.PHONY: lint
lint: ## golangci-lint (config in .golangci.yml)
	golangci-lint run $(PKGS)

.PHONY: vuln
vuln: ## govulncheck
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: fuzz-short
fuzz-short: ## Short fuzz run for every fuzz target (nightly uses a longer budget)
	@for target in $$(grep -rhoE '^func (Fuzz[A-Za-z0-9_]+)' --include='*_test.go' cmd internal | awk '{print $$2}' | sort -u); do \
		for pkg in $$(grep -rlE "^func $$target" --include='*_test.go' cmd internal | xargs -n1 dirname | sort -u); do \
			echo "fuzzing $$pkg/$$target"; \
			$(GO) test -run '^$$' -fuzz "^$$target$$" -fuzztime $${FUZZTIME:-15s} ./$$pkg || exit 1; \
		done; \
	done

.PHONY: docs
docs: ## Regenerate docs/commands and the man pages
	$(GO) run ./internal/testing/docgen -dir docs

.PHONY: docs-check
docs-check: docs ## Fail when generated docs are out of date
	@git diff --exit-code -- docs || { echo "docs are out of date: run 'make docs'"; exit 1; }

.PHONY: refs-check
refs-check: ## Verify the vendored reference mirror (needs scripts/fetch-refs.sh to have run)
	scripts/fetch-refs.sh --verify

.PHONY: snapshot
snapshot: ## Cross-compile the release matrix locally (no publish)
	goreleaser build --snapshot --clean

.PHONY: release-check
release-check: ## Validate .goreleaser.yaml
	goreleaser check

.PHONY: smoke
smoke: build ## Install-free smoke test of the built binary
	./scripts/smoke.sh bin/teams

.PHONY: smoke-live
smoke-live: ## Maintainer-only live smoke test against a real tenant (TEAMS_E2E=1)
	@test "$$TEAMS_E2E" = "1" || { echo "set TEAMS_E2E=1 to run the live smoke test"; exit 1; }
	$(GO) test -tags e2e -run TestLiveSmoke ./internal/testing/live/...

.PHONY: record
record: ## Maintainer-only: record scrubbed real responses into fixtures
	@test "$$TEAMS_E2E" = "1" || { echo "set TEAMS_E2E=1 to record cassettes"; exit 1; }
	$(GO) test -tags record -run TestRecord ./internal/testing/record/...

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf bin dist $(COVERPROFILE)

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
