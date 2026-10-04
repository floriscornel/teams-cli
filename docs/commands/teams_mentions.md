## teams mentions

Show the messages that mention you

### Synopsis

Show the messages that mention you, newest first. The search runs with
IsMentioned:true and a sent>= window, and the timestamps are checked again on
the client, which is what makes --since exact (PLAN.md:233). The default
window is 24h.

```
teams mentions [flags]
```

### Options

```
  -h, --help           help for mentions
      --limit int      results per page (the API allows up to 50) (default 25)
      --since string   only mentions newer than this (default 24h; a duration or a timestamp)
      --until string   only mentions older than this (a duration or a timestamp)
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

