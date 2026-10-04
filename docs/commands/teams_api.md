## teams api

Call Microsoft Graph directly

### Synopsis

Call a Graph endpoint directly, for the operations the CLI does not wrap.

<method> is an HTTP method (GET, POST, PATCH, PUT, DELETE) and <path> is a
Graph path such as /me or /teams/{id}/channels; it may carry a query string.

Fields:
  -f key=value   a string field, dotted keys nest (body.content=hi)
  -F key=value   a typed field: true, false, null and numbers keep their type
  --input <f>    the whole JSON body from a file, or - for stdin

Read-only mode refuses every method but GET and HEAD.

```
teams api <method> <path> [flags]
```

### Options

```
      --dry-run                 print the request without sending it
  -f, --field stringArray       add a string field to the JSON body (key=value, dotted keys nest)
      --header stringArray      extra request header ("Name: value", repeatable)
  -h, --help                    help for api
      --input string            read the JSON body from a file, or - for stdin
      --query stringArray       query parameter (key=value, repeatable)
  -F, --raw-field stringArray   add a typed field (true/false, a number and null keep their type)
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

* [teams](teams.md)	 - Search, read and post in Microsoft Teams from the terminal

