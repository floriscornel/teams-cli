## teams edit

Edit a message you sent

### Synopsis

Edit a message's body (and, with --subject, its subject).

Only the body and the subject are editable, which is what the delegated PATCH
documents (refs/graph/api-reference/v1.0/api/chatmessage-update.md:44-45);
attachments cannot be added to an existing message, so --file is refused.

```
teams edit <message> [text|-] [flags]
```

### Options

```
      --dry-run               print the Graph request without sending it
      --file stringArray      file to attach (repeatable); an image up to 4 MB is embedded inline
  -h, --help                  help for edit
      --html                  send the text as HTML, sanitized against the Teams allow-list
      --importance string     message importance: high or urgent (default normal)
      --md                    treat the text as markdown (the default) (default true)
      --mention stringArray   person to mention (repeatable); the text must name them, e.g. @alice
      --subject string        subject line (channels and chats both store one)
      --team string           team the channel belongs to, when the reference does not say
      --text                  send the text verbatim, without markdown or HTML
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

