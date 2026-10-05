## teams calendar accept

Accept a meeting invitation

### Synopsis

Accept a meeting you were invited to. The organizer is notified unless
--no-notify is passed.

You cannot respond to a meeting you organize: Graph refuses it, so the CLI
does too, before sending anything.

```
teams calendar accept <event> [flags]
```

### Options

```
      --comment string   message to send with the response
      --dry-run          print the Graph request without sending it
  -h, --help             help for accept
      --no-notify        do not tell the organizer about the response
      --notify           tell the organizer about the response (default true)
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

