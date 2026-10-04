## teams auth login

Sign in to Microsoft Teams with a browser or a device code

### Synopsis

Sign in with the authorization code + PKCE flow in your browser, or with a
device code (`--device`) on a headless machine. When no terminal is available
the device-code flow is chosen automatically.

The MSAL token cache is written to the profile's token store: encrypted with a
key from your OS keychain by default, or a 0600 plaintext file where no
keychain is reachable.

```
teams auth login [flags]
```

### Options

```
      --device          use the device-code flow instead of the browser
  -h, --help            help for login
      --scopes string   override the profile's scopes (a preset name or a space separated list)
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

