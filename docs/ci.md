# CI, releases and the dev loop

This page describes the automation that lives in `.github/workflows/`, what it
runs locally, and — importantly — which repository secrets it needs. The rules
come from PLAN.md ("CI/CD, releasing and dev experience" and "CI gates"); the
GoReleaser keys are cited against the mirrored docs in
[`refs/goreleaser/www/content/`](../refs/INDEX.md).

## The one rule

**The Makefile is the single entry point.** Every CI step runs a `make` target,
so a green local run means a green CI run:

| Target | Command | Used by |
|---|---|---|
| `make lint` | `golangci-lint run ./...` | `ci.yml` → lint |
| `make fmt-check` | `golangci-lint fmt --diff ./...` | `ci.yml` → lint |
| `make tidy` | `go mod tidy`, then fails if `go.mod`/`go.sum` changed | `ci.yml` → lint |
| `make release-check` | `goreleaser check` | `ci.yml` → lint |
| `make test` | `go test -race -shuffle=on ./...` | `ci.yml` → test, `nightly.yml` → spec-drift |
| `make cover` | coverage profile + the 80% floor | `ci.yml` → coverage |
| `make vuln` | `govulncheck ./...` | `ci.yml` → vuln |
| `make snapshot` | `goreleaser build --snapshot --clean` | `ci.yml` → build |
| `make docs-check` | regenerate `docs/commands` + man pages, fail on diff | `ci.yml` → docs |
| `make refs-check` | `scripts/fetch-refs.sh --verify` | `ci.yml` → refs-check |
| `make fuzz-short` | every `Fuzz*` target with `-fuzztime ${FUZZTIME}` | `nightly.yml` → fuzz |

The documented exceptions are marked with a comment in the workflow:

- the `test` job on Windows runs `go test -race -shuffle=on ./...` directly
  because the GitHub Windows runner image ships no `make` (checked against
  `actions/runner-images`, `images/windows/Windows2025-Readme.md`) and the
  Makefile hardcodes `SHELL := /usr/bin/env bash`;
- `release.yml` calls `goreleaser release --clean` directly because there is no
  `make release` target (see "Gaps" below).

## Workflows

### `ci.yml` — pull requests and pushes to `main`

`permissions: contents: read`, `concurrency` cancels superseded PR runs, and
every third-party action is pinned by 40-hex commit SHA with the release tag in
a trailing comment.

| Job | What it does |
|---|---|
| `lint` | installs the pinned golangci-lint (`install-only`), runs `golangci-lint config verify`, `make fmt-check`, `make lint`, `make tidy`, then installs the pinned GoReleaser and runs `make release-check` |
| `test` | matrix `ubuntu-latest` / `macos-latest` / `windows-latest`, `make test`. Layer 5 `testscript` tests are ordinary Go tests driven by `testscript.Main`, so the same command runs them everywhere |
| `coverage` | `make cover` (one merged `coverage.out`, 80% floor through `internal/testing/coveragecheck`), then uploads `coverage.out` to Codecov |
| `vuln` | `make vuln` |
| `build` | `make snapshot` (the full darwin/linux/windows × amd64/arm64 matrix), uploads `dist/` as the `dist` artifact |
| `smoke` | downloads `dist/`, installs the binary **without** `actions/setup-go` or `actions/setup-node`, runs `teams version`, asserts `teams auth status` exits 3, and asserts no test-only endpoint flag is baked into the binary |
| `docs` | `make docs-check` |
| `refs-changed` | computes whether the mirror inputs changed (see below) |
| `refs-check` | fetches the mirror at the pinned SHAs, verifies it, regenerates the Layer 6 inputs and fails on diff |
| `pr-title` | Conventional Commits check, so GoReleaser's `changelog.use: github` grouping has something to group |

`ci.yml` is also **reusable** (`on.workflow_call`). `nightly.yml` calls it with
`refs-check-only: true`, which makes every other job skip itself through
`if: ${{ inputs['refs-check-only'] != true }}`. That keeps exactly one definition
of the refs-check job, as PLAN.md's workflow table asks for. (A dedicated
`.github/workflows/refs-check.yml` would be the more usual shape; it was not
created because the CI scope for this change is the three workflow files.)

#### When `refs-check` runs

`refs-check` is only worth its 45-minute budget when something that feeds the
mirror changed. The `refs-changed` job diffs the PR/push range and turns the job
on for:

```
scripts/fetch-refs.sh
scripts/gen-contract.sh
refs/INDEX.md
refs/MANIFEST.md
internal/testing/contract/
internal/testing/testdata/openapi/
```

`internal/testing/testdata/openapi/` is where `scripts/gen-contract.sh` writes
the committed generated inputs (the trimmed spec, `routes.txt` and
`provenance.json`).

`refs/MANIFEST.md` pins every mirrored source. The mirror checkout is cached
with `key: refs-${{ hashFiles('refs/MANIFEST.md') }}` and **no** `restore-keys`:
a fallback restore could put an older `refs/MANIFEST.md` back over the
checked-out one and silently verify the wrong SHAs, so a miss just means a fresh
fetch.

`workflow_call` (i.e. the nightly) always reports `refs=true`, so the mirror is
reverified on a schedule.

### `release.yml` — tag `v*`

`permissions: contents: write`, `id-token: write`, `attestations: write`.
A maintainer pushes a signed tag; the workflow then:

1. installs cosign (`sigstore/cosign-installer`) and syft
   (`anchore/sbom-action/download-syft`) — the GoReleaser Action itself installs
   nothing ([`ci/actions.md`](../refs/goreleaser/www/content/customization/ci/actions.md));
2. mints a **GitHub App installation token** with
   `actions/create-github-app-token` scoped to `teams-cli`, `homebrew-tap` and
   `scoop-bucket`. Cross-repo pushes never use a PAT (PLAN.md "Release secrets");
3. runs `goreleaser release --clean`, which builds, packages, writes one SBOM
   per archive with syft, and signs `dist/checksums.txt` with cosign keyless
   into `dist/checksums.txt.sigstore.json`
   ([`blog/cosign-v3.md`](../refs/goreleaser/www/content/blog/cosign-v3.md)).
   Each archive carries the binary, `LICENSE`/`README`/`CHANGELOG` when they
   exist, and the generated man pages under `man/`;
4. attests `dist/checksums.txt` with `actions/attest-build-provenance`, so users
   can run `gh attestation verify --owner floriscornel <artifact>`
   ([`publish/attestations.md`](../refs/goreleaser/www/content/customization/publish/attestations.md));
5. keeps a 90-day copy of the SBOMs, the checksums and the signature bundle as a
   workflow artifact.

**v0.x does not publish to package managers.** During v0.x, releases are cut as
prereleases (`v0.2.0-rc.1`), so `release.prerelease: auto` plus
`skip_upload: auto` on the cask and the Scoop manifest leave the tap and the
bucket untouched. Cutting a plain `v0.x.y` tag would publish them. `winget:`
stays commented out in `.goreleaser.yaml` until v1.0.

### `nightly.yml` — cron `17 4 * * *`

| Job | What it does |
|---|---|
| `fuzz` | `FUZZTIME=10m make fuzz-short` (the Makefile already reads `FUZZTIME`; local default is 15s) |
| `spec-drift` | `make test` — Layer 6 contract validation runs inside the normal suite. The job **never** fetches `refs/` and never resolves a branch tip: the trimmed OpenAPI subset and the api-reference route list are committed and pinned, so a failure means our toolchain or a dependency moved, not Microsoft. On failure it files (at most one) GitHub issue |
| `refs-check` | `uses: ./.github/workflows/ci.yml` with `refs-check-only: true` |

PLAN.md describes the spec-drift job as weekly; running it nightly is a superset
and the suite is cheap.

## Secrets

Nothing is invented here: every workflow reads these with `${{ secrets.NAME }}`,
and each one must be created in **Settings → Secrets and variables → Actions**.

| Secret | Workflow | Required? | What it is |
|---|---|---|---|
| `CODECOV_TOKEN` | `ci.yml` (coverage) | No | Codecov upload token. The upload step uses `fail_ci_if_error: false`, so a missing token warns instead of failing the build. Add the repository to Codecov and store the token to get PR annotations |
| `RELEASE_APP_ID` | `release.yml` | Yes (release) | App ID of the GitHub App used for cross-repo pushes |
| `RELEASE_APP_PRIVATE_KEY` | `release.yml` | Yes (release) | That App's private key (`.pem`) |

### The release GitHub App

Create one App (Settings → Developer settings → GitHub Apps) with:

- **Repository permissions:** `Contents: Read and write` (create the release,
  commit to the tap and the bucket). Nothing else is needed — the App token is
  not used for issues or workflows.
- **Installed on:** `floriscornel/teams-cli`, `floriscornel/homebrew-tap`,
  `floriscornel/scoop-bucket`.

`release.yml` passes that token as `GITHUB_TOKEN` to GoReleaser, so the cask and
Scoop commits are attributed to the App rather than to a person, and the token
expires within the hour.

`nightly.yml` uses the built-in `secrets.GITHUB_TOKEN` with `issues: write` for
the drift issue; that is the automatic per-run token, not a stored secret.

The Apple notarization secrets (`MACOS_SIGN_P12`, `MACOS_SIGN_PASSWORD`,
`MACOS_NOTARY_ISSUER_ID`, `MACOS_NOTARY_KEY_ID`, `MACOS_NOTARY_KEY`) described in
[`sign/notarize.md`](../refs/goreleaser/www/content/customization/sign/notarize.md)
are **not** used in v0.x: the cask documents the manual `xattr` instead (PLAN.md
"Install channels"). Add them together with the `notarize:` block when the
project moves to v1.0.

## Pinned versions and how to bump them

Three layers, all handled by Renovate (`renovate.json`):

1. **Actions** — pinned by full commit SHA with the tag in a trailing comment,
   e.g. `actions/checkout@3d3c42e5... # v7.0.1`. Renovate's `github-actions`
   manager moves the SHA and the comment together.
2. **Tool versions inside workflows** — the `env:` blocks in `ci.yml` and
   `release.yml` carry a `# renovate: datasource=github-releases depName=...`
   comment on the line above the value. Renovate updates them through the
   `customManagers` regex in `renovate.json`. Keep these equal to the versions
   the configuration was validated against locally:
   - golangci-lint `v2.13.2` → `.golangci.yml`
   - GoReleaser `v2.18.2` → `.goreleaser.yaml`
   - cosign `v3.1.3`, syft `v1.54.0` → the `signs:` and `sboms:` pipes
3. **The reference mirror** — pinned in `refs/MANIFEST.md` and bumped only with
   `scripts/fetch-refs.sh --update`, reviewed like any other dependency.

Renovate groups Go modules, GitHub Actions and the CI tool versions into separate
weekly PRs (`schedule: before 6am on monday`), keeps a dependency dashboard
issue, and lets security updates through at any time. Its commit prefix is
`chore(deps)`, which `.goreleaser.yaml` filters out of the changelog.

## Local equivalents

```sh
make check          # fmt-check + tidy + lint + test + cover (everything CI gates on)
make test-short     # the fast loop, no race detector
make snapshot       # the same cross-compile matrix the build job runs
make docs-check     # regenerate docs and fail on drift, like the docs job
make refs-check     # verify the mirror, like the refs-check job
FUZZTIME=10m make fuzz-short   # what the nightly fuzz job runs
make vuln release-check
```

`make smoke-live` and `make record` are maintainer-only and gated on
`TEAMS_E2E=1`; they are deliberately absent from every workflow.

## Notes on `.golangci.yml`

`.golangci.yml` is a **v2** config (`version: "2"`), so the v1
`issues.exclude-rules` block no longer exists: per-path exclusions are
`linters.exclusions.rules` entries. Verify it with
`golangci-lint config verify`, which is also the first step of the `lint` job.

- `forbidigo` bans `fmt.Print*` and `os.Exit` everywhere except
  `internal/output/` (which renders text), `cmd/` or any other `cmd/` directory
  (entry points decide the exit code), and `internal/testing/`. That last one is
  a deliberate, narrow deviation from PLAN.md's literal "outside
  `internal/output` and `cmd/`": this repo's dev tools are main packages at
  `internal/testing/<tool>/main.go` (`coveragecheck`, `gencontract`, `docgen`),
  they print to stdout by design, and PLAN.md already puts `internal/testing/...`
  outside the coverage scope as fakes and dev tooling.
- `depguard` has three rules: no `github.com/microsoftgraph/msgraph-sdk-go`, no
  `github.com/dnaeon/go-vcr` (v1 — use `gopkg.in/dnaeon/go-vcr.v4`), and no
  `internal/testing/...` imports from non-test code. The third one is scoped
  with `files: ["!$test", "!**/internal/testing/**"]`, because the rule is about
  *production* code importing test tooling, and the packages inside
  `internal/testing` legitimately import each other.

## Follow-ups

Both wrappers the workflows call now exist, so `refs-check` and `make smoke` run:

- **`scripts/gen-contract.sh`** regenerates the trimmed OpenAPI subset and the
  api-reference route list on top of `internal/testing/contract/cmd/gencontract`;
  `--verify` fails when a re-run would change the committed inputs.
- **`scripts/smoke.sh`** is the local half of the smoke job: it runs a built
  binary in an isolated config/state/cache directory, checks `version`, the help
  surface, exit code 3 from `auth status`, exit code 2 for a usage error, the
  config round trip, `cache info|clear` (which must keep token material) and the
  absence of the test-only endpoint markers that the CI job also greps for.
  Keep the marker list in `scripts/smoke.sh` and in `ci.yml` in step.

Open items that still need a human:

- **No `make release` target.** `release.yml` therefore invokes
  `goreleaser release --clean` directly. If a target is added, the workflow
  should call it.
- **No contract-only test target.** The nightly spec-drift job runs the whole
  suite through `make test` because the Makefile has no narrower hook; a
  `make contract` target would make that job cheaper and its failures more
  precise.
- **`make vuln` is unpinned** (`go run golang.org/x/vuln/cmd/govulncheck@latest`),
  so the scanner version can drift between runs.
- **The first release needs a maintainer**: `git tag -s v0.1.0-rc.1 && git push
  --tags` triggers `release.yml`. Nothing has been pushed from this work.
