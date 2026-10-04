## teams chat mark-unread

Mark a chat unread

### Synopsis

Move your own read watermark for a chat.

Graph stores it in the chat's viewpoint, which is what `teams unread` and
`teams chat list --unread` read back
(refs/graph/api-reference/v1.0/resources/chatviewpoint.md).

```
teams chat mark-unread <chat> [flags]
```

### Options

```
      --dry-run   print the Graph request without sending it
  -h, --help      help for mark-unread
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

