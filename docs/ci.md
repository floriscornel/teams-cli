# CI, releases and the dev loop

This page describes the automation that lives in `.github/workflows/`, what it
runs locally, and — importantly — which repository secrets it needs. The rules
come from PLAN.md ("CI/CD, releasing and dev experience" and "CI gates"); the
GoReleaser keys are cited against the mirrored docs in
[`refs/goreleaser/www/content/`](../refs/INDEX.md).

## The one rule

**`mise.toml` is the single entry point.** Every CI step runs a `mise run`
task, so a green local run means a green CI run:

| Task | Command | Used by |
|---|---|---|
| `mise run lint` | `golangci-lint config verify` + `golangci-lint run ./cmd/... ./internal/...` | `ci.yml` → lint |
| `mise run fmt-check` | `golangci-lint fmt --diff ./cmd/... ./internal/...` | `ci.yml` → lint |
| `mise run tidy` | `go mod tidy`, then fails if `go.mod`/`go.sum` changed | `ci.yml` → lint |
| `mise run release-check` | `goreleaser check` | `ci.yml` → lint |
| `mise run test` | `go test -race -shuffle=on ./...` | `ci.yml` → test |
| `mise run cover` | the suite once with a coverage profile, then the 80% floor | `ci.yml` → coverage |
| `mise run contract` | `go test -shuffle=on ./internal/testing/contract/` (no race detector) | `nightly.yml` → spec-drift |
| `mise run vuln` | `govulncheck ./...` | `ci.yml` → vuln |
| `mise run snapshot` | `goreleaser build --snapshot --clean` | `ci.yml` → build |
| `mise run release` | `goreleaser release --clean` | `release.yml` |
| `mise run docs-check` | regenerate `docs/commands` + `docs/man`, fail on a diff in those two trees | `ci.yml` → docs |
| `mise run refs-fetch` | `scripts/fetch-refs.sh` | `ci.yml` → refs-check |
| `mise run refs-check` | `scripts/fetch-refs.sh --verify` | `ci.yml` → refs-check |
| `mise run contract-generate` | `scripts/gen-contract.sh` | `ci.yml` → refs-check |
| `mise run fuzz-short` | every `Fuzz*` target with `-fuzztime ${FUZZTIME}` | `nightly.yml` → fuzz |

`mise run` without arguments lists every task with its description.

Two things are deliberately *not* tasks, and both are marked in the workflow:

- the `smoke` job runs plain shell scripts. It must not install mise, because
  `mise install` provisions the Go toolchain from `mise.toml`, and the point of
  that job is that a released artifact runs with **no Go and no Node** behind
  it;
- `release.yml` needs no exception any more: adding the missing `release` task
  was one of the open items this layout closed.

## Workflows

### `ci.yml` — pull requests and pushes to `main`

`permissions: contents: read`, `concurrency` cancels superseded PR runs, and
every third-party action is pinned by 40-hex commit SHA with the release tag in
a trailing comment.

Every job starts with the same two steps: `jdx/mise-action` (which installs the
pinned mise binary, runs `mise install`, and exports
`MISE_TRUSTED_CONFIG_PATHS` so the tasks in `mise.toml` run without a trust
prompt), then `actions/cache` for the Go module and build caches. The jobs whose
task installs a tool on demand (`lint`, `build`, `vuln`, `release`) set
`cache_save_post: true` on the mise-action step, so those tools end up in mise's
cache instead of being downloaded again on every run.

`docs-check` compares only the generated trees (`docs/commands`, `docs/man`), so
an uncommitted prose edit under `docs/` does not fail it.

**Why `actions/cache` and not `actions/setup-go`:** `mise.toml` keeps `GOPATH`,
`GOMODCACHE` and `GOCACHE` inside the checkout (`.cache/`), so a sandboxed
runner that cannot write the global Go cache works unchanged. `setup-go` caches
the *global* paths instead, which no task uses — with the old Makefile that
cache never matched either. The explicit step caches the paths the tasks really
write, keyed on `go.sum`.

| Job | What it does |
|---|---|
| `lint` | `mise run fmt-check`, `mise run lint`, `mise run tidy`, `mise run release-check` |
| `test` | matrix `ubuntu-latest` / `macos-latest` / `windows-latest`, `mise run test`. Layer 5 `testscript` tests are ordinary Go tests driven by `testscript.Main`, so the same command runs them everywhere — including Windows, where mise executes the task through `cmd /c` (the Makefile could not: the Windows runner image ships no `make`, so that row used to run `go test` by hand) |
| `coverage` | `mise run cover` (one run, one merged `coverage.out`, 80% floor through `internal/testing/coveragecheck`), then uploads `coverage.out` to Codecov |
| `vuln` | `mise run vuln` (govulncheck is pinned in `mise.toml`) |
| `build` | `mise run snapshot` (the full darwin/linux/windows × amd64/arm64 matrix), uploads `dist/` as the `dist` artifact |
| `smoke` | downloads `dist/`, installs the binary **without** mise, `setup-go` or `setup-node`, runs `teams version`, asserts `teams auth status` exits 3, and asserts no test-only endpoint flag is baked into the binary |
| `docs` | `mise run docs-check` |
| `refs-changed` | computes whether the mirror inputs changed (see below) |
| `refs-check` | `mise run refs-fetch`, `mise run refs-check`, `mise run contract-generate`, then fails on a diff |
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
A maintainer pushes a signed tag; the workflow installs mise and runs
`mise run release`, which is `goreleaser release --clean`. GoReleaser then:

1. builds darwin/linux/windows × amd64/arm64 with `CGO_ENABLED=0`,
   `-trimpath` and a commit timestamp, so the same commit rebuilds
   byte-identically;
2. writes tar.gz archives (zip on Windows) with the binary, `LICENSE`/`README`/
   `CHANGELOG` when they exist, and the generated man pages under `man/`;
3. writes `checksums.txt`;
4. writes one SBOM per archive with syft;
5. signs `dist/checksums.txt` with cosign keyless into
   `dist/checksums.txt.sigstore.json`
   ([`blog/cosign-v3.md`](../refs/goreleaser/www/content/blog/cosign-v3.md));
6. creates the GitHub Release and attaches all of it.

The workflow then attests `dist/checksums.txt` with
`actions/attest-build-provenance`, so users can run
`gh attestation verify --owner floriscornel <artifact>`
([`publish/attestations.md`](../refs/goreleaser/www/content/customization/publish/attestations.md)),
and keeps a 90-day copy of the SBOMs, the checksums and the signature bundle as
a workflow artifact.

**GitHub Releases is the only install channel.** GoReleaser here has no
`homebrew_casks`, no `scoops`, no `nfpms` and no `winget`: those publish to a
second repository (a tap, a bucket, `microsoft/winget-pkgs`) or need a package
review, and PLAN.md defers them past v1.0. Consequences worth knowing:

- the release workflow no longer mints a **GitHub App installation token** and
  no longer needs the `RELEASE_APP_ID` / `RELEASE_APP_PRIVATE_KEY` secrets: the
  automatic `GITHUB_TOKEN` writes the release in this repository and nothing
  else;
- cosign and syft are not installed by pinned actions any more. They are pinned
  in `mise.toml` as task tools of `mise run release`, together with GoReleaser,
  so one file versions all three and `mise run release` works locally too;
- users install with `go install github.com/floriscornel/teams-cli/cmd/teams@…`
  or by unpacking an archive (the release body says so, see `release.header` in
  `.goreleaser.yaml`);
- during v0.x, tags with a prerelease indicator (`v0.2.0-rc.1`) become GitHub
  pre-releases through `release.prerelease: auto`.

### `nightly.yml` — cron `17 4 * * *`

| Job | What it does |
|---|---|
| `fuzz` | `FUZZTIME=10m mise run fuzz-short` (the task reads `FUZZTIME`; local default is 15s) |
| `spec-drift` | `mise run contract` — the Layer 6 package without the race detector. The job **never** fetches `refs/` and never resolves a branch tip: the trimmed OpenAPI subset and the api-reference route list are committed and pinned, so a failure means our toolchain or a dependency moved, not Microsoft. The refs/-dependent generator tests skip here (they need the mirror, and `refs-check` is the job that has it). On failure it files (at most one) GitHub issue |
| `refs-check` | `uses: ./.github/workflows/ci.yml` with `refs-check-only: true` |

PLAN.md describes the spec-drift job as weekly; running it nightly is a superset.
Running the contract package instead of the whole suite (which the Makefile
forced, because it had no narrower hook) is what makes that cheap and its
failures precise.

## Secrets

Nothing is invented here: every workflow reads these with `${{ secrets.NAME }}`,
and each one must be created in **Settings → Secrets and variables → Actions**.

| Secret | Workflow | Required? | What it is |
|---|---|---|---|
| `CODECOV_TOKEN` | `ci.yml` (coverage) | No | Codecov upload token. The upload step uses `fail_ci_if_error: false`, so a missing token warns instead of failing the build. Add the repository to Codecov and store the token to get PR annotations |

That is the whole list. The GitHub App that used to be needed for the
`homebrew-tap` and `scoop-bucket` pushes is gone (see "GitHub Releases is the
only install channel"), and the Apple notarization secrets were never used in
v0.x. `nightly.yml` uses the built-in `secrets.GITHUB_TOKEN` with
`issues: write` for the drift issue; that is the automatic per-run token, not a
stored secret.

## Pinned versions and how to bump them

Three layers:

1. **Tools** — Go, golangci-lint, GoReleaser, cosign, syft and govulncheck are
   pinned in `mise.toml`: the two that every job needs under `[tools]`, the
   release-time and scan-time ones on the task that uses them. Renovate's
   `mise` manager bumps them (grouped as "mise tools"). `go` is the exception:
   it must stay equal to the `go` directive in `go.mod`, and a Renovate rule
   disables that specific bump so it stays a reviewed change.
2. **The mise binary** — pinned as `MISE_VERSION` in the `env:` block of
   `ci.yml`, `release.yml` and `nightly.yml`, with a
   `# renovate: datasource=github-releases depName=jdx/mise` comment that the
   custom manager in `renovate.json` reads. The `jdx/mise-action` step is itself
   pinned by commit SHA.
3. **The reference mirror** — pinned in `refs/MANIFEST.md` and bumped only with
   `scripts/fetch-refs.sh --update`, reviewed like any other dependency.

Renovate groups Go modules, GitHub Actions, mise tools, and the CI tool versions
into separate weekly PRs (`schedule: before 6am on monday`), keeps a dependency
dashboard issue, and lets security updates through at any time. Its commit
prefix is `chore(deps)`, which `.goreleaser.yaml` filters out of the changelog.

## Local equivalents

```sh
mise install               # Go + golangci-lint (and mise itself)
mise trust                 # once per clone: tasks in mise.toml execute code
mise run                   # list every task
mise run check             # fmt-check + tidy + lint + cover + contract (everything CI gates on)
mise run test              # the suite with the race detector
mise run test-short        # the fast loop, no race detector
mise run cover             # the suite once with coverage + the 80% floor
mise run contract          # Layer 6 (no race detector; see "Test speed" below)
mise run snapshot          # the same cross-compile matrix the build job runs
mise run docs-check        # regenerate docs and fail on drift, like the docs job
mise run refs-check        # verify the mirror, like the refs-check job
FUZZTIME=10m mise run fuzz-short   # what the nightly fuzz job runs
mise run vuln release-check
```

`mise run smoke-live` and `mise run record` are maintainer-only and gated on
`TEAMS_E2E=1`; they are deliberately absent from every workflow.

## Test speed (why the suite is shaped the way it is)

`mise run check` used to take about five minutes on a developer machine with the
`refs/` mirror present. Three things were wrong; each is now pinned down by a
comment in `mise.toml` or in the test that does it:

1. **The suite ran twice.** `make check` ran `make test` and then `make cover`,
   and each one executed the whole suite (only `-shuffle=on` differs, and a
   random seed defeats Go's test cache, so both really ran). `mise run cover` is
   now the single run: the profile it writes serves both the test gate and the
   coverage floor. `mise run test` remains for the CI matrix, which has its own
   job.
2. **The contract package reloaded the spec per test.** `contract.Load` parsed
   and schema-compiled the 1 MB trimmed description every time a test called it:
   ~0.4 s normally and ~4 s under `-race`, thirteen times per run. It now loads
   once per process (`sync.OnceValues` in
   `internal/testing/contract/validator.go`), which is safe because a
   `Validator` is read-only after construction.
3. **The race detector was doing nothing but harm in three tests.** The
   generator and trim tests parse the whole upstream Graph description out of
   `refs/`, which the race detector slows down ~17x while it cannot observe
   anything in single-goroutine code: `TestGenerateIsDeterministic` alone took
   ~50 s under `-race` and ~3 s without. Those tests now skip in a race build
   (`mirrorHeavy` in `internal/testing/contract/contract_test.go`, using the
   `race` build constraint), and `mise run contract` runs them without it.
   `mise run check` runs both, so the gate is unchanged; CI's `test` and
   `coverage` jobs have no `refs/` mirror and skipped these tests already.

With all three, `mise run cover` is ~20 s and `mise run check` ~35 s on the same
machine — and the coverage floor still passes (85.4% at the time of writing).

One thing to keep in mind: `-shuffle=on` means the Go test cache never applies,
so a repeat of `mise run test` re-executes everything. That is deliberate (a
fixed order hides order-dependent failures), and `mise run test-short` is there
for the tight loop; it skips the race detector but still shuffles.

## Notes on `.golangci.yml`

`.golangci.yml` is a **v2** config (`version: "2"`), so the v1
`issues.exclude-rules` block no longer exists: per-path exclusions are
`linters.exclusions.rules` entries. Verify it with
`golangci-lint config verify`, which `mise run lint` now runs as its first step
(so CI and a local run check the same thing).

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

Resolved by this layout, and worth not re-raising:

- **`mise run release` exists**, so `release.yml` calls a task instead of
  invoking GoReleaser by hand;
- **`mise run contract` exists**, so the nightly drift job runs the Layer 6
  package rather than the whole suite;
- **`mise run vuln` is pinned** (govulncheck 1.8.0 in `mise.toml`) instead of
  `go run …@latest`.

Still open:

- **`mise run snapshot` needs the GoReleaser download on first use** (mise
  installs the task tool on demand). It is cached in mise's own data directory,
  and CI gets it through `jdx/mise-action`'s cache.
- **The first release needs a maintainer**: `git tag -s v0.1.0-rc.1 && git push
  --tags` triggers `release.yml`. Nothing has been pushed from this work.
- **Windows is a build target, not a test-only curiosity**: the archives keep
  `windows_amd64`/`windows_arm64` and the `test` matrix runs on Windows, but no
  install channel there is planned before v1.0.
