# teams

`teams` is a command-line client for Microsoft Teams. It reads everything you can
see in Teams — your teams and channels, threads, chats, search results, mentions,
unread messages, people, files — and it writes: post, reply, edit, delete, react,
attach files, create chats, change members and move your read state.

It is one static binary with no runtime to install. Reading and writing both work
from a script, a cron job or an AI agent, and the same binary can sign in as a
company **service account** instead of as you.

```console
$ teams channel read Engineering/General --limit 2
Bob Builder · 2 hours ago · #General
  Morning all, the deploy notes are attached.
  attachment: deploy-notes.md

Alice Example · 2 hours ago · #General · edited · high importance
  Subject: Deploy
  Hello @Yuki Tanaka, can you review the deploy?
  reactions: 👍 2

$ teams post Engineering/General 'shipping v0.4 now :rocket:' --mention @yuki
posted 1750000000003
  https://teams.microsoft.com/l/message/19:general@thread.tacv2/1750000000003?…
```

## Install

**Download a release** (macOS, Linux and Windows; `amd64` and `arm64`):

```bash
gh release download --repo floriscornel/teams-cli --pattern 'teams_*_darwin_arm64.tar.gz'
tar xzf teams_*_darwin_arm64.tar.gz teams   # the archive also carries the man pages
install teams ~/.local/bin/teams
```

**Or build it yourself** (needs Go 1.27+):

```bash
go install github.com/floriscornel/teams-cli/cmd/teams@latest
```

The archives also carry the man pages (`man teams`) and shell completions for
bash, zsh, fish and PowerShell (`completions/`). Without an archive,
`teams completion bash` prints the same script, and `teams version --check` says
whether a newer release exists.

Every release ships `checksums.txt`, signed with cosign keyless and covered by a
GitHub build-provenance attestation:

```bash
gh attestation verify --owner floriscornel teams_*_darwin_arm64.tar.gz \
  --repository floriscornel/teams-cli \
  --signer-workflow floriscornel/teams-cli/.github/workflows/release.yml
```

## Sign in

```bash
teams auth login                 # opens your browser (authorization code + PKCE)
teams auth login --device        # a device code, for SSH and headless machines
teams auth status                # who you are, what the token can do, where it is stored
teams doctor                     # check the config, token store, scopes and Graph reachability
```

Sign-in works with the Microsoft Graph CLI Tools app by default. Companies often
block it, so the recommended setup is your own Entra app registration — see
[Register your own Entra app](docs/guides/app-registration.md) for the five-minute
walkthrough (redirect URI, public client flows, the permission set per preset and
the admin consent). `teams auth status --admin-request` prints the exact text to
paste into a ticket.

The token cache is encrypted and stays local; see
[Where your data lives](#where-your-data-lives).

## Reading

| Command | What it shows |
|---|---|
| `teams team list` · `teams team show <team>` | Your teams, and one team's details |
| `teams channel list <team>` · `teams channel show <channel>` | A team's channels, and one channel |
| `teams channel read <channel> [--limit --replies --since --until --all]` | A channel's messages, threads included with `--replies` |
| `teams thread read <message>` | One message and its replies |
| `teams chat list [--with <person> --topic <q> --unread --all]` | Your chats, newest activity first |
| `teams chat show <chat>` · `teams chat read <chat> [--from --since --until --limit]` | One chat, and its messages |
| `teams search <query> [--from --to --in --mentions-me --has-attachment --since --until --limit --page]` | KQL search over messages |
| `teams mentions [--since 24h]` | Messages that mention you |
| `teams unread [--chats --mentions] [--since]` | What is waiting for you |
| `teams user search <q>` · `teams user show <person>` | People in your directory |
| `teams channel files <channel>` · `teams file download <message> [-o dir]` | A channel's files, and a message's attachments |
| `teams calendar list [--user <who> --date <day> --days N --from/--to --tz --free-busy --chat --include-cancelled]` | Meetings for a day or a range, yours or a colleague's |
| `teams calendar show <event>` · `teams calendar search <query>` | One event in full, and a search over your primary calendar |
| `teams alias set\|list\|rm` | Names you can use anywhere a reference is |

`teams search` takes the documented KQL scope terms both as flags and inline in
the query, so `teams search 'from:bob deploy sent>=2026-10-01'` works. Hits carry
no message body — Graph does not return one — so pipe the `webUrl` into
`teams thread read` when you want the text.

```bash
teams calendar list                                # today
teams calendar list --date tomorrow --tz Asia/Tokyo
teams calendar list --from mon --to fri            # weekdays read naturally
teams calendar list --user bob@example.com          # full details if shared
teams calendar list --user bob@example.com --free-busy
teams calendar list --chat                         # resolve each meeting's chat
teams calendar show a1b2c3d                        # a handle from the listing
teams calendar search standup
```

Days are **local**: a day runs midnight to midnight in `--tz`, or in this
machine's zone without it. All-day events are floating, so they are matched by
date and never shifted by a timezone conversion. The `ID` column is a 7-character
handle for Graph's 152-character event id; `show` accepts the handle, the full id,
or an Outlook web link. A colleague's calendar is shown in full when it is shared
with you; otherwise the command falls back to free/busy (times and status only)
and says so on stderr.

## Writing

| Command | What it does |
|---|---|
| `teams post <channel\|chat> [text\|-]` | A new message |
| `teams reply <message> [text\|-]` | A thread reply in a channel; a quote reply in a chat |
| `teams edit <message> [text\|-] [--subject --importance]` | Change a message you sent |
| `teams delete <message>` | Soft-delete it, as Teams does (the conversation keeps a tombstone) |
| `teams react <message> <emoji> [--remove]` | Add or remove your reaction |
| `teams chat create --with <person> [--with …] [--topic <title>]` | A one-on-one or group chat |
| `teams chat add-member <chat> <person…>` · `teams chat delete <chat>` | Membership, and deleting a chat |
| `teams chat mark-read\|mark-unread <chat>` | Move your own read watermark |
| `teams api <method> <path>` | Any Graph call the CLI does not wrap |

```bash
# markdown is the default; --text sends it verbatim and --html sends HTML
teams post Engineering/General '**deploy done** — notes attached' --subject Deploy

# a file goes to the channel's SharePoint folder; an image is embedded inline
teams post Engineering/General 'the chart:' --file chart.png --file notes.md

# stdin, so another command can write the message
git log --oneline -5 | teams post Engineering/General -

# see exactly what would be sent, then send it
teams post Engineering/General 'hi' --dry-run
teams reply Engineering/General/m-1002 'on it' --dry-run
```

Anything that writes supports `--dry-run`, which prints the Graph request it
would send instead of sending it — including the uploads a message with `--file`
would perform.

## Writing to your calendar

```bash
teams calendar create --subject Standup --start 09:00 --duration 15m --attendee bob@example.com
teams calendar create --subject Offsite --all-day --date 2026-10-12 --days 2
teams calendar create --subject "Design review" --start 14:00 --teams
teams calendar accept a1b2c3d --comment "see you there"
teams calendar tentative a1b2c3d --propose "2026-10-07 15:00/2026-10-07 15:30"
teams calendar update a1b2c3d --subject "Moved review" --start 15:00 --duration 45m
teams calendar cancel a1b2c3d --comment "rescheduling"
teams calendar delete a1b2c3d --yes
```

**These commands notify people.** Creating with `--attendee` sends the
invitations immediately, so on a terminal the command asks first and anywhere
else it needs `--yes`. Accepting, tentatively accepting and declining tell the
organizer; cancelling a meeting — or deleting one you organize — sends your
attendees a cancellation. `--dry-run` prints the request instead of sending it
(and still runs the read-only checks), and `--no-notify` answers without telling
the organizer.

The CLI refuses what Graph would reject, before sending anything:

- responding to a meeting **you** organize (`you are the organizer…`);
- cancelling one you do not organize (use `teams calendar decline`);
- updating one you do not organize, unless `--local-copy` — otherwise you change
  only your copy and the organizer's next update overwrites it;
- `--propose` together with `--no-notify` (a proposed time has to reach the
  organizer), or on an event whose organizer does not accept proposals;
- `create --teams` on a mailbox that cannot make Teams meetings.

`update` sends **only the fields you name**, so moving a meeting cannot drop its
body or its Teams link. `--body` rewrites the body and can lose the Teams meeting
details, which is why it is never sent unless you ask for it.

## Mentions, files and images

`--mention` marks someone as a real Teams mention, so they get notified. The text
must name them the way you want it shown; the CLI resolves the person and matches
`@alice`, `@"Alice Example"` or the address you typed:

```bash
teams post Engineering/General 'thanks @alice, please review' --mention alice@example.com
teams post Engineering/General 'cc @"Yuki Tanaka"' --mention yuki@example.com
```

`--file` attaches a file and references it in the message. An image that fits the
4 MB inline limit is embedded in the message body; anything else is uploaded (the
channel's SharePoint folder, or your own OneDrive for a chat, with a sharing link
so recipients can open it).

## References: links, names, @people and aliases

Every `<team>`, `<channel>`, `<chat>`, `<message>` and `<person>` argument accepts
any of these:

```bash
teams channel read Engineering/General                    # Team/Channel
teams thread read Engineering/General/1750000000001         # a message in that channel
teams thread read 'https://teams.microsoft.com/l/message/19:general@thread.tacv2/1750000000001?groupId=…'
teams chat read @yuki                                      # your 1:1 chat with a person
teams chat read yuki@example.com                           # the same person, by address
teams chat read 'Release train'                            # a chat topic
teams chat read 19:bob@thread.v2                           # a raw id
teams chat read @boss                                      # an alias you defined
```

Names are matched case-insensitively and remembered per profile in a small cache
(an hour for names, a week for people), so the second command is instant.
`--refresh` resolves them again. When a name is ambiguous on a terminal, the CLI
asks which one you meant; in a script it prints the candidates and exits 2.

```bash
teams alias set boss alice@example.com
teams alias set standup Engineering/Daily
teams alias list
```

## Output and scripting

On a terminal you get tables and rendered markdown. When the output is piped you
get plain text, and `--json` gives a stable schema designed for scripts;
`--jq <expr>` filters it (and implies `--json`).

```bash
teams chat list --unread --json | jq -r '.[].topic'
teams search deploy --json --jq '.hits[0].webUrl'
teams channel read Engineering/General --json --jq '.[0].body.content'
teams unread --json --jq '{chats: (.chats | length), mentions: (.mentions | length)}'
```

Exit codes are the same everywhere, so a script can tell failures apart:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | error |
| 2 | usage — a bad flag, an ambiguous name, a missing confirmation |
| 3 | authentication required — no session, an expired token, a missing scope |
| 4 | not found |
| 5 | throttled by Graph (the CLI retries first) |

**Non-interactive runs never prompt.** With `--no-input`, in CI, or when stdin is
not a terminal, anything that needs an answer fails fast with a hint instead —
which is why destructive commands take `--yes`.

**Read-only mode** refuses every write before any network call:

```bash
teams --read-only channel read Engineering/General   # fine
teams --read-only post Engineering/General hi        # exit 2, "read-only"
TEAMS_READ_ONLY=1 teams react <message> 👍           # the same, from the environment
```

## Inbox and reminders

```bash
teams unread                       # unread chats and unread mentions, last 24h
teams unread --since 7d            # a wider window
teams unread --all                 # every unread chat, however old
teams mentions --since 2h          # just the mentions
teams chat mark-read 'Release train'
```

`teams unread` never marks anything read; only `teams chat mark-read` does.

## Configuration

The config file is at `~/.config/teams/config.toml` on Linux,
`~/Library/Application Support/teams/config.toml` on macOS and
`%AppData%\teams\config.toml` on Windows; `TEAMS_CONFIG` overrides the path.
Profiles let you keep a personal identity and a service account side by side:

```toml
default_profile = "me"

[profiles.me]
tenant = "contoso.com"        # or "common"
client_id = ""                # empty → the Microsoft Graph CLI Tools public client
mode = "full"                 # full | read-only (read-only refuses write commands)
cloud = "global"              # global | usgov | china
token_store = "auto"          # auto | file | file:///path/to/cache
scopes = "full"               # chats | read-only | full, or an explicit list

[profiles.bot]
tenant = "contoso.com"
client_id = "00000000-0000-0000-0000-000000000000"
scopes = "full"
```

```bash
teams profile list
teams config set profiles.bot.client_id 00000000-0000-0000-0000-000000000000
teams --profile bot chat list        # or TEAMS_PROFILE=bot, or default_profile
```

Environment overrides: `TEAMS_PROFILE`, `TEAMS_READ_ONLY`, `TEAMS_CONFIG`,
`TEAMS_CACHE_DIR`, `TEAMS_STATE_DIR`, `TEAMS_ACCESS_TOKEN` (a short-lived Graph
token used as-is, for CI) and `TEAMS_NO_KEYCHAIN` (use the plaintext token file,
for containers).

### Service accounts

A service account signs in the same way — once, with `--device`:

```bash
teams auth login --profile bot --device     # signed in as the bot user
teams --profile bot post Engineering/Deploys 'nightly build finished'
```

The token cache lives in the same place as your own, so the account is reusable
on that machine. Storing it in Azure Key Vault, so an unattended runner can read
it, is on the roadmap (see [Status](#status)).

### Scopes and admin consent

The `scopes` setting is a **preset for the app registration**, and it decides what
the CLI can ask for:

| Preset | Includes |
|---|---|
| `chats` | Your chats: `Chat.Read`, `Chat.ReadWrite`, `ChatMessage.Send`, files in your OneDrive |
| `read-only` | Reading, plus `ChannelMessage.Read.All` and `Files.Read.All` for channel content |
| `full` | Everything: channel and chat writes, and channel file uploads (`Files.ReadWrite.All`) |

`ChannelMessage.Read.All`, `ChannelMessage.ReadWrite` and `TeamMember.Read.All`
need **admin consent**, so reading a channel usually needs an admin even though
writing a chat does not. A command whose scope the token does not carry fails
immediately, names the scope, and exits 3 — it does not call Graph first.
`teams doctor` shows which commands the current token unlocks, and
`teams auth status --admin-request` prints the consent request.

`teams chat delete` needs `Chat.ManageDeletion.All`, which is in no preset
because an admin has to consent to it. On a terminal the CLI offers to sign in
again with that scope; anywhere else it exits 3 with the admin request.

The **calendar** commands work the same way, because a calendar scope in a preset
would make every existing login ask for it (which triggers `AADSTS65001`):

| Command | Scope |
|---|---|
| `teams calendar list` (your own), `show`, `search` | `Calendars.Read` |
| `teams calendar list --user <colleague>` | `Calendars.Read.Shared` |
| `teams calendar list --chat`, `show` (chat lookup) | `OnlineMeetings.Read` |

None of them needs admin consent by the docs, but a production tenant may still
block user consent, in which case the error's hint says so and
`teams auth status --admin-request` prints the ticket. Listing a colleague by
**address** needs nothing beyond the calendar scopes; naming them by display name
additionally resolves through the directory, which needs `User.ReadBasic.All` or
`People.Read`.

## Where your data lives

| Kind | Where | Removed by |
|---|---|---|
| Config | `os.UserConfigDir()/teams/config.toml` | `teams config` |
| Secrets | your OS keychain + `token.bin` in the state dir | `teams auth logout` — never `teams cache clear` |
| Cache (rebuildable) | `os.UserCacheDir()/teams/<profile>/` | `teams cache clear` |
| State | `~/.local/state/teams/<profile>/` on Linux, beside the config elsewhere | `teams cache clear --all` |

Files are `0600` and directories `0700`. The token cache is encrypted with
AES-256-GCM, and only the 32-byte data key sits in your keychain — created the
first time tokens are stored, so a fresh `teams auth status` never touches it.

```bash
teams cache info            # every path, size and entry count, no network calls
teams cache clear           # the rebuildable cache
teams cache clear --all -y  # everything except the config and your tokens
```

## Teams API escape hatch

Anything the CLI does not wrap is still reachable, with the same authentication:

```bash
teams api GET /me
teams api GET /teams/$TEAM_ID/channels --jq '.value[].displayName'
teams api POST /chats/19:bob@thread.v2/markChatReadForUser -f user.id=user-1
teams api POST /search/query --input query.json
```

`-f key=value` adds a string field (dotted keys nest, so `-f body.content=hi`
works), `-F key=value` keeps JSON types, `--input <file>` sends a whole body,
`--query` and `--header` add parameters and headers, and `--dry-run` prints the
request. Read-only mode still applies: a raw `GET` or `HEAD` is allowed, anything
else is refused.

## Troubleshooting

| Symptom | What to do |
|---|---|
| `exit 3`, "not signed in" | `teams auth login`, or `--device` on a headless machine |
| `exit 3`, "needs `ChannelMessage.Read.All`" | The token lacks the scope: `teams doctor` shows the gap, `teams auth status --admin-request` prints the ticket for an admin |
| Graph answers 403 for a call | Usually consent, occasionally a tenant policy on the app registration; the error names the scope Graph wanted |
| `AADSTS50011` on sign-in | The app registration has no `http://localhost` redirect URI; `teams auth login --device` works meanwhile |
| `AADSTS50020` / `AADSTS65001` | The tenant or client ID is wrong / the user or an admin has not consented: check `tenant` and `client_id` in the profile |
| A command finds no chat for `@person` | There is no existing 1:1 chat; any write (`teams post @person hi`) creates it, or use `teams chat create --with <person>` |
| A name matches several objects | Run it on a terminal and pick one, or use the raw id from `teams user search` |
| Calls hang or time out | `-v` logs every request, page and retry; a stalled call reports a timeout rather than hanging |
| Nothing works behind a proxy | Set `HTTPS_PROXY`; the CLI uses the standard Go proxy variables |

## Security and privacy

- **Credentials never touch the command line.** There is no flag that takes a
  secret; the token cache is encrypted at rest, and no command prints a token.
- **The token cache is the only secret on disk.** `teams cache clear` can never
  delete it — `teams auth logout` is the command that forgets you.
- **`TEAMS_ACCESS_TOKEN` is a decode-only escape hatch.** It is checked for
  audience and expiry to catch mistakes, not verified as a signature, and it
  skips the scope pre-check.
- **`teams api` sends whatever you ask.** It is the escape hatch, so it can write
  anything the token allows. `--dry-run` shows the request first.
- **AI features are opt-in and not built yet.** When they land, nothing is sent to
  a model provider until you run `teams ai setup` and consent, and Teams content
  is only sent to the provider you configure.

## Status

**`v1.0.0` has shipped**, and this branch adds the calendar reads (`teams calendar
list`, `show`, `search`) on top of it; the calendar writes (`create`, `update`,
`accept`, `tentative`, `decline`, `cancel`, `delete`) land next. The binary is what
this README describes, and this documentation is also published at
**<https://floriscornel.github.io/teams-cli/>** (generated from the repository).

In order from here:

1. **Calendar writes** — `create`, `update`, the four responses, `cancel` and
   `delete`, behind `Calendars.ReadWrite`. They notify attendees, so they ask for
   confirmation and support `--dry-run`.
2. **AI features** — `summarize`, `ask`, `draft`, `catchup` with an Anthropic,
   OpenAI-compatible or Azure/Foundry provider, conversation history and curated
   memory. Opt-in, off by default.
3. **Service accounts** — the Key Vault token store, `auth export`/`auth refresh`
   and a CI guide, for unattended runners.

[PLAN.md](PLAN.md) is the full design and the reasoning behind each decision.

## Contributing

Bug reports and pull requests are welcome at
[floriscornel/teams-cli](https://github.com/floriscornel/teams-cli). The short
version for a checkout:

```bash
mise trust && mise install
mise run check        # fmt, tidy, lint, tests with coverage, contract checks
mise run test         # go test -race -shuffle=on ./...
mise run docs         # regenerate docs/commands, the man pages and completions
mise run docs-site    # render the documentation site into dist/docs-site
```

[AGENTS.md](AGENTS.md) explains the layout and the rules (in particular: read the
reference mirror under `refs/` before changing any Graph call, OAuth flow, Teams
URL or KQL query), [docs/ci.md](docs/ci.md) covers CI and releases, and
[SECURITY.md](SECURITY.md) says how to report a vulnerability privately.

## License

See [LICENSE](LICENSE) (added with the first release).
