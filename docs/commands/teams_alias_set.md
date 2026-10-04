## teams alias set

Create or replace an alias

### Synopsis

Create or replace an alias. The target is anything the CLI accepts as a
reference: a person (alice@example.com or @alice), a chat, Team/Channel, a
Teams link or an id. Aliases are per profile.

```
teams alias set <name> <target> [flags]
```

### Options

```
  -h, --help   help for set
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

* [teams alias](teams_alias.md)	 - Name a person, chat or channel once and reuse it everywhere

