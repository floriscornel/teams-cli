## teams post

Post a message to a channel or a chat

### Synopsis

Post a message.

The container is a channel (Engineering/General, a channel link or a channel
id, with --team when the reference does not name the team) or a chat (a chat
link or id, a topic name, @person or an e-mail address). A bare name is a chat
topic: a channel name is only unique inside its team, so a channel is always
written Team/Channel.

The text is markdown by default (--md), or plain text (--text) or HTML
(--html). With no text on a terminal, $EDITOR opens; `-` reads stdin. Files
(--file) are uploaded before the message is sent, and an image that fits the
4 MB inline-image limit is embedded in the message instead.

```
teams post <channel|chat> [text|-] [flags]
```

### Options

```
      --dry-run               print the Graph request without sending it
      --file stringArray      file to attach (repeatable); an image up to 4 MB is embedded inline
  -h, --help                  help for post
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

