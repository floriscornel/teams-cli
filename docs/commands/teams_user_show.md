## teams user show

Show one user

### Synopsis

Show one user. <user> is an e-mail address, a user principal name, an id,
@name or a display name; a name is resolved through the entity cache, the
members of your chats and teams, /me/people and finally the directory
(PLAN.md:167).

```
teams user show <user> [flags]
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

* [teams user](teams_user.md)	 - Search the directory and show users

