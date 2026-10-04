## teams version

Print the CLI version and build information

### Synopsis

Print the version, the commit and the build date.

With --check, ask GitHub Releases whether a newer `teams` exists. The check is
opt-out (`TEAMS_NO_UPDATE_CHECK=1` or `update_check = false` in the config), and
outside a terminal it only ever runs because --check asked for it.

```
teams version [--check] [flags]
```

### Options

```
      --check   check GitHub Releases for a newer version
  -h, --help    help for version
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

