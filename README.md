# teams

A single static binary for Microsoft Teams: search and read messages, channels,
threads and chats, and (from Phase 4) post, reply, edit and react — as yourself,
or as a company service account.

This repository is at **Phase 2 (Foundation)**: the CLI skeleton, config and
profiles, authentication, the local data layout, the Graph transport layer and
the test harness are in place. Read commands land in Phase 3, write commands in
Phase 4, the bot/headless work in Phase 5 and the AI features in Phase 6.
[PLAN.md](PLAN.md) is the source of truth for what exists and why.

## Install (development)

```bash
git clone git@github.com:floriscornel/teams-cli.git
cd teams-cli
make build          # bin/teams
./bin/teams version
```

`make` keeps the Go caches inside the checkout (`.cache/`), so it also works in a
sandbox that cannot write the global Go cache. Override with
`GOCACHE=/tmp/gocache make test` if you prefer the global one.

## Commands (Phase 2)

| Command | What it does |
|---|---|
| `teams auth login [--device] [--scopes …]` | Sign in with auth code + PKCE, or a device code |
| `teams auth status [--admin-request]` | Who you are, which scopes the token carries, where the token cache lives |
| `teams auth logout [--all]` | Remove the cached account and delete the token material |
| `teams whoami` | `GET /me` for the signed-in user |
| `teams doctor [--offline]` | Diagnose config, token store, keychain, scopes, clock skew and Graph reachability, and print the fix |
| `teams cache info` / `teams cache clear [--names --ai --all]` | Inspect and clear local data — never tokens |
| `teams profile list` / `teams profile use <name>` | Manage profiles |
| `teams config get\|set\|list` | Read and write the config file |
| `teams version` | Build information |

Global flags: `--profile`, `--json`, `--no-color`, `--quiet`, `-v/--verbose`,
`--no-input`, `--read-only`.

Exit codes: `0` ok, `1` error, `2` usage, `3` auth required, `4` not found,
`5` throttled.

## Configuration

The config file lives at `os.UserConfigDir()/teams/config.toml`
(`~/.config/teams/config.toml` on Linux, `~/Library/Application Support/teams/config.toml`
on macOS, `%AppData%\teams\config.toml` on Windows); `TEAMS_CONFIG` overrides it.

```toml
default_profile = "me"

[profiles.me]
tenant = "colorkrew.com"      # or "common"
client_id = ""                # empty → the Microsoft Graph CLI Tools public client
mode = "full"                 # full | read-only (read-only refuses write commands)
cloud = "global"              # global | usgov | china
token_store = "auto"          # auto | file | file:///path | keyvault://vault/secret
scopes = "full"               # chats | read-only | full, or an explicit list
```

Environment overrides: `TEAMS_PROFILE`, `TEAMS_READ_ONLY`, `TEAMS_CONFIG`,
`TEAMS_STATE_DIR`, `TEAMS_CACHE_DIR`, `TEAMS_ACCESS_TOKEN`,
`TEAMS_NO_UPDATE_CHECK`, and `TEAMS_NO_KEYCHAIN` (forces the plaintext token file
where no keychain exists, for example in a container).

### Scopes are presets for the app registration

| Preset | Scopes |
|---|---|
| `chats` | `User.Read User.ReadBasic.All People.Read Chat.ReadBasic Chat.Read Chat.ReadWrite ChatMessage.Send Files.ReadWrite` |
| `read-only` | `chats` minus the writes, plus `Team.ReadBasic.All Channel.ReadBasic.All ChannelMessage.Read.All TeamMember.Read.All Files.Read.All` |
| `full` | `read-only` plus `ChannelMessage.Send ChannelMessage.ReadWrite Chat.ReadWrite ChatMessage.Send Files.ReadWrite.All` |

`ChannelMessage.Read.All`, `ChannelMessage.ReadWrite` and `TeamMember.Read.All`
need **admin consent**, so even read-only channel access needs an admin in most
tenants. `teams auth status --admin-request` prints the exact changes to request.

### Where local data lives

| Kind | Location | Cleared by |
|---|---|---|
| Config | `os.UserConfigDir()/teams/config.toml` | `teams config` |
| Secrets | OS keychain + `<state>/<profile>/token.bin` | `teams auth logout` — **never** `teams cache clear` |
| Cache (rebuildable) | `os.UserCacheDir()/teams/<profile>/` | `teams cache clear` |
| State (user data) | `<state>/<profile>/` (Linux: `~/.local/state/teams`, macOS/Windows: beside the config) | `teams cache clear --ai` / `--all` |

Files are `0600` and directories `0700`. The token cache is encrypted with
AES-256-GCM; only the 32-byte data key lives in the OS keychain, and the key is
created **only when tokens are actually stored** (a first `teams auth status` never
touches your keychain).

## Authentication

`teams auth login` uses the authorization code + PKCE flow in your browser; on a
machine without a browser or a terminal it falls back to a device code
automatically, and it falls back on `AADSTS50011` (a redirect URI the app
registration does not allow) while telling you how to fix it permanently.

For headless bots, sign in once as the service account and store the cache where
the runner can read it (`keyvault://…` arrives in Phase 5). `TEAMS_ACCESS_TOKEN`
is an escape hatch for a short-lived Graph token: it is decoded (audience and
expiry) but **not** verified or scope-checked.

## Development

```bash
make check        # fmt-check + tidy + lint + test + coverage gate
make test         # go test -race -shuffle=on ./...
make cover        # coverage profile with the 80% floor on non-excluded code
make docs         # regenerate docs/commands and docs/man
make smoke        # run the built binary through the release smoke checks
make snapshot     # cross-compile the release matrix locally (needs goreleaser)
make refs-check   # verify the vendored reference mirror (needs it fetched)
```

The test pyramid is described in [PLAN.md](PLAN.md#automated-testing-strategy-no-real-graph-api-in-ci):

- `internal/format`, `internal/ref`, … — pure unit tests (Phase 3+);
- `internal/testing/fakegraph` — a stateful in-memory Graph;
- `internal/testing/fakeidp` — a TLS fake identity platform that real MSAL talks to;
- `internal/testing/contract` — a trimmed copy of Microsoft's Graph OpenAPI
  description plus the api-reference route list, both committed and checked in CI;
- `internal/cli/testdata/script/*.txtar` — `testscript` end-to-end runs of the real
  CLI against those fakes.

Tests never need `refs/` and never need the network.

Before changing any Graph call, OAuth flow, Teams URL or KQL query, read
[AGENTS.md](AGENTS.md): find the topic in [refs/INDEX.md](refs/INDEX.md), read the
referenced document, and cite its path in the PR.

## License

See [LICENSE](LICENSE) (added with the first release).
