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

* [teams alias](teams_alias.md)	 - Name a person, chat or channel once and reuse it everywhere
* [teams api](teams_api.md)	 - Call Microsoft Graph directly
* [teams auth](teams_auth.md)	 - Sign in, inspect the session and sign out
* [teams cache](teams_cache.md)	 - Inspect and clear the local cache
* [teams channel](teams_channel.md)	 - List, read and inspect channels
* [teams chat](teams_chat.md)	 - List, show, read and manage your chats
* [teams config](teams_config.md)	 - Read and write the config file
* [teams delete](teams_delete.md)	 - Delete a message (soft delete)
* [teams doctor](teams_doctor.md)	 - Check the config, token store, scopes, clock and Graph reachability
* [teams edit](teams_edit.md)	 - Edit a message you sent
* [teams file](teams_file.md)	 - Download files attached to a message
* [teams mentions](teams_mentions.md)	 - Show the messages that mention you
* [teams post](teams_post.md)	 - Post a message to a channel or a chat
* [teams profile](teams_profile.md)	 - List or select profiles
* [teams react](teams_react.md)	 - React to a message, or remove your reaction
* [teams reply](teams_reply.md)	 - Reply to a message
* [teams search](teams_search.md)	 - Search Teams messages
* [teams team](teams_team.md)	 - List and show the teams you belong to
* [teams thread](teams_thread.md)	 - Read a message and its replies
* [teams unread](teams_unread.md)	 - Show unread chats and the mentions you have not read
* [teams user](teams_user.md)	 - Search the directory and show users
* [teams version](teams_version.md)	 - Print the CLI version and build information
* [teams whoami](teams_whoami.md)	 - Show the signed-in user

