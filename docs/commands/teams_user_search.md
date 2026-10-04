## teams user search

Search the directory for a person

### Synopsis

Search the directory with a startswith filter on displayName, falling back
to userPrincipalName and mail when nothing matches. A single quote in the
query is escaped by doubling it, which OData requires and the MCP never does
(PLAN.md:231).

```
teams user search <query> [flags]
```

### Options

```
      --all         fetch every page
  -h, --help        help for search
      --limit int   maximum number of users (default 25)
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

