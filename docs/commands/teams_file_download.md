## teams file download

Download a message's attachments and inline images

### Synopsis

Download the files a message carries into a directory (the current one by
default). Files are written 0600, and an existing file is never replaced
unless --overwrite is passed. --name filters by file name, --no-images
skips the inline images, -o chooses the directory.

```
teams file download <message> [flags]
```

### Options

```
  -h, --help            help for download
      --name string     only download attachments whose name contains this text
      --no-images       skip the message's inline images
  -o, --output string   directory to write the files into (default ".")
      --overwrite       replace files that already exist
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

* [teams file](teams_file.md)	 - Download files attached to a message

