## teams chat delete

Delete a chat (needs admin consent, requested on demand)

### Synopsis

Delete a chat.

Graph needs Chat.ManageDeletion.All for this, which no preset carries because an
admin has to consent to it. On a terminal the CLI asks to sign in again with that
scope; anywhere else it fails with exit 3 and the text of the admin request
(`teams auth status --admin-request`).

Deleting a chat is confirmed first; --yes skips the prompt, and is required in
non-interactive mode.

```
teams chat delete <chat> [flags]
```

### Options

```
      --dry-run   print the Graph request without sending it
  -h, --help      help for delete
      --yes       skip the confirmation prompt
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

* [teams chat](teams_chat.md)	 - List, show, read and manage your chats

