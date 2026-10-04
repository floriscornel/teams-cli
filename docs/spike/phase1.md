# Phase 1 spike — findings (2026-10-04)

Live results against a corporate Microsoft 365 test tenant, using our own app registration ("Teams MCP Server")
and the scope set teams-mcp ran with. The throwaway code is in `spike/` (`go run ./spike <command>`; see
`spike/main.go` for the environment variables). The probes recorded status codes, counts and shapes only, never
message content. Where a result contradicts a mirrored doc, the doc path is cited.

**Status:** done, except for the items under [Still open](#still-open).

## Auth

| Probe | Result |
|---|---|
| Device code, own app | ✅ signed in within 27 s. A `.../device` page and a code; MSAL prints the message. |
| Interactive auth code + PKCE, own app | ❌ `AADSTS50011`: MSAL sends `http://localhost:<random port>`, and the app registers only `…/oauth2/nativeclient`. **Fix:** register `http://localhost` as a *Mobile and desktop* redirect. The port is ignored for localhost (`refs/entra/docs/identity-platform/reply-url.md`, "Localhost exceptions"). The app has no owners visible to us, so an admin has to make this change. |
| Access token `aud` | **GUID form** `00000003-0000-0000-c000-000000000000`, not `https://graph.microsoft.com`. PLAN.md had these the wrong way round for `TEAMS_ACCESS_TOKEN` validation. |
| Access token `scp` | Contains **every admin-consented scope**, whichever subset we asked for. `GrantedScopes` matches it. The feature matrix can therefore be computed straight from `scp`. |
| `cp1` client capability | `xms_cc=[cp1]` appears in the token. Access-token lifetime was **24 h**, with `RefreshOn` about 12 h later, which is consistent with CAE long-lived tokens. |
| Silent acquisition | Served from the cache (`TokenSource=1`). The store was **not rewritten** on a cache hit. |
| Forced refresh (`WithClaims`) | Redeemed the refresh token in 228 ms and **rewrote the store**, which confirms the write-back design. |
| MSAL cache size, one account | **8,236 bytes**. That is above go-keyring's macOS limit (about 3.9 KB) and its Windows limit (2.5 KB), so envelope encryption is required, not optional. |

### Consent in this tenant
- The app's admin grant (AllPrincipals) covers `Channel.ReadBasic.All ChannelMessage.Edit ChannelMessage.Read.All ChannelMessage.Send Chat.Create Chat.ReadWrite Team.ReadBasic.All TeamMember.Read.All User.Read`.
- A silent request for anything else fails with **AADSTS65001**. That covers `User.ReadBasic.All`, `People.Read`, `Files.Read.All`, `Files.ReadWrite.All`, `ChannelMessage.ReadWrite`, `ChatMessage.Send` and `Chat.ManageDeletion.All`.
- The user-consent policy is `microsoft-user-default-low`, and **no** delegated permissions are classified as low. Users can't self-consent to anything useful, so PLAN.md's `chats` tier ("no admin consent needed") doesn't help in that tenant: **every scope change needs an admin**.
- `ChannelMessage.Edit` is granted, but it **does not** allow editing or deleting channel messages. Graph answers 403 with "API requires one of 'ChannelMessage.ReadWrite, Group.ReadWrite.All'", which matches the docs. Don't rely on `ChannelMessage.Edit`.
- **Graph CLI Tools client** (`14d82eec-…`): its service principal is enabled and has no assignment requirement. The signed-in user holds a **per-user** grant that includes `User.ReadBasic.All` and the Teams scopes, so the default client works for that user. That proves nothing about other users. Not tested with a sign-in.

### Admin request (one ticket)
1. Add `http://localhost` (Mobile and desktop) to the app's redirect URIs.
2. Admin-consent `User.ReadBasic.All` (user search and mention resolution) and `ChannelMessage.ReadWrite` (channel edit and delete). Optionally add `Files.Read.All` / `Files.ReadWrite.All` (channel files and uploads) and `People.Read` (relevance-ranked people).
3. Add an owner to the app registration, so that future redirect or scope changes don't need a ticket.

## Token store

| Platform | Result |
|---|---|
| macOS (arm64) | ✅ envelope: the data key is in the login keychain through `/usr/bin/security -i`, with no prompts. Ciphertext file is 0600, tampering is rejected by GCM, and 6 KB and 64 KB round-trip. go-keyring accepts a 2,600-byte payload and rejects 3,900 bytes (`ErrSetDataTooBig`). |
| Linux, headless (OrbStack Ubuntu, static `CGO_ENABLED=0`) | ✅ falls back to a plaintext 0600 file with a warning. The D-Bus session socket is missing. |
| Linux with an unlocked gnome-keyring (`dbus-run-session` + `gnome-keyring-daemon --unlock`) | ✅ envelope over pure-Go D-Bus. **No size limit** (5,000 bytes accepted). |
| Windows | ⚠️ cross-compiles; **not run** (no Windows host available). |
| Key Vault | ⏭ skipped by decision. The code path (`spike/kv.go`) compiles, but it has not been exercised. |

## Graph, read side

| Probe | Result | vs docs / plan |
|---|---|---|
| `GET /me/joinedTeams` | 200, 15 teams, no `nextLink` | — |
| `GET /teams/{id}/members?$top=5` | 200 **with `@odata.nextLink`** | the plan said member lists have no `nextLink` → **implement paging** |
| `GET …/channels/{id}/filesFolder` | 403; the error lists `ChannelSettings.Read.All, ChannelSettings.ReadWrite.All, Files.Read.All, Sites.Read.All, Group.Re…` | `ChannelSettings.Read.All` is accepted but missing from the docs table |
| channel messages `$top=50` / `$top=51` | 200 / **400** ("limit of '50' … exceeded") | cap enforced |
| channel messages `$filter` | **400** "Parameter 'Filter' not supported" | rejected, not ignored → fakegraph must return 400 |
| `Prefer: include-unknown-enum-members` | with it: `systemEventMessage` ×15; without: `unknownFutureValue` ×15 | confirmed: send it on every message read |
| Channel root ordering (20 roots, `$expand=replies`) | Ordered **desc by max(root.lastModifiedDateTime, newest reply createdDateTime)**. For 8 of the 20 roots, `root.lastModifiedDateTime` is older than their newest reply. | No field holds chain activity, but with `$expand=replies` the client can compute it and **stop the `--since` walk at the first older chain** |
| replies `$top=50` / `$top=51` | 200 / 400 | cap 50 confirmed |
| `GET /me/chats?$expand=lastMessagePreview&$orderby=…desc&$top=50` | 200, `nextLink`, `viewpoint` on 50 of 50 | documented unread path works |
| `/me/chats?$top=51` | 400 | cap 50 |
| `/me/chats?$expand=members` | one chat returned **116 members** | the documented 25-member cap was not observed; keep the code defensive anyway |
| chat messages, default order (150 messages) | **not** sorted by `createdDateTime` or by `lastModifiedDateTime` | always sort on the client |
| chat `$filter=lastModifiedDateTime gt X` **with** `$orderby` | correct, 0 violations, pages with `nextLink` | documented |
| same filter **without** `$orderby` | also correct (0 violations) | the docs say it is ignored; keep sending `$orderby` (documented) |
| chat `$filter=createdDateTime lt X` without `$orderby` | **ignored** (31 of 50 violate) | matches the docs |
| same, with `$orderby=createdDateTime desc` | correct | — |
| `createdDateTime gt`, `$orderby … asc` | 400, 400 | as documented |
| `GET /me/people?$search=` | 403 (no `People.Read`) | — |
| `GET /users?$filter=…'O''B'…`, `GET /users/{other}` | 403, 403: `User.Read` covers `/me` only | **`user search/show` and email→id lookup are impossible without `User.ReadBasic.All`** |
| `GET /me/chats/{id}/members` | 37 members; `userId` on 37, `email` on 35 | membership is a **people source that works without `User.ReadBasic.All`** |
| `$batch` with 2 sub-requests | outer 200; sub-statuses 200 and 403 | as documented |

## Search (`POST /search/query`, `chatMessage`)

| Probe | Result | vs docs / plan |
|---|---|---|
| `size` 25 / 26 / 50 | 25 / 26 / **50** hits | there is **no 25 cap** for `chatMessage` |
| `from=25,size=25` | 25 hits | paging works |
| `total` | **the total match count**: 1,946 for `IsMentioned:true` at `size=5`; 0 for a nonsense term | the docs say it is the count on the page; observed otherwise |
| `IsMentioned:true`, `hasAttachment:true`, `mentions:<id without dashes>` | 200 | documented terms work |
| `IsRead:false` / `IsRead:true` (any casing, with or without other terms) | **500 every time**, across 3 retries | documented but broken here → **don't build `unread` on search** |
| `sent>=D` vs `sent>=DT20:00:00Z` | 300+ hits vs 23 hits, 0 before the cut | **the time part is honored**; the MCP's day-granularity premise is wrong |
| `sent>D` | excludes the whole of day D | — |
| Timezone of a bare `sent>=D` boundary | inconclusive: the earliest of 300 scanned hits was 04:19Z, and results are unsorted | always send full UTC timestamps |
| Hit `resource` keys | `@odata.type, channelIdentity, chatId, createdDateTime, etag, from, id, importance, lastModifiedDateTime, subject, webLink` | **no `body`**: show the hit's `summary`, or fetch the message |

## Graph, write side
Targets: a sandbox channel and the self-chat. Each probe message was tagged `[teams-cli spike …]`.

| Probe | Result |
|---|---|
| Channel post with `subject` + inline image through `hostedContents[]` (`temporaryId` "1" ↔ `../hostedContents/1/$value`) | 201. The `subject` is stored and returned. The hosted-content list shows 1 item. |
| Standalone `POST …/messages/{id}/hostedContents` (the MCP's approach) | **405**: confirms the MCP bug |
| Channel reply, set/unset reaction | 201, 204/204 |
| Channel PATCH / softDelete (root and reply) with `ChannelMessage.Edit` | **403** for each: requires `ChannelMessage.ReadWrite`. *Two spike messages were left in the sandbox channel.* |
| Self-chat `48:notes`: post with `subject` + `hostedContents` | 201. `subject` is **stored on chats too**. |
| `replyWithQuote` | 201 |
| Chat reactions, PATCH | 204/204, 204 |
| `markChatUnreadForUser` / `markChatReadForUser` | 204 / 204. `viewpoint` cannot be observed for `48:notes`: get-chat doesn't return it, and the chat isn't in the top 50 of the list. Re-test on a normal chat. |
| Chat softDelete via `/users/{me}/chats/…` | 204 (documented path) |
| Chat softDelete via `/chats/…` | **405**: the `/users/{me}` quirk is real |

## Deep links seen in the wild
- "Copy link to channel" produces `/l/channel/19%3A…%40thread.tacv2/General?groupId=…&tenantId=…`, so the **channel id is percent-encoded**.
- The self-chat link is `/l/chat/48:notes/conversations?context={"contextType":"chat"}`. `48:notes` is a valid Graph chat id for posting.

## Consequences for PLAN.md (applied)
- `TEAMS_ACCESS_TOKEN`: accept the GUID `aud` as the primary form.
- Features come from `scp`. The scope tiers become *presets for the app registration*, not a promise of consent-free use.
- People resolution order: alias → entity cache → **chat/team members** → `/me/people` (if granted) → `/users` (if granted).
- Channel `--since`: use `$expand=replies` and stop at the first chain older than the window.
- Search: `sent` with full UTC timestamps; `size` up to 50; `total` is usable for "N more"; no `IsRead`; hits carry no body.
- fakegraph: 400 for `$top` > 50 and for `$filter` on channel messages; chat `createdDateTime lt` without `$orderby` is ignored; 405 for standalone hostedContents and for `/chats/…/softDelete`; member lists page.

## Still open
- Key Vault round-trip: size, latency, versioning (skipped by decision; code ready).
- Windows envelope store (no host).
- PKCE sign-in (blocked on the redirect URI, an admin change).
- A device-code sign-in with the Graph CLI Tools client by a user *without* a pre-existing grant.
- `viewpoint` change after mark-read on a normal chat.
- The timezone of a bare-date `sent` boundary.
- testscript coverage merging (needs the Phase 2 module).

## Cleanup
- The spike token cache is in `~/Library/Application Support/teams-cli-spike/`, with its key in the login keychain (service `teams-cli-spike`). `go run ./spike logout` removes both.
- OrbStack machine `teams-cli-spike` (with gnome-keyring installed) is kept for Phase 2 Linux tests. Remove it with `orb delete teams-cli-spike`.
