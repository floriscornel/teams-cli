## teams channel read

Read a channel's root messages

### Synopsis

Read a channel's messages. --since and --until are applied on the client:
the channel message endpoint supports only $top and $expand, and $filter is a
400 rather than a silent ignore, so the CLI pages the listing and stops at the
first thread older than the window (PLAN.md:187).

```
teams channel read <channel> [flags]
```

### Options

```
      --all            fetch every page instead of --limit
  -h, --help           help for read
      --limit int      maximum number of messages (newest first) (default 20)
      --replies        include each thread's replies
      --since string   only messages newer than this (a duration like 24h, or a timestamp)
      --team string    team the channel belongs to (when the reference does not say)
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

* [teams channel](teams_channel.md)	 - List, read and inspect channels

