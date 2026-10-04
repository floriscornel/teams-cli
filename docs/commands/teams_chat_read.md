## teams chat read

Read a chat's messages

### Synopsis

Read a chat's messages. --since and --until are applied on the server: the
chat message endpoint documents $orderby and a $filter with gt/lt on one date
property, and a $filter that does not match the $orderby is silently ignored -
so --since uses lastModifiedDateTime gt and the result is re-checked on
createdDateTime on the client (PLAN.md:183).

```
teams chat read <chat> [flags]
```

### Options

```
      --all            fetch every page instead of --limit
      --from string    only messages sent by this person
  -h, --help           help for read
      --limit int      maximum number of messages (newest first) (default 20)
      --since string   only messages newer than this (a duration like 24h, or a timestamp)
      --until string   only messages older than this (a duration like 24h, or a timestamp)
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

* [teams chat](teams_chat.md)	 - List, show, read and manage your chats

