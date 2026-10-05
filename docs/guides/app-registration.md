# Register your own Entra app (recommended)

`teams` signs in with a **delegated** Microsoft Graph identity: it acts as a user,
never as an application. By default it uses the Microsoft Graph CLI Tools public
client, which many tenants block or restrict. Registering your own app takes a
few minutes, needs one admin consent, and gives you a stable `client_id` you
control.

You need an Entra ID tenant, permission to register an application (or an admin
who will do it), and one person who can consent on behalf of the tenant.

## 1. Create the registration

1. In the [Entra admin center](https://entra.microsoft.com) open
   **Applications → App registrations → New registration**.
2. Name it something recognisable, for example `teams CLI`.
3. **Supported account types:** *Accounts in this organizational directory only*.
   (Use *Multitenant* only if people outside your tenant will sign in.)
4. Leave the redirect URI empty for now and create it.

## 2. Let the CLI sign in

Open the new registration's **Authentication** page:

1. **Add a platform → Mobile and desktop applications** and add
   `http://localhost` as a redirect URI. Entra ignores the port for a localhost
   redirect, so the CLI's ephemeral port works with this one entry.
2. Under **Advanced settings**, set **Allow public client flows** to **Yes**.
   Without it, the device-code flow (`teams auth login --device`) fails.

If the browser flow still answers `AADSTS50011`, that redirect URI is what is
missing.

## 3. Add the delegated Graph permissions

On **API permissions → Add a permission → Microsoft Graph → Delegated
permissions**, add the set that matches how you will use the CLI. `teams doctor`
tells you which commands each set unlocks.

| Preset | Delegated permissions |
|---|---|
| `chats` | `User.Read`, `User.ReadBasic.All`, `People.Read`, `Chat.ReadBasic`, `Chat.Read`, `Chat.ReadWrite`, `ChatMessage.Send`, `Files.ReadWrite` |
| `read-only` | `chats` without the three writes, plus `Team.ReadBasic.All`, `Channel.ReadBasic.All`, `ChannelMessage.Read.All`, `TeamMember.Read.All`, `Files.Read.All` |
| `full` | `read-only` plus `ChannelMessage.Send`, `ChannelMessage.ReadWrite`, `Chat.ReadWrite`, `ChatMessage.Send`, `Files.ReadWrite.All` |

Notes:

- **`ChannelMessage.Read.All`, `ChannelMessage.ReadWrite` and
  `TeamMember.Read.All` require admin consent**, so reading a channel usually
  needs an admin even though writing in a chat does not.
- `Files.Read.All` / `Files.ReadWrite.All` reach a team's SharePoint drive, which
  is where channel files live. `Files.ReadWrite` alone covers only your own
  OneDrive (chat attachments).
- Do **not** add `offline_access` (or `openid`/`profile`): MSAL adds them itself.
- `teams chat delete` needs `Chat.ManageDeletion.All`, which is in no preset. The
  CLI requests it on demand; add it here if you want the command to work without
  a second sign-in.
- **The calendar commands need their own permissions**, and none of them belongs
  to a preset:

  | Command | Delegated permission |
  |---|---|
  | `teams calendar list` (your own), `show`, `search` | `Calendars.Read` |
  | `teams calendar list --user <colleague>` | `Calendars.Read.Shared` |
  | `teams calendar list --chat`, and the chat line of `show` | `OnlineMeetings.Read` |
  | the calendar writes (`create`, `update`, `accept`, `tentative`, `decline`, `cancel`, `delete`) | `Calendars.ReadWrite` |

  Add `Calendars.Read` and `Calendars.Read.Shared` to read calendars, plus
  `Calendars.ReadWrite` if the calendar writes should work without a second
  sign-in, and `OnlineMeetings.Read` for the Teams-meeting chat lookup.

  They are deliberately **not** in a preset: adding a scope to one makes every
  existing sign-in request it, and Entra consent is all-or-nothing per request,
  so a profile that has not consented yet fails with `AADSTS65001`. Without them
  here the CLI asks for them the first time a calendar command runs, on a
  terminal.
- The reference does not mark any calendar scope as requiring admin consent, but
  some tenants block user consent anyway. If a calendar command fails with a
  consent error, send an admin the request from
  `teams auth status --admin-request`.

Then click **Grant admin consent** (or send the request to an admin).

## 4. Keep it maintainable

- **Owners:** on **Owners**, add the people who may change the registration, so a
  redirect or scope change does not need a ticket.
- **Credentials:** nothing else is needed. The CLI is a public client and uses no
  secret, which is why no password or certificate is created here.

## 5. Point the CLI at it

```bash
teams config set profiles.me.client_id 00000000-0000-0000-0000-000000000000
teams config set profiles.me.tenant contoso.com        # or the tenant ID
teams config set profiles.me.scopes full               # chats | read-only | full
teams auth login
teams doctor
```

`teams doctor` reports the account, the scopes the token carries, and what each
command needs; `teams auth status --admin-request` prints the consent text for
anything still missing.

## Doing the same for a service account

Register one app (or reuse this one), give the service account a Teams license,
and sign in once as that user:

```bash
teams auth login --profile bot --device
teams --profile bot chat list
```

The token cache is stored for that profile on that machine. Reading the cache
from an unattended runner (Key Vault) is on the roadmap; `TEAMS_ACCESS_TOKEN` is
the workaround until then, for a token your runner mints itself.
