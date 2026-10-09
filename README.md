# Edka CLI

`edka` is the command line for [Edka](https://edka.io). It deploys, follows logs,
diagnoses failures, and manages clusters, apps and databases from a terminal, a
script or a coding agent. `edka api` calls any Edka API endpoint that a CLI token
can reach. The [CLI documentation](https://edka.io/docs/cli) covers every command.

```console
$ edka login
Authorize Edka CLI in your browser
…
✓ Signed in to Acme as you@example.com
  Next: edka link

$ edka link --cluster production --deployment api
✓ Linked this directory
  Cluster: production
  Deployment: api
  Context: /home/you/api/.edka.json
  Next: edka status

$ edka up --wait
Deployment submitted. Waiting for generation 3…
Applying generation 3…
Rolling out: 1/2 updated, 2/2 ready
Rolling out: 2/2 updated, 2/2 ready
✓ api is running generation 3
```

## Install

macOS and Linux:

```sh
curl -fsSL https://edka.io/install.sh | sh
```

Homebrew:

```sh
brew install edkadigital/tap/edka
```

Windows, in PowerShell:

```powershell
irm https://edka.io/install.ps1 | iex
```

Go 1.26 or newer:

```sh
go install github.com/edkadigital/cli/cmd/edka@latest
```

The install scripts check each download against the SHA-256 in the release's
`checksums.txt`. [Install options](https://edka.io/docs/cli/overview/#install-options)
covers pinning a version, choosing the directory, and the release archives.

## Examples

The examples use a deployment named `api` on a cluster named `production`. In a
linked directory, commands default to the linked deployment, so the name is
optional. `edka <command> --help` lists the flags and more examples of a command.

### Deploy a change

`--diff` compares a change with the deployment and sends nothing:

```console
$ edka up api --field config.image_tag=v3 --field config.replicas:=3 --diff
api at generation 5: 2 changes. Nothing was sent.

  config.image_tag   v2 → v3
  config.replicas    2 → 3

To apply this only while api is at generation 5, run the command without --diff and with --expected-generation 5.
```

With `--expected-generation`, Edka refuses the change if the deployment is no
longer at that generation. `--wait` follows the rollout:

```sh
edka up api --field config.image_tag=v3 --field config.replicas:=3 --expected-generation 5 --wait
```

A Git deployment builds from its branch. `--wait` streams the build log and,
with auto-deploy on, follows the rollout of the new image:

```sh
edka build api --wait
```

To go back, list the revisions and restore one:

```sh
edka deployments revisions api
edka rollback api --generation 4
```

### Find out why a deployment fails

`edka diagnose` reads the rollout, the pods, the Kubernetes events and the log of
the failing pod. It prints each problem with the commands to run next, and
changes nothing:

```console
$ edka diagnose api
✗ Pod api-x2k starts and exits, 4 times so far.
  back-off 5m0s restarting failed container
  Next: Read what it printed last, under Logs.
  Next: edka logs api --pod api-x2k --previous --tail 200
  Next: edka rollback api --generation 4

Deployment   api
Cluster      production
Namespace    default
Status       deploying
Image        ghcr.io/acme/api:v3
Revision     generation 5 (applied 5, healthy 4)
Rollout      failed: CrashLoopBackOff: back-off 5m0s restarting failed container
Replicas     1/2 ready, 1 updated, 1 available

POD       STATUS    READY   RESTARTS   REASON
api-7d9   running   true    0          —
api-x2k   failed    false   4          CrashLoopBackOff

WARNING   LAST SEEN          COUNT   OBJECT        MESSAGE
BackOff   2026-10-02 12:00   12      Pod api-x2k   Back-off restarting failed container api

Logs of api-x2k, from the container before its last restart
listening on :8080
panic: DATABASE_URL is not set
Error: api has a problem; see the finding above
```

Without a deployment, `edka diagnose` checks the cluster. Apps, previews,
databases and cron jobs each have a `diagnose` of their own, such as
`edka databases diagnose orders`.

`edka logs` reads the failing pod with the most restarts, or else the newest pod:

```sh
edka logs api --follow
edka logs api --since 15m --timestamps
edka logs api --previous                 # The container before its last restart
```

### Variables and secrets

```console
$ edka env
NAME        VALUE
PORT        8080
LOG_LEVEL   info
API_TOKEN   (secret)
```

Each change starts a rollout. A secret's value comes from a hidden prompt or from
stdin, never from the command line, where shell history would keep it:

```sh
edka env set LOG_LEVEL=debug --wait
edka env set --secret DATABASE_URL
printf %s "$API_TOKEN" | edka env set --secret API_TOKEN
```

### Clusters and kubectl

```sh
edka clusters create staging --wait
edka clusters kubeconfig production --merge --use
edka run --kubeconfig -- kubectl get pods -A
```

`--merge --use` adds the cluster to your kubeconfig and makes it kubectl's current
context. `edka run --kubeconfig` gives one command a kubeconfig that expires after
an hour, and deletes it when the command exits.

An organization can ask for a passkey check before sensitive actions, such as a
kubeconfig download. At a terminal, the command opens the check in the Edka
console and continues after you confirm with your passkey. A command without a
terminal prints the console address and stops, so run `edka verify` before a
script. After you confirm, sensitive actions from the same login work for 5
minutes:

```sh
edka verify
edka clusters kubeconfig production --output-file production.yaml
```

### Apps and databases

```sh
edka apps install umami --set postgres_instance=postgres --set hostname=stats.example.com --wait
edka databases backup orders
edka databases backups orders
```

In a terminal, `apps install` asks for each setting the app needs that `--set`
leaves out.

### Scripts, CI and the API

With `--json`, stdout holds only the result. Progress and errors go to stderr. A
`diagnose` that finds a problem exits with 1:

```sh
edka deployments list --all --json --no-input
edka diagnose api --json | jq '.findings[].next'
```

`edka api` has a command for each API endpoint, and calls a path directly:

```sh
edka api operations --search backup
edka api clusters nodepools list --cluster production
edka api get /api/inventory/resources --query kind=deployment --json
```

## Documentation

| Page | Covers |
| --- | --- |
| [Overview](https://edka.io/docs/cli/overview/) | Install, sign in, link a directory, upgrade, shell completion |
| [Deploy from the terminal](https://edka.io/docs/cli/deploy/) | `up`, `--diff`, `build`, `logs`, `env`, rollbacks, previews |
| [Diagnose problems](https://edka.io/docs/cli/diagnose/) | `diagnose` for deployments, clusters, apps, previews, databases and cron jobs |
| [Clusters and infrastructure](https://edka.io/docs/cli/clusters/) | Clusters, kubeconfig, node pools, domains, registries, secrets |
| [Apps and add-ons](https://edka.io/docs/cli/apps/) | Installing apps from the catalog, add-ons |
| [Databases and cron jobs](https://edka.io/docs/cli/databases-cronjobs/) | Backups, logs, runs |
| [API commands](https://edka.io/docs/cli/api/) | `edka api` for every endpoint |
| [Scripts and CI](https://edka.io/docs/cli/scripting/) | JSON output, exit codes, profiles, environment variables |

## Upgrade

```sh
edka upgrade
```

`edka upgrade` installs the latest release after checking its SHA-256. A Homebrew
install upgrades with `brew upgrade edka`.

## Security

[SECURITY.md](SECURITY.md) says how to report a vulnerability, and
[docs/security.md](docs/security.md) how the CLI handles credentials, tokens and
downloads.

## Development

```sh
make build    # bin/edka
make test     # Tests with the race detector
make check    # Lint, formatting, vet and build
make vuln     # Known vulnerabilities in the code and its Go release
```

`make check` needs [golangci-lint](https://golangci-lint.run/docs/welcome/install/)
v2, and `make vuln` needs the network. CI runs the tests on macOS, Linux and Windows
for each pull request.

[`internal/catalog/operations.json`](internal/catalog/operations.json) lists the
endpoints behind `edka api`. Edka's maintainers generate it from the API's source, so
a change to it comes as a regenerated file, not a hand edit.

See [architecture](docs/architecture.md) and [security](docs/security.md).

## License

MIT. See [LICENSE](LICENSE).
