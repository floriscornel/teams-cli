## teams react

React to a message, or remove your reaction

### Synopsis

React to a message with an emoji, or remove the reaction with --remove.

The reaction is sent as the reactionType string Graph documents, which is the
unicode character itself ("like" is the one named example); the CLI passes it
through, so any emoji Teams accepts works
(refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:68).

```
teams react <message> <emoji> [flags]
```

### Options

```
      --dry-run       print the Graph request without sending it
  -h, --help          help for react
      --remove        remove your reaction instead of adding it
      --team string   team the channel belongs to, when the reference does not say
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

