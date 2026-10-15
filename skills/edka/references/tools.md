# Tools

Context, profiles, health checks, shell completion, upgrades and the agent skill of the CLI.

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

## `edka skill install`

Install the agent skill for Claude Code and other agents.

Write the agent skill that matches this version of the CLI to
`~/.claude/skills/edka`, where Claude Code finds it. The skill shows a coding agent
how to check which cluster and deployment a command acts on, diagnose a failure,
preview a change and ask before a destructive command. Its references list every
command with its flags.

`--project` writes it to `.claude/skills/edka` at the root of the current
repository, to commit with the project. `--dir` writes it to another agent's skills
directory. Run the install again after `edka upgrade` to match the new version.

An install replaces the files an earlier one wrote. It asks before it replaces a
skill directory that holds other files.

Flags:

- `--dir string`: Install in this skills directory, such as ~/.agents/skills
- `--project`: Install in .claude/skills at the root of this repository

Examples:

```bash
edka skill install
edka skill install --project
edka skill install --dir ~/.agents/skills --json
```

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
