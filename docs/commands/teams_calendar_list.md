## teams calendar list

List meetings for a day or a range

### Synopsis

List your meetings, or a colleague's, for one day or a date range.

Days are local (--tz, or this machine's zone). Another user's calendar is
shown in full when it is shared with you; otherwise the command falls back to
free/busy, which shows only times and status (--free-busy asks for that
directly). The ID column is a short handle for the full event id, usable with
`teams calendar show`.

```
teams calendar list [--user <who>]… [--date <day>] [--days N] [--from <day> --to <day>] [flags]
```

### Options

```
      --all                 show every row instead of --limit
      --chat                resolve each Teams meeting's chat and add a Chat column
      --date string         the day to show: today, tomorrow, yesterday, YYYY-MM-DD, +Nd, -Nd or a weekday name (default "today")
      --days int            how many days to show from --date (default 1)
      --free-busy           show free/busy only, even for your own shared calendars
      --from string         first day of a range (inclusive); must be paired with --to
  -h, --help                help for list
      --include-cancelled   also show events that were cancelled
      --limit int           maximum number of rows (default 200)
      --to string           last day of a range (inclusive); must be paired with --from
      --tz string           IANA time zone for the days and times (default: this machine's)
      --user strings        a colleague whose calendar to include (repeatable, comma-separated; default: you)
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

