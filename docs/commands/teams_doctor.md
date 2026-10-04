## teams doctor

Check the config, token store, scopes, clock and Graph reachability

### Synopsis

Diagnose the local setup and print the fix for every problem.

doctor checks the config file, the token store (including keychain
reachability), the scopes the current token carries against the feature
matrix, the clock skew against Graph's own clock, and Graph reachability.
It never makes a write call; --offline skips the network checks entirely.

```
teams doctor [flags]
```

### Options

```
  -h, --help      help for doctor
      --offline   skip the checks that need the network
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

* [teams](teams.md)	 - Search, read and post in Microsoft Teams from the terminal

