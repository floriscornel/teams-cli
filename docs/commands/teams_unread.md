## teams unread

Show unread chats and the mentions you have not read

### Synopsis

Show what is waiting for you: chats whose newest message is newer than
their read watermark, and the messages that mention you. It never marks
anything read.

Both halves look at the last 24h by default; --since/--until change the
window and --all removes it, which lists every unread chat however old.

```
teams unread [flags]
```

### Options

```
      --all            every unread chat, however old (no window)
      --chats          only unread chats
  -h, --help           help for unread
      --mentions       only unread mentions
      --since string   window to look back over (default 24h; a duration or a timestamp)
      --until string   ignore activity newer than this
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

