## teams auth logout

Sign out and delete the cached tokens

### Synopsis

Remove the cached account and delete the token store for the profile.

With --all, sign out of every configured profile: this is the "forget
everything" command, and it is the only one that deletes token material.
`teams cache clear` never does.

```
teams auth logout [flags]
```

### Options

```
      --all    sign out of every configured profile
  -h, --help   help for logout
      --yes    do not ask for confirmation
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

* [teams auth](teams_auth.md)	 - Sign in, inspect the session and sign out

