## teams search

Search Teams messages

### Synopsis

Search Teams messages with KQL. The documented scope terms are available
through flags (--from, --to, --mentions-me, --has-attachment) and inline in
<query>, so from:bob sent>2026-10-01 keeps working.

A hit carries no message body: Graph returns the id, the sender, the
timestamps, the subject and a web link (docs/spike/phase1.md:83).
Use `teams thread read <link>` for the text.

```
teams search <query> [flags]
```

### Options

```
      --from string      only messages sent by this person (from:)
      --has-attachment   only messages with attachments
  -h, --help             help for search
      --in string        only messages in this channel or chat
      --limit int        results per page (the API allows up to 50) (default 25)
      --mentions-me      only messages that mention you
      --page int         1-based page number (the search API pages by from/size) (default 1)
      --since string     only messages newer than this (a duration like 7d, or a timestamp)
      --to string        only messages sent to this person (to:, 1:1 chats only)
      --until string     only messages older than this (a duration like 7d, or a timestamp)
```

### Options inherited from parent commands

```
      --jq string        filter JSON output through a jq expression (implies --json)
      --json             machine-readable JSON output
      --no-color         disable colours (also honours NO_COLOR)
      --no-input         never prompt; fail fast instead
      --profile string   profile to use (default: the configured default_profile)
      --quiet            suppress progress messages on stderr
      --read-only        refuse write commands
      --refresh          ignore cached names and resolve them again
  -v, --verbose count    verbose diagnostics on stderr (-v, -vv)
```

### SEE ALSO

* [teams](teams.md)	 - Search, read and post in Microsoft Teams from the terminal

