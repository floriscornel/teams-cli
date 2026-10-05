## teams calendar search

Search your calendar

### Synopsis

Search events in your PRIMARY calendar only — no shared or delegated
calendar is searched, and no other calendar can be asked for. Results are
not sorted by the service and come 25 per page; --limit/--all decide how
many are shown.

Results are printed with the same columns and the same --json schema as
`teams calendar list`.

```
teams calendar search <query> [flags]
```

### Options

```
      --all         page through every result
  -h, --help        help for search
      --limit int   maximum number of results (default 25)
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

