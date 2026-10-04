## teams team show

Show one team

### Synopsis

Show one team. <team> is a team name, Team/Channel, a Teams link or an id.
Names are matched case-insensitively and resolved through the entity cache;
--refresh resolves them again.

```
teams team show <team> [flags]
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

* [teams team](teams_team.md)	 - List and show the teams you belong to

