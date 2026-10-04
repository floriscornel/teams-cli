# teams

A single static binary for Microsoft Teams: search and read messages, channels,
threads and chats, and post, reply, edit, react and attach files — as yourself,
or as a company service account.

This repository is at **Phase 4 (Write)**: the CLI skeleton, config and profiles,
authentication, the local data layout, the Graph transport layer and the test
harness are in place; every read command works (teams, channels, chats, threads,
search, mentions, users, files and aliases) and so does every write — posting,
replying, editing, deleting, reacting, chat management and a raw `teams api`
escape hatch. The bot/headless work is Phase 5 and the AI features are Phase 6.
[PLAN.md](PLAN.md) is the source of truth for what exists and why.

## Install

Releases are published on **GitHub Releases only** — there is no Homebrew cask,
no Scoop manifest and no distribution package:

```bash
# build from source
go install github.com/floriscornel/teams-cli/cmd/teams@latest

# or download an archive for your platform
gh release download --repo floriscornel/teams-cli --pattern 'teams_*_darwin_arm64.tar.gz'
```

Every archive is covered by `checksums.txt`, which is signed with cosign keyless
and has a build-provenance attestation, so a download can be verified:

```bash
gh attestation verify --owner floriscornel teams_*_darwin_arm64.tar.gz \
  --repository floriscornel/teams-cli \
  --signer-workflow floriscornel/teams-cli/.github/workflows/release.yml
```

## Development

`mise.toml` is the single entry point for humans and CI: every CI step runs a
task from it, so "works locally" means the same thing as "works in CI".

```bash
git clone git@github.com:floriscornel/teams-cli.git
cd teams-cli
mise trust          # once per clone: tasks in mise.toml execute code
mise install        # the pinned Go toolchain and golangci-lint
mise run            # list every task
mise run build      # bin/teams
./bin/teams version
```

The tasks keep the Go caches inside the checkout (`.cache/`), so they also work
in a sandbox that cannot write the global Go cache. Override with
`GOCACHE=/tmp/gocache mise run test` if you prefer the global one.

```bash
mise run check        # fmt-check + tidy + lint + cover + contract
mise run test         # go test -race -shuffle=on ./...
mise run cover        # the suite once with coverage, then the 80% floor
mise run contract     # Layer 6 without the race detector (see docs/ci.md)
mise run docs         # regenerate docs/commands and docs/man
mise run smoke        # run the built binary through the release smoke checks
mise run snapshot     # cross-compile the release matrix locally
mise run refs-check   # verify the vendored reference mirror (needs refs-fetch)
```

## Commands

Reading (Phase 3):

| Command | What it does |
|---|---|
| `teams team list` / `teams team show <team>` | The teams you belong to, and one team's details |
| `teams channel list <team>` / `teams channel show <channel>` | A team's channels, and one channel |
| `teams channel read <channel> [--limit --all --since --until --replies]` | A channel's messages, with threads on request |
| `teams channel files <channel>` | The channel's SharePoint folder |
| `teams thread read <message>` | One message and its thread (a chat message has none) |
| `teams chat list [--with <user> --topic <q> --unread]` | Your chats, newest activity first |
| `teams chat show <chat>` | One chat with its members and read state |
| `teams chat read <chat> [--since --until --from --limit --all]` | A chat's messages |
| `teams search <query> [--from --to --in --since --until --mentions-me --has-attachment --limit --page]` | KQL search over Teams messages |
| `teams mentions [--since 24h]` | The messages that mention you |
| `teams unread [--chats --mentions]` | Unread chats and unread mentions |
| `teams user search <q>` / `teams user show <user>` | The directory, and one person |
| `teams file download <message> [-o dir] [--name --no-images --overwrite]` | Attachments and inline images |
| `teams alias set\|list\|rm` | Name a person, chat or channel once and reuse it |

Every `<team>`, `<channel>`, `<chat>`, `<message>` and `<user>` argument accepts a
Teams deep link, a `Team/Channel` name path, `@person`, an e-mail address, an
alias or a raw id. Names are matched case-insensitively, kept in a per-profile
entity cache (an hour for names, a week for people) and re-resolved with
`--refresh`.

Writing (Phase 4):

| Command | What it does |
|---|---|
| `teams post <channel\|chat> [text\|-]` | Post a message: markdown (default), `--text`, `--html`, `--mention`, `--file`, `--subject`, `--importance`, `--dry-run` |
| `teams reply <message> [text\|-]` | Reply in a channel thread, or quote-reply in a chat |
| `teams edit <message> [text\|-]` | Edit the body (and `--subject`) of a message you sent |
| `teams delete <message>` | Soft-delete a message, as Teams does |
| `teams react <message> <emoji> [--remove]` | Set or remove your reaction |
| `teams chat create --with <person> [--topic]` | A one-on-one or group chat (returns the existing one-on-one chat) |
| `teams chat add-member <chat> <person…>` | Add members |
| `teams chat delete <chat> [--yes]` | Delete a chat; the admin-consented scope is requested on demand |
| `teams chat mark-read\|mark-unread <chat>` | Move your own read watermark |
| `teams api <method> <path> [-f k=v \| -F k=v \| --input file]` | Raw Graph call, with `--query`, `--header` and `--dry-run` |

A message body is sanitized against the Teams allow-list and mentions are
inserted *after* the sanitizer runs — the other order silently drops every
`<at>` tag. `--file` uploads a file into the channel's SharePoint folder (or
your OneDrive for a chat) and references it as an attachment; an image up to the
documented 4 MB inline limit is embedded in the message instead. Every write
command supports `--dry-run`, which prints the Graph request it would send.

Account and local data (Phase 2):

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

Global flags: `--profile`, `--json`, `--jq <expr>`, `--refresh`, `--no-color`,
`--quiet`, `-v/--verbose`, `--no-input`, `--read-only`.

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

Tasks, versions and the dev loop live in [mise.toml](mise.toml); the list above
is the short version, and [docs/ci.md](docs/ci.md) covers CI, releases and why
the suite is shaped the way it is.

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
