## teams calendar cancel

Cancel a meeting you organize

### Synopsis

Cancel a meeting. Only the organizer can cancel; use `teams calendar
decline` to turn down an invitation instead.

Your attendees are told the meeting is cancelled.

```
teams calendar cancel <event> [flags]
```

### Options

```
      --comment string   message to send to the attendees
      --dry-run          print the Graph request without sending it
  -h, --help             help for cancel
      --yes              skip the confirmation prompt
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

