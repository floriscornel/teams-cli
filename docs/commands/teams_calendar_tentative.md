## teams calendar tentative

Tentatively accept a meeting invitation

### Synopsis

Tentatively accept a meeting you were invited to, optionally proposing a
different time with --propose.

--propose needs --notify (it is a response to the organizer), and the
organizer must allow new-time proposals.

```
teams calendar tentative <event> [flags]
```

### Options

```
      --comment string   message to send with the response
      --dry-run          print the Graph request without sending it
  -h, --help             help for tentative
      --no-notify        do not tell the organizer about the response
      --notify           tell the organizer about the response (default true)
      --propose string   propose a new time as <start>/<end>
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

