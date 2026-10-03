# Edka CLI

The Edka CLI lets you interact with your Edka clusters from the command line. Read the [CLI documentation](https://edka.io/docs/cli).

Sign in through the browser, link a project directory to a cluster or a deployment,
then deploy, follow logs, diagnose failures, and manage clusters, apps and databases.
`edka api` calls any Edka API endpoint that a CLI token can reach.

```console
$ edka login
  Authorize Edka CLI in your browser
  ✓ Signed in to Acme as you@example.com

$ edka link --cluster production --deployment api
  ✓ Linked this directory

$ edka up --wait
  Deployment submitted. Waiting for generation 3…
  Applying generation 3…
  Rolling out: 1/2 updated, 2/2 ready
  Rolling out: 2/2 updated, 2/2 ready
  ✓ api is running generation 3

$ edka logs --follow
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

## Get started

```sh
edka login                      # Sign in through your browser
edka link                       # Pick a cluster, then a deployment, for this directory
edka status                     # The linked deployment, its replicas and pods
edka up --wait                  # Redeploy and follow the rollout
edka logs --follow
edka diagnose                   # What is wrong, and the commands to run next
edka --help                     # Every command
```

`edka <command> --help` shows the flags and examples of a command.

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
