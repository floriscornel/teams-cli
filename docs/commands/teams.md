## teams

Search, read and post in Microsoft Teams from the terminal

### Synopsis

teams is a standalone CLI for Microsoft Teams.

It signs in as you (or as a service account) with delegated Microsoft Graph
permissions, keeps the token cache encrypted, and never prompts in
non-interactive mode.

### Options

```
  -h, --help             help for teams
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
* [teams cache](teams_cache.md)	 - Inspect and clear the local cache
* [teams config](teams_config.md)	 - Read and write the config file
* [teams doctor](teams_doctor.md)	 - Check the config, token store, scopes, clock and Graph reachability
* [teams profile](teams_profile.md)	 - List or select profiles
* [teams version](teams_version.md)	 - Print the CLI version and build information
* [teams whoami](teams_whoami.md)	 - Show the signed-in user

