## teams calendar update

Change the fields you name on an event

### Synopsis

Change an event. Only the fields you name are sent, so a change to the time
cannot drop the meeting body or the Teams link.

As a non-organizer an update succeeds but changes only YOUR copy, which the
organizer's next update overwrites; pass --local-copy to do it anyway.

```
teams calendar update <event> [--subject] [--start] [--end|--duration] [--location] [--body] [flags]
```

### Options

```
      --body string       new body text (rewriting a body can drop the Teams meeting details)
      --dry-run           print the Graph request without sending it
      --duration string   new length (30m, 1h30m); used when --end is not given
      --end string        new end time
  -h, --help              help for update
      --local-copy        update your own copy even though you are not the organizer
      --location string   new location
      --start string      new start time
      --subject string    new subject
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

