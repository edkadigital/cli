---
name: edka
description: Use the Edka CLI (`edka`) to deploy, follow rollouts, read logs, find out why something fails, and manage clusters, deployments, apps, add-ons, databases, cron jobs, domains, registries, secrets and node pools on Edka, or to call the Edka API. Use when the user mentions Edka or `edka`, asks to deploy, build, roll back, scale or restart something that runs on Edka, wants its logs or the reason a deployment, app, database or cluster on Edka is failing, or works in a directory with an `.edka.json` link.
---

# Edka CLI

`edka` is the command line for [Edka](https://edka.io). It deploys, reads logs,
diagnoses failures, and manages clusters, apps and databases. `edka <command> --help`
prints the flags and examples of any command, and the
[CLI documentation](https://edka.io/docs/cli/) covers each one.

## Agent guidance

### Context

- A command takes its profile, organization, cluster and deployment from its flags,
  then the `EDKA_*` variables, then the directory link (`.edka.json`, written by
  `edka link`), then the profile. In a linked directory, `edka logs` means the
  linked deployment's logs.
- `edka context` prints what a command in this directory acts on. Run it before a
  change when the target isn't certain.
- Pass `--no-input` in an agent's shell, or set `EDKA_NO_INPUT=1`. A command then
  fails with a message instead of waiting at a prompt or a picker.
- Names work wherever an ID does. A name that matches more than one resource fails
  with an error that lists their IDs; rerun with the ID, or add `--cluster`.

### Sign-in

- The error ``not signed in; run `edka login` `` means there is no sign-in.
  `edka login` opens a browser and waits for the user, so ask the user to run it in
  their own terminal. `edka whoami` shows who is signed in, to which organization.
- `edka login --read-only` signs in without write access, and Edka refuses changes
  made with that sign-in. For an agent that only investigates, the user can run
  `edka login --profile readonly --read-only`, and the agent passes
  `--profile readonly` to each command.
- `edka doctor` checks connectivity, the sign-in and the context when commands fail
  for unclear reasons.

### Reading state

- Add `--json` to anything you parse. Stdout then holds only the result; progress,
  prompts and errors go to stderr. A list keeps the API's envelope, `{"data": [...]}`.
- Tables show selected columns. `--json` prints the full records.
- `edka status` shows the linked cluster and deployment, `edka deployments list --all`
  every deployment.

### Finding out why something fails

- Start with `diagnose`. It reads the rollout, pods, Kubernetes events and the log of
  the failing pod, prints each problem with the commands to run next, and changes
  nothing. `edka diagnose` covers a deployment, or the cluster when there is no
  deployment; `edka apps diagnose`, `edka previews diagnose`,
  `edka databases diagnose` and `edka cronjobs diagnose` cover the other kinds.
- `diagnose` exits with 1 when it finds a problem. That is its answer, not a broken
  command. With `--json`, stdout has `healthy`, `findings` and each finding's `next`
  commands.
- The `Next:` commands repeat the context flags you passed, so they run as printed.
  A `Next:` command that changes something, such as a rollback, still follows the
  safety rules below.
- `edka logs` reads the failing pod with the most restarts, or else the newest pod.
  `--previous` reads the container before its last restart, which holds a crash's
  output.

### Changing things

- Preview first. `edka up --diff`, `edka env set --diff` and `edka env unset --diff`
  print what would change and send nothing. `--dry-run` on `edka clusters create`,
  `edka deployments create`, the `edka nodepools` changes and the endpoint commands
  under `edka api` prints the request instead of sending it.
- `edka up --diff` prints the deployment's generation. Pass it back with
  `--expected-generation` and Edka refuses the change if the deployment changed in
  between.
- `--wait` follows a rollout or build to its end, and fails with the cause when it
  fails. Report what the command printed, such as `✓ api is running generation 3`.
  Without `--wait`, a command only submitted the change.
- `--field key=value` sends a string. `--field key:=JSON` sends a number, boolean,
  list or object, as in `--field config.replicas:=3`.
- `edka build` builds a Git deployment from its branch, and with auto-deploy on,
  `--wait` also follows the rollout of the new image. `edka up` redeploys the last
  successful build and builds nothing.
- Use the command for a resource before `edka api`. When there is none,
  `edka api operations --search <term>` finds the endpoint command, and
  `edka api get /api/...` calls a path directly.

### Safety rules

- Commands that delete, uninstall, revoke, roll back, create a cluster, resize a node
  pool, update add-ons or scale a deployment to zero ask for confirmation, and so do
  the `edka api` requests Edka marks. Without a terminal they fail with
  `<action> requires confirmation; rerun with --yes`. Treat that error as the
  question. Show the user the action it names, and rerun with `--yes` only after
  they agree. Never add `--yes` on your own.
- `edka up`, `edka build`, `edka env set`, `edka env unset`, `edka restart` and
  `edka scale` change a running deployment without asking. Run them only for a
  change the user asked for, on the target `edka context` confirms.
- Secret values never go on the command line, where shell history and the process
  list keep them. `edka env set --secret NAME` and `edka secrets set` read the value
  from stdin: `printf %s "$TOKEN" | edka env set --secret API_TOKEN`. `KEY=value`
  without `--secret` makes a plain variable that `edka env` shows to everyone.
- An organization can ask for a passkey check before sensitive actions, such as a
  kubeconfig download. Without a terminal the command stops with
  `This action needs a passkey check. Approve it at <address>…`, and `--json` adds
  `verify_url`. When Edka sends a code for the Edka app on the user's phone, the
  message names it and `--json` adds `verify_code`. Give the user the address and
  the code, wait until they confirm with their passkey, then run the command again
  within 5 minutes. Only the user can confirm.
- `edka run --kubeconfig -- kubectl …` gives one command a kubeconfig that expires
  after an hour and is deleted when the command exits. Use it for kubectl.
  `edka clusters kubeconfig` issues the user's kubeconfig once and writes it to disk,
  and `--rotate` revokes the previous one.
- `edka logs --follow` polls until Ctrl+C and never ends on its own. Read a window
  with `--since 15m` or `--tail 200` instead.

### Exit codes and errors

| Code | Meaning |
| --- | --- |
| `0` | The command succeeded. |
| `1` | The command failed, or a `diagnose` found a problem. |
| `130` | The command was interrupted with Ctrl+C or SIGTERM. |

`edka run` exits with the code of the command it ran. With `--json`, an error is a
JSON object on stderr with `error` and `exit_code`. An error from the API adds
`status` and `request_id`, and `code`, `reason`, `details` and `fields` when the API
sent them. Each entry of `fields` names an invalid `field` and its `message`.

### Workflows

Deploy a new image tag, after checking the target and the change:

```bash
edka context
edka up api --field config.image_tag=v3 --diff
edka up api --field config.image_tag=v3 --expected-generation 5 --wait
```

Build a Git deployment and follow it out:

```bash
edka build api --wait
```

Find out why a deployment fails, and go back to a working revision once the user
agrees:

```bash
edka diagnose api
edka logs api --previous --tail 200
edka deployments revisions api
edka rollback api --generation 4 --yes
```

Variables and secrets:

```bash
edka env
edka env set LOG_LEVEL=debug --diff
edka env set LOG_LEVEL=debug --wait
printf %s "$API_TOKEN" | edka env set --secret API_TOKEN
```

kubectl against a cluster:

```bash
edka run --kubeconfig --cluster production -- kubectl get pods -A
```

Databases and cron jobs:

```bash
edka databases diagnose orders
edka databases backups orders
edka cronjobs runs cleanup
edka cronjobs logs cleanup
```

An endpoint without a command of its own:

```bash
edka api operations --search backup
edka api clusters nodepools list --cluster production
edka api get /api/inventory/resources --query kind=deployment --json
```

### Common mistakes

- Reading exit code 1 from `diagnose` as a failed command. It found a problem, and
  the findings say which.
- Running `edka up` to build a Git deployment. `edka build` builds; `up` redeploys
  the last successful build.
- `--field config.replicas=3` sends the string `"3"`. A number needs
  `--field config.replicas:=3`.
- Parsing a table. `--json` has the full records and a stable shape.
- Running `edka login` in the agent's shell, where it waits for a browser sign-in
  nobody sees. Ask the user to run it.
- Assuming the linked deployment is the target. `--deployment`, `EDKA_DEPLOYMENT` and
  `edka run` override the link. `edka context` shows the one in effect.

## Install

```bash
curl -fsSL https://edka.io/install.sh | sh
brew install edkadigital/tap/edka
```

On Windows, `irm https://edka.io/install.ps1 | iex` in PowerShell. `edka upgrade`
installs the latest release.

<!-- BEGIN GENERATED: make skill -->
## Command reference

Each reference file has the flags and examples of its commands. `edka <command> --help` prints the same help.

### Sign-in and project link

Sign in, link a directory to a cluster and deployment, and confirm your identity.

- `edka link`: Choose a cluster and deployment for this directory
- `edka login`: Sign in securely through your browser
- `edka logout`: Revoke access and remove saved credentials
- `edka verify`: Confirm your identity with a passkey
- `edka whoami`: Show your identity and authorized organization

Flags and examples: [references/setup.md](references/setup.md)

### Everyday workflows

Deploy, build, read logs, diagnose, change variables, scale, restart and roll back a deployment.

- `edka build [deployment]`: Build a Git deployment from its current branch
- `edka diagnose [deployment]`: Find out why a deployment or cluster is not working
- `edka env`: List and change a deployment's variables and secrets
- `edka env list`: List variables, and secrets by name
- `edka env set KEY=value...`: Set variables, or secrets with --secret
- `edka env unset KEY...`: Remove variables or secrets
- `edka logs [deployment]`: Read deployment logs; follow with --follow
- `edka open`: Open the linked cluster in the Edka console
- `edka restart [deployment]`: Restart a deployment's pods
- `edka rollback [deployment]`: Restore a previous deployment revision
- `edka run -- <command> [args…]`: Run a local command with EDKA_* context variables set
- `edka scale [deployment]`: Set a deployment's replica count
- `edka status`: Inspect your linked cluster and deployment
- `edka up [deployment]`: Deploy image changes or redeploy a successful Git build

Flags and examples: [references/workflows.md](references/workflows.md)

### `edka addons`

Install, update and uninstall add-ons.

- `edka addons catalog`: List add-ons you can install
- `edka addons get [add-on]`: Show an installed add-on
- `edka addons install <add-on>`: Install an add-on from the catalog
- `edka addons list`: List installed add-ons and their updates
- `edka addons uninstall <add-on>`: Uninstall an add-on from its cluster
- `edka addons update [add-on]`: Update add-ons to the catalog version

Flags and examples: [references/addons.md](references/addons.md)

### `edka apps`

Install and manage apps, and publish and share your own.

- `edka apps catalog [app]`: List apps you can install, or an app's settings
- `edka apps custom`: List the custom apps your organization published
- `edka apps diagnose [app]`: Find out why an app is not running
- `edka apps get [app]`: Show an installed app
- `edka apps init <slug>`: Write the starting files of a new custom app
- `edka apps install <app>`: Install an app from the catalog
- `edka apps list`: List installed apps
- `edka apps logs [app]`: Read an app's logs; follow with --follow
- `edka apps options <app> <setting>`: List the choices for an app setting
- `edka apps publish [directory]`: Publish a custom app to your organization's catalog
- `edka apps share <directory or app>`: Offer a custom app to the community catalog
- `edka apps uninstall <app>`: Uninstall an app from its cluster
- `edka apps update <app>`: Move an installed custom or community app to another version
- `edka apps validate [directory]`: Check the files of a custom app

Flags and examples: [references/apps.md](references/apps.md)

### `edka clusters`

Create, inspect, protect and delete clusters.

- `edka clusters create <name>`: Create a cluster on Hetzner
- `edka clusters delete <cluster>`: Delete a cluster, its servers and data
- `edka clusters diagnose [cluster]`: Find out what is wrong with a cluster
- `edka clusters get [cluster]`: Show a cluster and its node pools
- `edka clusters kubeconfig <cluster>`: Download your kubeconfig for a cluster
- `edka clusters list`: List clusters
- `edka clusters protect <cluster>`: Protect a cluster from deletion
- `edka clusters unprotect <cluster>`: Allow a cluster to be deleted

Flags and examples: [references/clusters.md](references/clusters.md)

### `edka cronjobs`

Inspect, run and pause scheduled jobs.

- `edka cronjobs delete <cronjob>`: Delete a cronjob and its Kubernetes resources
- `edka cronjobs diagnose [cronjob]`: Find out why a cronjob's runs fail or don't start
- `edka cronjobs get [cronjob]`: Show a cronjob and its last run
- `edka cronjobs list`: List cronjobs
- `edka cronjobs logs [cronjob]`: Read a run's logs; the newest run by default
- `edka cronjobs resume <cronjob>`: Restart scheduled runs
- `edka cronjobs runs [cronjob]`: List recent runs, newest first
- `edka cronjobs suspend <cronjob>`: Stop scheduled runs
- `edka cronjobs trigger <cronjob>`: Start a run now

Flags and examples: [references/cronjobs.md](references/cronjobs.md)

### `edka databases`

Inspect, back up and delete databases.

- `edka databases backup <database>`: Start a backup now
- `edka databases backups [database]`: List a database's backups, newest first
- `edka databases delete <database>`: Delete a database and its data
- `edka databases diagnose [database]`: Find out what is wrong with a database
- `edka databases get [database]`: Show a database, its instances and recovery window
- `edka databases list`: List databases
- `edka databases logs [database]`: Read a database's logs; follow with --follow

Flags and examples: [references/databases.md](references/databases.md)

### `edka deployments`

Inspect and manage deployments.

- `edka deployments build [deployment]`: Build a Git deployment from its current branch
- `edka deployments create`: Create a deployment from an image or a GitHub repository
- `edka deployments delete <deployment>`: Delete a deployment and its Kubernetes resources
- `edka deployments diagnose [deployment]`: Find out why a deployment is not running
- `edka deployments env`: List and change a deployment's variables and secrets
- `edka deployments env list`: List variables, and secrets by name
- `edka deployments env set KEY=value...`: Set variables, or secrets with --secret
- `edka deployments env unset KEY...`: Remove variables or secrets
- `edka deployments get [deployment]`: Show a deployment's configuration
- `edka deployments list`: List deployments
- `edka deployments logs [deployment]`: Read deployment logs; follow with --follow
- `edka deployments restart [deployment]`: Restart a deployment's pods
- `edka deployments revisions [deployment]`: List revisions to roll back to
- `edka deployments rollback [deployment]`: Restore a previous deployment revision
- `edka deployments scale [deployment]`: Set a deployment's replica count
- `edka deployments status [deployment]`: Show runtime status, replicas and pods

Flags and examples: [references/deployments.md](references/deployments.md)

### `edka domains`

Add domains, issue their certificates and delete them.

- `edka domains add <domain>`: Add a domain to a cluster
- `edka domains delete <domain>`: Delete a domain and its certificate
- `edka domains get [domain]`: Show a domain, its certificate and the DNS records it needs
- `edka domains list`: List domains and their certificates
- `edka domains update <domain>`: Change a wildcard's apex or a domain's Cloudflare token
- `edka domains verify <domain>`: Check a domain's DNS record and issue its certificate

Flags and examples: [references/domains.md](references/domains.md)

### `edka nodepools`

List, add, scale and delete node pools.

- `edka nodepools add <name>`: Add a node pool to a cluster
- `edka nodepools delete <pool>`: Delete a node pool and its servers
- `edka nodepools get [pool]`: Show a node pool, its labels and taints
- `edka nodepools list`: List node pools
- `edka nodepools scale <pool>`: Change how many servers a node pool has

Flags and examples: [references/nodepools.md](references/nodepools.md)

### `edka previews`

List, inspect and delete pull request previews.

- `edka previews delete <pr>`: Delete a preview and what runs for it
- `edka previews diagnose [pr]`: Find out why a preview is not running
- `edka previews list [deployment]`: List a deployment's previews
- `edka previews logs [pr]`: Read a preview's logs; follow with --follow
- `edka previews open [pr]`: Open a preview in the browser
- `edka previews status [pr]`: Show a preview, its replicas and pods

Flags and examples: [references/previews.md](references/previews.md)

### `edka registries`

Store registry credentials and browse a cluster's registry.

- `edka registries add <name>`: Store the credentials of a registry
- `edka registries apply <registry>`: Let a cluster pull from a registry
- `edka registries default [registry]`: Show or set a cluster's default registry
- `edka registries delete <registry>`: Delete a registry's credentials
- `edka registries delete-tags <repository> <tag>...`: Delete tags from a cluster's own registry
- `edka registries get <registry>`: Show a registry and the clusters that use it
- `edka registries images`: Show a cluster's own registry and the images it holds
- `edka registries list`: List the organization's registries
- `edka registries remove <registry>`: Take a registry from a cluster
- `edka registries tags <repository>`: List the tags of a repository in a cluster's own registry

Flags and examples: [references/registries.md](references/registries.md)

### `edka secrets`

Create, change and delete Kubernetes secrets in a cluster.

- `edka secrets delete <secret>`: Delete a secret
- `edka secrets get <secret>`: Show a secret's keys, type and age
- `edka secrets list`: List the secrets Edka manages, with their keys
- `edka secrets set <secret> [KEY...]`: Create a secret, or set keys of one
- `edka secrets unset <secret> KEY...`: Remove keys from a secret

Flags and examples: [references/secrets.md](references/secrets.md)

### `edka trusts`

Let GitHub Actions workflows sign in without a secret.

- `edka trusts create <name>`: Let a GitHub Actions workflow publish and update one app
- `edka trusts delete <trust>`: Stop a workflow from signing in
- `edka trusts get <trust>`: Show a trust and the job settings that use it
- `edka trusts list`: List the workflows that may sign in

Flags and examples: [references/trusts.md](references/trusts.md)

### Tools

Context, profiles, health checks, shell completion, upgrades and the agent skills of the CLI.

- `edka completion <bash|zsh|fish|powershell>`: Generate shell completions
- `edka context`: Show effective profile and project context
- `edka doctor`: Check CLI connectivity, authentication and context
- `edka profile list`: List profiles without secrets
- `edka profile remove <name>`: Remove a signed-out profile
- `edka profile use <name>`: Set the default profile
- `edka skill install [skill...]`: Install the agent skills for Claude Code and other agents
- `edka unlink`: Remove this directory's project context
- `edka upgrade`: Install the latest release of the CLI
- `edka version`: Show the version, commit and API revision of this binary

Flags and examples: [references/tools.md](references/tools.md)

### `edka api`

Call any Edka API endpoint, by command or by path.

- `edka api`: Call any Edka API endpoint, by command or by path
- `edka api delete <path>`: DELETE an Edka API path
- `edka api get <path>`: GET an Edka API path
- `edka api operations`: Search every endpoint command
- `edka api patch <path>`: PATCH an Edka API path
- `edka api post <path>`: POST an Edka API path
- `edka api put <path>`: PUT an Edka API path

Flags and examples: [references/api.md](references/api.md)

## Global flags

Every command takes these flags. A variable in parentheses sets the same value.

- `--api-url string`: API origin (EDKA_API_URL)
- `--cluster string`: Cluster ID or name (EDKA_CLUSTER)
- `--color string`: Terminal color: auto, always, never (default "auto")
- `--credential-store string`: Credential storage: auto, keyring, file (EDKA_CREDENTIAL_STORE) (default "auto")
- `--debug`: Print each request to stderr (EDKA_DEBUG)
- `--deployment string`: Deployment ID or name (EDKA_DEPLOYMENT)
- `--json`: Print machine-readable JSON
- `--no-input`: Never prompt (EDKA_NO_INPUT; also enabled in CI)
- `--organization string`: Authorized organization ID (EDKA_ORGANIZATION)
- `--output string`: Output format: table, json, raw (default "table")
- `--profile string`: Saved profile (EDKA_PROFILE)
- `--timeout duration`: HTTP request timeout (default 30s)
- `--trust string`: Sign in from GitHub Actions with this trust (EDKA_TRUST)
- `-y, --yes`: Confirm destructive operations
<!-- END GENERATED -->
