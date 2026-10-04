## teams auth status

Show the signed-in account, its scopes and the token store

### Synopsis

Show who you are signed in as, which scopes the current token carries (the
`scp` claim lists every consented scope), and where the token cache lives.

Exits 3 when the profile is not signed in or the refresh token is dead.

```
teams auth status [flags]
```

### Options

```
      --admin-request   print the admin-consent and redirect-URI changes to request
  -h, --help            help for status
```

### Options inherited from parent commands

```
      --json             machine-readable JSON output
      --no-color         disable colours (also honours NO_COLOR)
      --no-input         never prompt; fail fast instead
      --profile string   profile to use (default: the configured default_profile)
      --quiet            suppress progress messages on stderr
      --read-only        refuse write commands
  -v, --verbose count    verbose diagnostics on stderr (-v, -vv)
```

### SEE ALSO

* [teams auth](teams_auth.md)	 - Sign in, inspect the session and sign out

