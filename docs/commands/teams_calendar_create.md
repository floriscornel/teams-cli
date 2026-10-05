## teams calendar create

Create an event, optionally with attendees and a Teams meeting

### Synopsis

Create an event. Days and times are LOCAL (--tz, or this machine's zone).

Attendees are invited immediately: on a terminal the command asks first, and
anywhere else it needs --yes. --teams asks for a Teams meeting, which the
mailbox has to allow.

```
teams calendar create --subject <s> (--start <time> [--end <time> | --duration <d>] | --all-day --date <day> [--days N]) [flags]
```

### Options

```
      --all-day             create a floating all-day event instead of a timed one
      --attendee strings    invite this person (repeatable, comma-separated)
      --body string         the event body text
      --date string         the day to show: today, tomorrow, yesterday, YYYY-MM-DD, +Nd, -Nd or a weekday name (default "today")
      --days int            how many days to show from --date (default 1)
      --dry-run             print the Graph request without sending it
      --duration string     length when --end is not given (default 30m)
      --end string          end time
      --from string         first day of a range (inclusive); must be paired with --to
  -h, --help                help for create
      --include-cancelled   also show events that were cancelled
      --location string     the location
      --start string        start time (YYYY-MM-DD HH:MM, HH:MM, or RFC3339)
      --subject string      the event subject
      --teams               make it a Teams online meeting
      --to string           last day of a range (inclusive); must be paired with --from
      --tz string           IANA time zone for the days and times (default: this machine's)
      --yes                 skip the confirmation prompt when there are attendees
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

