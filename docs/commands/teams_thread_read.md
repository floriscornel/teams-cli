## teams thread read

Read one message and, for a channel, its thread

### Synopsis

Read a message. <message> is a Teams message link, Team/Channel/MessageId,
Team/Channel/<id> or a chat message link. Channel threads are read with the
messages/{id} and messages/{id}/replies endpoints; chats have no threads, so a
chat message is shown on its own.

```
teams thread read <message> [flags]
```

### Options

```
      --all            fetch every page of replies
  -h, --help           help for read
      --limit int      maximum number of replies (0 means every page)
      --since string   only replies newer than this (a duration like 24h, or a timestamp)
      --team string    team the channel belongs to (when the reference does not say)
      --until string   only replies older than this (a duration like 24h, or a timestamp)
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

* [teams thread](teams_thread.md)	 - Read a message and its replies

