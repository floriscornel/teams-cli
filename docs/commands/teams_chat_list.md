## teams chat list

List your chats, newest activity first

```
teams chat list [flags]
```

### Options

```
      --all            page through every chat (slow on a busy tenant: each page expands the members)
  -h, --help           help for list
      --limit int      maximum number of chats, newest first (default 50)
      --topic string   only chats whose topic contains this text
      --unread         only chats with unread messages
      --with string    only the 1:1 chat with this person
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

* [teams chat](teams_chat.md)	 - List, show and read your chats

