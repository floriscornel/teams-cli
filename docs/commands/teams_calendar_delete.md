## teams calendar delete

Delete an event

### Synopsis

Delete an event from your calendar.

If you organize it and it has attendees, they are sent a cancellation.

```
teams calendar delete <event> [flags]
```

### Options

```
      --dry-run   print the Graph request without sending it
  -h, --help      help for delete
      --yes       skip the confirmation prompt
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

* [teams calendar](teams_calendar.md)	 - List, show and search your calendar, and act on meetings

