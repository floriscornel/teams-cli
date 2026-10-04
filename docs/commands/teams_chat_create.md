## teams chat create

Create a group chat or a one-on-one chat

### Synopsis

Create a chat. --with names the other participants (repeatable); you are added
automatically. One participant makes a one-on-one chat, which the API returns
from its cache when that chat already exists; two or more make a group chat,
where --topic is allowed.

```
teams chat create --with <person> [--with <person>…] [--topic <title>] [flags]
```

### Options

```
      --dry-run            print the Graph request without sending it
  -h, --help               help for create
      --topic string       title of the chat (group chats only)
      --with stringArray   person to include (repeatable): @name, an e-mail address or a user id
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

