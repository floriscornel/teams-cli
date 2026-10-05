## teams calendar show

Show one event in full

### Synopsis

Show one event: when, where, who, your response and the Teams join URL.

<event> is a handle from `teams calendar list`/`search`, a full event id, or
an Outlook web link. Days and times are shown in your local zone.

```
teams calendar show <event> [flags]
```

### Options

```
  -h, --help   help for show
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

* [teams calendar](teams_calendar.md)	 - List, show and search your calendar

