## teams delete

Delete a message (soft delete)

### Synopsis

Delete a message the way Teams does: a soft delete, which keeps the id and
leaves a tombstone in the conversation
(refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md).

A chat message is deleted through the user-relative route the docs document
(POST /users/{user-id}/chats/{chat-id}/messages/{id}/softDelete), because the
/chats form answers 405 (docs/spike/phase1.md:99).

```
teams delete <message> [flags]
```

### Options

```
      --dry-run       print the Graph request without sending it
  -h, --help          help for delete
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

