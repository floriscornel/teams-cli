## teams calendar

List, show and search your calendar, and act on meetings

### Synopsis

Read your calendar, or a colleague's, and act on meetings.

Days and times are LOCAL: a day runs midnight to midnight in --tz, or in
this machine's zone when --tz is not given. All-day events are floating, so
they are matched by date and never shifted by a timezone conversion.

The write commands notify people: an invitation reaches every --attendee as
soon as the event is created, accepting or declining tells the organizer,
and cancelling or deleting a meeting you organize sends your attendees a
cancellation. They ask for confirmation first, and take --dry-run.

Calendar scopes are requested at first use, so your profile may ask for them
the first time one of these commands runs.

```
teams calendar [flags]
```

### Options

```
  -h, --help   help for calendar
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
* [teams calendar accept](teams_calendar_accept.md)	 - Accept a meeting invitation
* [teams calendar cancel](teams_calendar_cancel.md)	 - Cancel a meeting you organize
* [teams calendar create](teams_calendar_create.md)	 - Create an event, optionally with attendees and a Teams meeting
* [teams calendar decline](teams_calendar_decline.md)	 - Decline a meeting invitation
* [teams calendar delete](teams_calendar_delete.md)	 - Delete an event
* [teams calendar list](teams_calendar_list.md)	 - List meetings for a day or a range
* [teams calendar search](teams_calendar_search.md)	 - Search your calendar
* [teams calendar show](teams_calendar_show.md)	 - Show one event in full
* [teams calendar tentative](teams_calendar_tentative.md)	 - Tentatively accept a meeting invitation
* [teams calendar update](teams_calendar_update.md)	 - Change the fields you name on an event

