# Security policy

`teams` is a command-line client for Microsoft Teams. It talks to Microsoft
Graph with tokens you grant it, so a vulnerability here can expose your mail,
chats, channels, files and tenant metadata. Please report anything suspicious
privately, before it becomes a public issue.

## Reporting a vulnerability

**Use GitHub's private vulnerability reporting:**

<https://github.com/floriscornel/teams-cli/security/advisories/new>

That form is private to the maintainers and gives us a place to discuss, fix and
credit the report. If you cannot use it, open a public issue that says only
"security report, please contact me" with no technical detail, and a maintainer
will follow up with a private channel.

Please do **not** open a public issue with reproduction steps, an exploit, or
sample tokens.

What helps us most in the report:

- the affected version (`teams version` prints version, commit and date);
- the platform and how you installed it (a GitHub Release archive, or
  `go install`);
- a minimal reproduction, ideally against the bundled fake Graph server rather
  than a real tenant;
- what an attacker gains: token disclosure, privilege escalation across
  profiles, data written to a place it should not be, and so on.

### What to expect

| Stage | Target |
|---|---|
| Acknowledgement of the report | 3 business days |
| Initial assessment (severity, affected versions) | 10 business days |
| Fix or documented mitigation | 30 days for high and critical, best effort otherwise |

We will credit you in the release notes unless you ask us not to. Please give us
the agreed window before publishing anything yourself.

## Supported versions

This is a pre-1.0 project: only the **latest tagged release** is supported, and
fixes ship as a new patch tag rather than as a backport. `v0.x` tags with a
prerelease indicator (`v0.2.0-rc.1`) are pre-releases and are not supported
either.

| Version | Supported |
|---|---|
| latest `vX.Y.Z` release | :white_check_mark: |
| older `vX.Y.Z` releases | :x: |
| `-rc.N` / `-SNAPSHOT` builds | :x: |
| `main` (built from source) | best effort |

A bad release is yanked by marking it a pre-release and shipping a fixed patch
tag; tags are never moved or re-pointed (PLAN.md "Rollback"). Checksums,
`checksums.txt.sigstore.json`, SBOMs and a build-provenance attestation are
published with every release so you can verify what you downloaded:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify --owner floriscornel <artifact>
```

## Secrets never travel on the command line

The CLI never accepts a token, client secret or password as a command-line
argument, and never prints one or writes one to a config file (PLAN.md
"Secrets", AGENTS.md "Project conventions"):

- Tokens live in the OS keyring, or are supplied through the environment (for
  example the bot flow's `TEAMS_*` variables), or fetched at runtime from Azure
  Key Vault through OIDC.
- `teams` rejects token-like flags instead of reading them, because arguments
  are visible to every other process on the machine through `ps`, land in shell
  history, and are copied into CI logs and crash reports.
- Nothing in this repository contains credentials: the release workflow uses
  only the automatic `GITHUB_TOKEN` and the runner's OIDC token, and it publishes
  to this repository only (PLAN.md "Release secrets").

If you find a code path that takes a secret from `argv`, prints one, logs one, or
writes one to disk, treat it as a vulnerability and report it through the private
channel above.

## Scope

In scope: the `teams` CLI and its release artifacts (the archives on the GitHub
Release, the checksums, the SBOMs and the attestation), the fake Graph and fake
identity-provider test servers, and the release pipeline.

Out of scope: vulnerabilities in Microsoft Graph, Entra ID or Teams themselves
(report those to Microsoft), and findings that require an attacker to already
control your machine, your keyring, or your Microsoft tenant.
