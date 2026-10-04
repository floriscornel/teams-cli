## teams cache clear

Delete the rebuildable cache (never tokens or config)

### Synopsis

Delete local, rebuildable data.

With no flags it removes the entity cache; --names removes only the
name/person cache; --ai also removes AI session history and memory;
--all removes everything except the config file and the token secrets.

Tokens are never deleted: use `teams auth logout` for that.

```
teams cache clear [flags]
```

### Options

```
      --ai      also clear AI session history and memory
      --all     clear all local state except config and tokens
  -h, --help    help for clear
      --names   clear only the name and person cache
      --yes     do not ask for confirmation
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

* [teams cache](teams_cache.md)	 - Inspect and clear the local cache

