## teams chat add-member

Add one or more members to a chat

### Synopsis

Add members to a chat. Each one is added with the documented owner role: a
guest role is a tenant decision, and `member` is not accepted in the delegated
flow (refs/graph/api-reference/v1.0/api/chat-post.md:47).

Every member is attempted, so one failure does not hide the others; the
command exits 1 when any of them failed.

```
teams chat add-member <chat> <person…> [flags]
```

### Options

```
      --dry-run   print the Graph requests without sending them
  -h, --help      help for add-member
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

