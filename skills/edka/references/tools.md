# Tools

Context, profiles, health checks, shell completion and upgrades of the CLI.

## `edka completion <bash|zsh|fish|powershell>`

Generate shell completions.

## `edka context`

Show effective profile and project context.

## `edka doctor`

Check CLI connectivity, authentication and context.

## `edka profile list`

List profiles without secrets.

## `edka profile remove <name>`

Remove a signed-out profile.

Remove a profile's saved API address, organization and cluster. Sign out of
the profile first, so its credentials don't outlive it.

The default profile always exists. Removing it resets it to
https://api.edka.io. When you remove the active profile, default becomes active.

Examples:

```bash
edka logout --profile staging
edka profile remove staging
```

## `edka profile use <name>`

Set the default profile.

## `edka unlink`

Remove this directory's project context.

## `edka upgrade`

Install the latest release of the CLI.

Download the latest release from github.com/edkadigital/cli, check it against the
SHA-256 in the release's checksums.txt, and replace this binary with it.

A download that does not match replaces nothing. `--check` reports the latest
release without installing it.

Flags:

- `--check`: Report the latest release without installing it

Examples:

```bash
edka upgrade
edka upgrade --check
edka upgrade --check --json
```

## `edka version`

Show the version, commit and API revision of this binary.

Show what this binary is: its version, the commit it was built from, its Go
release, and the revision of the Edka API that the `edka api` commands were
generated from. Include the output in a bug report.

Examples:

```bash
edka version
edka version --json
```

Every command also takes the global flags in SKILL.md.
