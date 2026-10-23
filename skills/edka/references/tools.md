# Tools

Context, profiles, health checks, shell completion, upgrades and the agent skills of the CLI.

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

## `edka skill install [skill...]`

Install the agent skills for Claude Code and other agents.

Write the agent skills that match this version of the CLI for each coding agent
installed here, to the skills directory it reads, each skill in a directory of its
name:

- `edka` shows a coding agent how to check which cluster and deployment a command
  acts on, diagnose a failure, preview a change and ask before a destructive
  command. Its references list every command with its flags.
- `edka-app` shows it how to package an application as an Edka app, validate and
  publish it, and install it on a cluster.

An agent is installed when its settings directory exists, such as `~/.claude` or
`~/.cursor`. `--agent` names the agents to install for instead, found or not:
claude-code, codex, cursor, gemini-cli, github-copilot, opencode, windsurf, amp,
goose, droid, kiro-cli, cline, roo, junie, qwen-code and hermes-agent. With no
agent found, the install is for Claude Code and Codex. Claude Code reads
`$CLAUDE_CONFIG_DIR/skills` when the variable is set, and Codex, Cline and other
agents share `~/.agents/skills`.

Name skills to install only those. `--project` writes them to the agents' skills
directories at the root of the current repository, such as `.claude/skills` and
`.agents/skills`, to commit with the project. `--dir` writes them to one directory
instead. `edka upgrade` rewrites the skills in the agents' directories in your home;
run the install again after it for the other directories.

An install replaces the files an earlier one wrote. It asks before it replaces a
skill directory that holds other files.

Flags:

- `--agent strings`: Install for these agents, found or not; repeatable
- `--dir string`: Install in this skills directory alone
- `--project`: Install in the agents' skills directories at the root of this repository

Examples:

```bash
edka skill install
edka skill install edka-app
edka skill install --agent claude-code,cursor
edka skill install --project
edka skill install --dir ./skills --json
```

## `edka unlink`

Remove this directory's project context.

## `edka upgrade`

Install the latest release of the CLI.

Download the latest release from github.com/edkadigital/cli, check it against the
SHA-256 in the release's checksums.txt, and replace this binary with it.

A download that does not match replaces nothing. `--check` reports the latest
release without installing it.

After an upgrade the new release rewrites the agent skills that `edka skill install`
wrote to the skills directories of coding agents in your home, such as
`~/.claude/skills`. It keeps a skill with files that changed after the install.

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
