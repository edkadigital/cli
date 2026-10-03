# Edka CLI

The Edka CLI lets you interact with your Edka clusters from the command line. Read the [CLI documentation](https://edka.io/docs/cli).


`edka` is the command line for [Edka](https://edka.io). Sign in through the browser,
link a directory to a cluster or a deployment, then deploy, follow logs, diagnose
failures and manage clusters, apps and databases from the terminal. `edka api` calls
any Edka API endpoint that a CLI token can reach.

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

Homebrew, on macOS and Linux:

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

The install scripts download the archive of the latest release for your system and
check it against the SHA-256 in the release's `checksums.txt`. `install.sh` puts
`edka` in `~/.local/bin`, and says how to add it to `PATH` when it isn't there.
`install.ps1` puts `edka.exe` in `%LOCALAPPDATA%\Programs\edka` and adds that
directory to your `PATH`. Both read three variables:

| Variable | Sets |
| --- | --- |
| `EDKA_VERSION` | A release to install, such as `v1.2.3`, instead of the latest |
| `EDKA_INSTALL_DIR` | The directory of the binary. `install.ps1` then leaves `PATH` alone |
| `EDKA_RELEASES_URL` | A mirror of `https://github.com/edkadigital/cli/releases` |

```sh
curl -fsSL https://edka.io/install.sh | EDKA_VERSION=v1.2.3 EDKA_INSTALL_DIR="$HOME/bin" sh
```

Each [release](https://github.com/edkadigital/cli/releases/latest) also has the
archives, named `edka_<version>_<os>_<arch>`, for macOS (`darwin`), Linux and
Windows on `amd64` and `arm64`, and `checksums.txt` with the SHA-256 of each.

### Build from source

Requires Go 1.26 or newer.

```sh
git clone https://github.com/edkadigital/cli.git
cd cli
make install
edka version
```

`make install` installs to Go's binary directory (`go env GOPATH` + `/bin` unless
`GOBIN` is set). Add that directory to `PATH`. `make build` creates `bin/edka`.

## Upgrade

```sh
edka upgrade            # Install the latest release
edka upgrade --check    # Report the latest release and install nothing
```

`upgrade` replaces the binary with the latest release on
[GitHub](https://github.com/edkadigital/cli/releases). It downloads the archive for the
system and architecture of the running binary and checks it against the SHA-256 in
the release's `checksums.txt`. A download that does not match replaces nothing. The
command writes the new binary into the directory of the old one, so it needs write
access there. On Windows the old binary stays as `edka.exe.old` until the next
upgrade. It skips pre-releases, such as `v1.3.0-rc.1`. With `--json`, stdout has
`version`, `latest`, `update_available` and `updated`.

Homebrew keeps track of the version it installed, so upgrade a binary from Homebrew
with `brew upgrade edka`. `edka upgrade` names that command and replaces nothing.

A released binary looks for a newer release once a day, while a command runs at a
terminal. When it finds one, it prints a line on stderr after the command's output,
with the new version and the command that installs it. The lookup is one request to github.com without a token,
and a command waits up to 2 seconds for it. It installs nothing. It does not run in
CI, in a script or a pipe, or with `EDKA_NO_UPDATE_CHECK=1`.

A binary built from a checkout has the version `dev`. It looks for no release, and
`upgrade` does not replace it. `go install …@v1.2.3` builds the release `v1.2.3`,
and `go install …@main` builds `dev`.

## Start working

```sh
edka login                              # Browser OAuth with explicit consent
edka login --read-only                  # Omit write permissions
edka whoami                             # Identity and authorized organization
edka link                               # Pick a cluster, then optionally a deployment
edka link --cluster production --deployment api
edka status                             # Linked cluster or deployment status
edka build --wait                       # Build the branch, stream its log, follow auto-deploy
edka up --wait                          # Redeploy the latest successful Git build
edka up api --field config.image_tag=v3 --diff   # What a change would alter; sends nothing
edka logs --follow --tail 200
edka diagnose                           # What is wrong with the linked deployment, or else the cluster
edka env                                # Variables, and secrets by name
edka env set LOG_LEVEL=debug --wait     # Change one, keep the rest
edka restart api                        # Shortcut for `edka deployments restart`
edka scale api --replicas 3
edka rollback api --generation 4 --yes
edka open                               # Open this cluster in the console
```

Create a deployment from a JSON body:

```sh
edka deployments create --example > deployment.json     # A body to start from
edka deployments create --schema                        # Every field, as JSON Schema
edka deployments create --data @deployment.json --link --wait
edka deployments create --git --example                 # For a GitHub repository that Edka builds
```

`deployments create` creates a deployment in the linked or selected cluster. The body
names a container image, or with `--git` a GitHub repository that Edka builds.
`--example` and `--schema` print what the binary holds and send no request. `--link`
links this directory to the new deployment. `--wait` follows the first rollout, and
with `--git` the first build before it. `--dry-run` prints the request and sends
nothing. With `--json`, stdout has the created deployment. With `--wait` it has the
runtime status of an image deployment, or the first build of a Git deployment.
`edka up --data @deployment.json` also creates an image deployment, in a directory
that links none.

`edka build api --wait` builds the deployment's branch and streams the build log and
each build step to stderr. With auto-deploy on, the default, it then waits for the
rollout of the new image, so the command ends when the build runs. With auto-deploy
off, deploy the build with `edka up api --wait`.

`up [deployment]` redeploys the latest successful Git build. With `--data` or `--field`,
it changes the deployment's image or settings. Without changes, it restarts an image
deployment. `--wait` waits until the submitted generation is applied and
healthy and runtime status reports it deployed. Meanwhile it prints to stderr how many
pods are updated and ready, and each pod that fails with its reason. It exits
unsuccessfully when the generation fails, when a later generation such as an automatic
rollback replaces it, or when the wait times out. The error names the failure reason.

`up --diff` compares the settings it would send with the deployment, and sends
nothing:

```console
$ edka up api --data @settings.json --diff
api at generation 5: 3 changes. Nothing was sent.

  config.env_variables   + LOG_LEVEL=debug
                         ~ PORT: 8080 → 9090
                         - OLD_FLAG
  config.image_tag       v2 → v3
  secret_values          ~ API_KEY (values not shown)

Unchanged: config.port
Ignored: image_tag: the settings route does not read it; send config.image_tag
```

A settings request replaces each list it sends. So for `config.env_variables`,
`config.secrets`, `config.volumes` and the hostnames, the diff lists each entry the
request adds, changes or removes. A request that sets `config.expose_via_ingress` to
`false` removes every hostname of the deployment, and the diff lists them. It shows a
secret by name and never a value. `Unchanged` lists the fields sent with the value
they have. `Ignored` lists the fields that have no effect, such as one the settings
route does not read, or a value in `secret_values` for a name that is not among the
deployment's secrets. `Refused` lists what Edka refuses the request for: a name or
namespace other than the deployment's, an exposed deployment with no hostname, or a
new secret with no value in `secret_values`. With `--json`, stdout has `generation`,
`changes`, `unchanged`, `ignored` and `refused`.

`--expected-generation 5` sends the change with the generation the diff printed. Edka
refuses it when the deployment is at another generation, so the change that is
applied is the one that was compared.

Log following polls the selected pod; it prints new overlapping lines, and a new
snapshot when the log window or pod changes. It is not a durable log stream.

`diagnose [deployment]` reads a deployment's rollout, pods, Kubernetes events, last
revisions and the log of its failing pod. It reports what is wrong, with the commands
to run next:

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

Logs of api-x2k, from the container before its last restart
listening on :8080
panic: DATABASE_URL is not set
```

It finds these causes:

- an image the cluster can't pull, or an image name Kubernetes refuses;
- a container that exits, or that is killed for its memory limit;
- a Secret or ConfigMap the pod names and the cluster lacks;
- a pod that no node can run, and a volume that can't be mounted;
- a health check that fails;
- a change that failed and that Edka rolled back, is rolling back, or could not roll
  back;
- a failed last build of a Git deployment;
- a cluster Edka can't read.

The log is the failing pod's, from the container before its last restart when it
restarted. `--tail` sets its length, 30 lines by default. A pod whose container never
started has no log, so none is read. A health check that fails while the rollout
still runs is a note, since it may pass once the container is up. A warning is a
finding while the pod it names is not ready, or while a ReplicaSet can't create its
pods. A warning from an earlier rollout is listed in the table and is no finding.

`diagnose` changes nothing. It exits 1 when it finds a problem, after the report.
When it can't read a source, such as the events of a cluster that does not answer, it
names the source on stderr and reports the rest. With `--json`, stdout has `healthy`,
`findings`, `deployment`, `runtime`, `revisions`, `events`, `build`, `logs` and
`unread`. Each finding has `problem`, `summary`, `detail` and `next`. A `--wait` that
fails names `edka diagnose` in its error. A failed `apps install --wait` or
`apps update --wait` names `edka apps diagnose`, and a failed `clusters create --wait`
names `edka clusters diagnose`.

Without a deployment as an argument, in `--deployment` or in the link, `edka diagnose`
diagnoses the cluster in context, as `edka clusters diagnose` does.
`edka deployments diagnose` takes a deployment in every case. Clusters, apps,
previews, databases and cronjobs have a `diagnose` of their own, described under
[Resources](#resources).

## Variables and secrets

```sh
edka env
edka env set PORT=8080 LOG_LEVEL=debug --wait
edka env set --secret DATABASE_URL SESSION_KEY
printf %s "$API_TOKEN" | edka env set --secret API_TOKEN
edka env unset LOG_LEVEL
edka env set PORT=9090 DEBUG=1 --diff   # What the change would alter; sends nothing
```

`edka env` shows the variables of the linked deployment, or of the one `--deployment`
names, and lists its secrets by name only. `set` and `unset` change the keys they name
and keep the rest. Each change starts a rollout, and `--wait` follows it the way
`up --wait` does. When every value is already set, `set` sends no request, and
with `--json` it prints `"changed": false`, the deployment ID and the keys. `unset`
fails without a change when a key is not set.

When the deployment is saved elsewhere between the command's read and its save, Edka
refuses the change. `set` and `unset` then read the lists again and send the change
once more, so the keys of both saves stay. After three refusals the command stops
and nothing is saved.

`set --secret` reads each value at a hidden prompt. When stdin is not a terminal, it
reads one value from stdin and drops the final newline. A value on the command line
is refused, because shell history keeps it and other local users can read it in the
process list. Setting a secret with the name of a variable moves the variable into
secrets. A secret becomes a variable only after `unset`, so a hidden value is never
shown by accident.

`set --diff` and `unset --diff` compare the change with the deployment and send
nothing, as `up --diff` does:

```console
$ edka env set PORT=9090 DEBUG=1 --diff
api at generation 5: 1 change. Nothing was sent.

  config.env_variables   + DEBUG=1
                         ~ PORT: 8080 → 9090
```

A secret is shown by name. `set --secret --diff` reads no value, at a prompt or from
stdin. The checks of the change still run, so `unset --diff` fails for a key that is
not set.

Names use letters, digits and underscores, and start with a letter or underscore.
`edka up --field config.env_variables:=[...]` sends a complete list, which replaces
every stored variable.

## Resources

```sh
edka clusters create staging --wait --merge-kubeconfig --use
edka clusters list
edka clusters get production            # Summary, control plane and node pools
edka clusters diagnose production       # What is wrong, and the commands to run next
edka clusters kubeconfig production --merge --use   # Add it to ~/.kube/config and switch to it
edka clusters kubeconfig production --output-file ~/.kube/production.yaml
edka run --cluster production --kubeconfig -- kubectl get nodes
edka clusters protect production
edka clusters delete staging

edka nodepools list                     # Node pools of the linked cluster, or of every cluster
edka nodepools get workers              # Servers, autoscaling, labels, taints and what is pinned to it
edka nodepools add workers --type cx33 --nodes 2 --wait
edka nodepools scale workers --nodes 4 --wait
edka nodepools scale burst --min 1 --max 8   # An autoscaling pool takes a range
edka nodepools delete workers

edka domains list                       # Domains and the state of their certificates
edka domains add '*.example.com' --apex # A wildcard, with example.com on its certificate
edka domains get '*.example.com'        # The certificate and the DNS records to create
edka domains verify '*.example.com' --wait   # Check the DNS record, then issue the certificate
edka domains add app.example.com        # A hostname, validated over HTTP
edka domains delete app.example.com

edka registries add ghcr --type github-registry --url ghcr.io --username octocat
edka registries apply ghcr              # Let the linked cluster pull from it
edka registries default ghcr            # The registry Git deployments use when they name none
edka registries list                    # The organization's registries, and how the cluster uses them
edka registries images                  # The cluster's own registry and its repositories
edka registries tags api
edka registries delete-tags api v1 v2

edka secrets list                       # The secrets Edka manages, with their keys
edka secrets set database PASSWORD --namespace shop   # Create it, or set a key; the value is asked
edka secrets set tls-ca --from-file ca.crt=./ca.crt
edka secrets get database --namespace shop
edka secrets unset database OLD_PASSWORD --namespace shop
edka secrets delete database --namespace shop

edka apps list                          # Installed apps in the linked cluster, or everywhere
edka apps list --all --json
edka apps get strapi
edka apps logs strapi --follow
edka apps diagnose strapi               # Why the app is not running
edka apps catalog --category databases
edka apps install excalidraw --wait     # Install an app from the catalog
edka apps uninstall onlinetest

edka apps init memos --image ghcr.io/usememos/memos --tag 0.25.1 --port 5230 --with access,storage
edka apps validate ./memos              # Each finding names the file, the line and a fix
edka apps publish ./memos               # Store a version in your organization's catalog
edka apps custom                        # The custom apps your organization published
edka apps update memos --wait           # Move an installed app to its latest version
edka apps share ./memos                 # Offer the app to the community catalog, as a pull request

edka addons list                        # Installed add-ons, their versions and updates
edka addons list --outdated
edka addons get cert-manager
edka addons update cert-manager --wait  # Update to the catalog version
edka addons update --all --wait         # Update every add-on that has an update
edka addons install reflector --wait
edka addons uninstall reflector
edka addons catalog --category security

edka deployments list
edka deployments create --data @deployment.json --wait   # `--example` prints a body
edka deployments get api
edka deployments status api             # Replicas, running image and pods
edka deployments diagnose api           # What is wrong, and the commands to run next
edka deployments logs api --pod api-7d9f --previous
edka deployments revisions api
edka deployments rollback api --generation 4
edka deployments delete api

edka databases list
edka databases get orders               # Instances, primary and recovery window
edka databases diagnose orders          # Instances, archiving and backups that fail
edka databases backup orders
edka databases backups orders
edka databases logs orders --follow
edka databases delete orders

edka cronjobs list
edka cronjobs runs nightly-report
edka cronjobs diagnose nightly-report   # Why the last run failed, or why none starts
edka cronjobs trigger nightly-report
edka cronjobs logs nightly-report --run nightly-report-29311
edka cronjobs suspend nightly-report
edka cronjobs resume nightly-report

edka previews list                      # Pull request previews of the linked deployment
edka previews status 12                 # The preview of pull request 12, its replicas and pods
edka previews diagnose 12               # Why the preview is not running
edka previews logs 12 --follow
edka previews open 12                   # Open its URL in the browser
edka previews delete 12 --deployment api
```

Resources are found by name: clusters, node pools, domains, registries, secrets,
deployments, databases, cronjobs and add-ons by their name, apps by the name the console shows or their instance or release name. A
linked cluster narrows lists and lookups; pass `--cluster` for another one, or
`--all` to list every cluster. When a name matches more than one resource, a
terminal shows a picker and scripts get an error that lists the matching IDs.

Commands that change something (`delete`, `install`, `update`, `uninstall`, `trigger`,
`suspend`, `backup`, `protect`) need the name as an argument. Deletes ask for confirmation;
deleting a cluster or database asks you to type its name. Tables show the useful
columns; `--json` prints the full records as `{"data": [...]}`. Logs accept `--pod`,
`--container` and `--previous`; by default Edka picks the failing pod with the most
restarts, or else the newest one.

A Git deployment with previews on gets a preview for each open pull request. The
`previews` commands read those of the linked deployment, or of the one `--deployment`
names, and name a preview by its pull request number, written `12` or `#12`.
`previews list [deployment]` shows each preview's status, branch, author, URL and
expiry, and `--deleted` includes deleted ones. `previews status` shows the preview as
Edka records it and what runs for it in the cluster. A preview that is still building,
or whose build failed, runs nothing yet: the command then shows the record with its
error and says on stderr what it could not read. With `--json`, the record has a
`runtime` field, null in that case. `previews logs` takes the flags of `edka logs`.
`previews open` prints the URL to stderr and opens it in the browser; with `--json` or
without a terminal it prints `{"url": ...}` and opens nothing. `previews delete` asks
for confirmation, or takes `--yes`.

Clusters, apps, previews, databases and cronjobs each have a `diagnose`. It reads what
Edka records and what runs in the cluster, and prints its findings with the commands
to run next, then what it read. Like `edka diagnose`, it changes nothing, exits 1 when it
finds a problem, and names on stderr a source it can't read. With `--json`, stdout has
`healthy`, `findings`, `unread` and what was read.

`clusters diagnose [cluster]` reads whether Edka reaches the cluster's Kubernetes API,
Edka's last check of the node pools, the status of each deployment, the pods that fail
and the warnings Kubernetes recorded:

```console
$ edka clusters diagnose production
✗ Node production-pool-workers-worker2 is not ready.
  Next: edka nodepools list --cluster production
  Next: edka api clusters drift repair-plan get production
✗ Deployment api is failing.
  CrashLoopBackOff: back-off 5m0s restarting failed container
  Next: edka diagnose api --cluster production
✗ Pod kube-system/coredns-1 is failing.
  coredns: ImagePullBackOff

Cluster        production
Status         active
Provider       hetzner
Location       fsn1
Kubernetes     v1.33.1+k3s1
Connectivity   connected
Workers        3/3
Node pools     drifted, checked 2026-10-02 11:00
Deployments    1 deployed, 1 failed

NAMESPACE     POD           PROBLEM    RESTARTS   MESSAGE
prod          api-7d9-x2k   crashing   12         app: CrashLoopBackOff - back-off 5m0s restarting failed container
kube-system   coredns-1     error      0          coredns: ImagePullBackOff
```

It finds these causes:

- a cluster that failed, with the last message Edka recorded for it;
- a Kubernetes API that Edka can't reach, with what Edka found when it investigated
  and the action it suggests;
- a node that is not ready, a server that did not join, and a pool with more or fewer
  nodes than it is set to;
- a deployment that fails or that the cluster lacks, with `edka diagnose` for it as
  the next command;
- a pod that crashes, can't start, is pending or failed.

The failing pod of a deployment that fails is part of that deployment's finding. Five
other pods get a finding each, one more finding counts the rest, and the table lists
up to 20. A report that finds no problem says `No problem found in what could be read`
when a source can't be read. With `--json`, what was read is in `cluster`, `activity`,
`connectivity`, `drift`, `deployments`, `pods` and `events`. `activity` holds Edka's
last five messages about a cluster that is not active.

`apps diagnose [app]` reads the installed app, its pods, and the events and the log of
its failing pod. It finds the causes that `edka diagnose` finds in a deployment's
pods, an install or update that failed, with the message Edka recorded, and an
uninstall that failed. The log is the one of the container that fails, which can be
an init container. The pod of a job that finished, such as the one that installed the
app, is in the table and is no finding. While Edka installs or updates the app, a pod
that is not ready yet is a note. `--tail` sets the length of the log. With `--json`,
what was read is in `app`, `pods`, `events` and `logs`.

`previews diagnose [pr]` reads the preview, its rollout, pods, events and the log of
its failing pod. It finds the causes that `edka diagnose` finds in a deployment's
pods, and a preview that failed, with the error Edka recorded. A preview that is still
building, or that failed, runs nothing, so the command reads no events or pods for it.
`--tail` sets the length of the log. With `--json`, what was read is in `preview`,
`runtime`, `pods`, `events` and `logs`.

`databases diagnose [database]` reads the database, its instances, the conditions and
warnings its operator reports, and its backups. It finds an instance that is not
ready, a write-ahead log the database does not archive, a last backup that failed and
a database that Edka failed to provision. It prints the log of the first instance that
is not ready, `--tail` lines of it. While Edka still provisions the database, the
findings are notes. With `--json`, what was read is in `database`, `runtime` and
`logs`.

`cronjobs diagnose [cronjob]` reads the cronjob, its last five runs and its events. It
finds a last run that failed, with the warning Kubernetes recorded for it and its log,
a run that Kubernetes could not start, a run whose pod Kubernetes can't create, a run
that no node can run, and a change to the cronjob that failed. A suspended schedule and a cronjob with no run yet are notes.
With `--json`, what was read is in `cronjob`, `runtime`, `runs`, `events` and `logs`.

`addons list` shows each installed add-on with its version. The UPDATE column names
the catalog version when it is newer than the installed one, and `--outdated` lists
only those add-ons. `addons update` moves an add-on to the catalog version, or to
`--version`. It shows both versions and asks for confirmation, or `--yes`, and the
add-on keeps its configuration. When the add-on already runs the catalog version,
`update` sends no request. `addons install` installs the catalog version and the
add-ons it depends on, and retries an add-on that failed.

`addons update --all` updates every add-on in the linked or selected cluster that
has a newer catalog version. It lists them with both versions and asks once. An
add-on updates after the add-ons it depends on. With `--wait`, each update finishes
before the next starts, and a failure stops the rest and names them. The command
names the add-ons it leaves out and why: one that failed or is mid-operation, and
one the console manages with a newer catalog version.

`install`, `update` and `uninstall` take `--wait`, which prints progress to stderr
until the operation ends, for up to `--wait-timeout` per add-on. A failed operation
exits unsuccessfully with the reason Edka recorded. `addons uninstall` refuses a
required add-on, one that another installed add-on depends on, and one that is
still installing or updating. Edka also refuses while an app, a database or a
domain uses the add-on, and the error names what uses it.

The console manages six add-ons from other cluster views: Zot from Registries, the
GitHub Actions runner controller from Actions, and Envoy Gateway, the Tailscale
operator, MetalLB and the Cloudflare connector from Gateway. `edka addons` lists
them, and those views install, update and uninstall them. With `--json`, each add-on
also carries `catalog_version`, `update_available`, `category`, `namespace`,
`required`, `depends_on`, `dependents` and `managed_from`.

`clusters create` creates a Hetzner cluster with the console's defaults: deletion
protection on, a daily etcd backup between 00:00 and 06:00 UTC, k3s at the version
Edka recommends, and one node pool. Flags cover the location, control plane, HA and
the default pool; `--data` and `--field` set any other request field. It shows the
estimated monthly price and asks for confirmation, or `--yes`; `--dry-run` prints the
request instead. `--wait` prints provisioning progress until the cluster is active,
and `--merge-kubeconfig` then merges your kubeconfig as described below.

`nodepools add` adds a Hetzner Cloud node pool to the linked or selected cluster, in
the cluster's location unless `--location` names another. `--nodes` sets how many
servers it has, and `--min` with `--max` makes it autoscale between them instead.
`--label key=value` and `--taint key=value:effect` set node labels and taints, with
the effect `NoSchedule`, `PreferNoSchedule` or `NoExecute`.

`nodepools scale` sets the servers of a pool with `--nodes`. Edka creates the
servers a larger pool needs. For a smaller pool it drains the servers it removes,
then deletes them. An autoscaling pool takes `--min` and `--max` instead. On a pool
with a set number of servers, `--min` and `--max` turn autoscaling on, and it stays
on for the life of the pool. When the pool already has the size, `scale` sends no
request.

`nodepools delete` drains the servers of a pool, then deletes them. Edka refuses
while a deployment, an app, a database or another resource is pinned to the pool,
and the error names them. `nodepools get` lists them under "Pinned to it".

`add`, `scale` and `delete` show what they change and ask for confirmation, or
`--yes`; `--dry-run` prints the request instead. `--wait` prints the cluster's events
to stderr until the pool has its servers, or until they are deleted, for up to
`--wait-timeout`. Edka applies one node pool change to a cluster at a time. It
refuses a change while another one runs, and one made from pools that changed after
the command read them. A cluster with a Hetzner Metal node pool changes its node
pools in the console.

`domains add` adds a hostname or a wildcard to the linked or selected cluster, on the
traffic class `--class` names or on the cluster's default one. Let's Encrypt
validates a wildcard over DNS. It validates a hostname over HTTP, which needs a
public traffic class, or over DNS with `--validation dns-01`. `--apex` puts the
hostname a wildcard is under on its certificate: `example.com` for `*.example.com`.
Quote a wildcard, so the shell leaves the `*` alone.

`domains get` reads the certificate from the cluster and lists the DNS records the
domain needs: the CNAME that validates a certificate over DNS, and an A, AAAA or
CNAME record for each address of the traffic class. `domains add` prints the same
records.

`domains verify` checks the CNAME of a domain validated over DNS. With the record in
place Edka issues the certificate, and without it Edka keeps checking. `--wait`
prints the DNS and certificate status to stderr until the certificate is ready, for
up to `--wait-timeout`. A hostname validated over HTTP needs no `verify`: Edka
issues its certificate once the hostname reaches the cluster.

`domains update --apex` includes the apex of a wildcard, and `--apex=false` leaves it
out. `domains delete` deletes a domain with its certificate and its HTTPS listener,
after confirmation or with `--yes`.

A registry is the credentials of a container registry, stored for the organization.
`registries add` stores them for Docker Hub, GitHub, Google Artifact Registry,
Amazon ECR or any other registry. It reads the password or token at a hidden
prompt, or from stdin when stdin is not a terminal, and never from an argument.
`--replace` stores new credentials for a registry that exists. Flags left out keep
their values, and the clusters that use the registry get the new credentials.

`registries apply` lets the linked or selected cluster pull from a registry: Edka
creates its pull secret in every namespace. `registries remove` deletes those
secrets, after confirmation or with `--yes`, and keeps the credentials.
`registries default` shows the cluster's default registry, which a Git deployment
builds to and pulls from when it names none. With a registry it sets the default,
with `--own` it sets the cluster's own registry, and with `--none` it leaves the
cluster without one.

`registries list` lists the organization's registries. With a cluster, the IN
CLUSTER column says how that cluster uses each one. `registries delete` deletes the
credentials, and refuses a registry a cluster still uses, naming the clusters.

`registries images` shows the registry that runs in a cluster: its address inside
the cluster, its storage, and each repository with its number of tags.
`registries tags` lists the tags of a repository. `registries delete-tags` deletes
tags, after confirmation or with `--yes`. The registry deletes an image with every
tag it has, so Edka refuses when a tag you did not name shares an image with one
you named, and `--force` deletes those tags too.

`edka secrets` manages the Kubernetes secrets Edka created in a cluster, which a
deployment, an app or a cronjob reads. A secret lives in a namespace: `--namespace`
names it, and without it the commands use `default`. `secrets list` and
`secrets get` show the keys of a secret and never a value, and Edka leaves alone
the secrets it did not create.

`secrets set` sets keys of a secret and keeps the others. It creates the secret
when the namespace has none of that name. Each value is read at a hidden prompt,
or one value from stdin when stdin is not a terminal, and `--from-file KEY=path`
takes the content of a file. A value on the command line is refused, as for
`env set --secret`. `secrets unset` removes keys, and fails without a change when
a key is not set. `secrets delete` asks for confirmation, or takes `--yes`. A
secret looked for in the wrong namespace names the namespace that has it. A pod
that reads a secret as environment variables gets a new value when it restarts.

`clusters kubeconfig` downloads your own kubeconfig, either to a new private file
with `--output-file` or into the kubeconfig kubectl uses with `--merge`. Edka issues
it once, so the command checks the destination before it downloads: a new file
must not exist yet, and a merge target must parse and have no entry of the same
name from another tool. `--merge` names the cluster, context and user
`edka-<organization>-<cluster>`, replaces what an earlier merge wrote for the same
cluster, and keeps the rest of the file. It writes to the first file in
`KUBECONFIG` that exists, or to `~/.kube/config`, and keeps the previous version
with an `.edka-backup` suffix. `--use` switches kubectl to the new context. If the
merge fails after the download, the kubeconfig is saved to a new private file
next to the target.

`--rotate` revokes your previous kubeconfig and downloads a new one, after you
confirm or pass `--yes`: `edka clusters kubeconfig production --rotate --merge`.

For a single command, `run --kubeconfig` passes a kubeconfig that expires after an
hour and leaves the one-time download available.

The top-level `restart`, `scale`, `rollback`, `build`, `logs` and `env` are
shortcuts for the same `edka deployments` commands, for the linked deployment.

## Installing apps

```sh
edka apps catalog
edka apps catalog umami
edka apps options umami postgres_database --set postgres_instance=postgres
edka apps install excalidraw --wait
edka apps install umami --set postgres_instance=postgres --set postgres_database=umami --set postgres_user=umami --set hostname=stats.example.com
edka apps install n8n --name n8n-staging --data @n8n.json
```

`edka apps catalog <app>` lists an app's settings, their defaults, and which are
required. `apps install` installs into the linked or selected cluster and sends only
the settings you pass. Edka fills in the catalog's defaults and generates the app's
passwords. Settings with a list of options, such as a PostgreSQL instance or a traffic
class, take the option's name.

The catalog holds Edka's apps, the custom apps your organization published, and the
community's. They all install the same way, by slug:
`edka apps install memos --wait`.

In a terminal, `apps install` asks for what the app needs and `--set` left out:

- the PostgreSQL, Valkey or ClickHouse instance it uses, then the database and user
  in that instance, each from a picker;
- secrets the catalog does not generate, as hidden input;
- whether to expose the app, then its traffic class, then a hostname from the
  cluster's domains on that traffic class. For a wildcard domain you type the name
  under it. The Tailscale traffic class takes any hostname;
- a name for the new instance, when the app is already installed in the cluster.

It then shows every setting it will send and asks to go ahead. `--yes` skips that
question. Other settings keep their defaults.

`--set` refuses secrets, because shell history keeps them. Enter them at the prompt,
or put them in a file for `--data @file`. `--wait` follows the installation until
the app is installed, with progress on stderr.

Scripts and agents never get a prompt. They list the choices for a setting with
`apps options <app> <setting> --json`, which prints `{"data": [{"value", "label"}],
"open": false}`. `open` is true for lists such as namespaces, where a new name works
too. A list that depends on another setting takes it with `--set`. With `--json`, a
failed install prints each missing setting on stderr, with its options or the setting
it depends on:

```json
{"error": "…", "status": 400, "fields": [
  {"field": "postgres_instance", "message": "Required", "label": "PostgreSQL instance",
   "type": "dynamic-select",
   "options": [{"value": "104b21ed-…", "label": "postgres (postgres.postgres)"}]},
  {"field": "postgres_database", "message": "Required", "label": "PostgreSQL database",
   "type": "dynamic-select", "depends_on": "postgres_instance"}
]}
```

## Every API endpoint

`edka api` has a command for each Edka API endpoint that a CLI token can call. Each
has its own help, path parameters, JSON body and query flags. The command hierarchy
follows the API path: a cluster-scoped database operation lives under
`edka api clusters databases`. An endpoint that performs an action is named after it,
such as `edka api deployments restart` or `edka api cronjobs trigger`. Other endpoints
end in `list` or `get` for reads, and `create`, `update` or `delete` for writes. Help
shows a summary of the endpoint when Edka has one, and its method and path.

```sh
edka api operations --search backup
edka api operations --search database --json
edka api clusters list
edka api cluster get production
edka api clusters nodepools list --cluster production
edka api clusters databases list --cluster production
edka api inventory resources list
edka api organization current-usage get
edka api clusters create --help
```

Use `edka api operations --search <term>` to find commands for node pools, databases
and backups, registries, object storage, cronjobs, Git builds and previews, coding
agents and environments, GitHub Actions, secrets, observability, ingress,
certificates and DNS, integrations, and costs. Typing an endpoint command without
`api`, such as `edka inventory resources list`, prints the `edka api` command to run.

```sh
edka api clusters create --data @cluster.json
edka api clusters create --field name=staging --field master_ha:=true --dry-run
edka api deployments scale --id "$DEPLOYMENT_ID" --field replicas:=3
edka api get /api/inventory/resources --query kind=deployment --json
edka api post /api/clusters --data @- < cluster.json
edka api deployments settings update --schema
```

`--field key=value` is a string; `--field key:=JSON` accepts numbers, booleans,
arrays and objects. Dot-separated keys construct nested objects. Quote shell
arguments containing spaces or special characters. `--query key=value` may repeat.
`--dry-run` prints the operation without submitting it; resolving a cluster name
may still read the cluster list. `--schema` prints the JSON Schema of the request
body, with an example, and sends no request. Three routes have one: creating an
image deployment, creating a Git deployment and changing a deployment's settings.
Their help lists the flag. Raw API requests remain subject to the same server
permissions. A command that deletes, replaces or revokes something asks for
confirmation, or takes `--yes`. Its help says so, and `edka api operations` marks it
in the CONFIRM column. A raw path asks when the route it names does.

For binary or text downloads, use `--output-file` to write a private file:

```sh
edka api clusters ssh-private-key download get --cluster production --output-file id_production
```

An endpoint protected by identity step-up needs the console while step-up is on, and
its help says so. Changes to your sign-in identity, billing changes and deleting the
organization stay in the console.

## Profiles and directory context

```sh
edka login --profile work
edka login --profile personal
edka profile list
edka profile use work
edka context --json
edka unlink
edka run --cluster staging -- ./scripts/release.sh
edka run --kubeconfig -- kubectl get pods -A
```

`link` writes `.edka.json` containing IDs and the API origin, with no credentials.
Commands find the nearest link in this directory or its parents, stopping at the
Git repository boundary. Add `.edka.json` to your project's `.gitignore` if the
context should remain local. `unlink` removes the link in the current directory,
even one `edka` can't read; parent links remain available. `run` starts a local command with `EDKA_PROFILE`,
`EDKA_API_URL`, `EDKA_ORGANIZATION`, `EDKA_CLUSTER` and `EDKA_DEPLOYMENT` set to
the current context. `edka` commands inside it read those variables ahead of the
directory link, so a script keeps the same target after it changes directory.
A variable with no value is set empty and falls back to the link, except the
deployment when `run` has a cluster. With `--kubeconfig`, `run` also sets
`KUBECONFIG` to a private file with your own kubeconfig for the cluster. It
expires after an hour, and `run` deletes the file when the command exits. `run`
passes no API token or application secrets, and exits with the command's exit
code. Ctrl+C and SIGTERM reach the command, and `run` waits for it to exit.

Profile precedence: flag → `EDKA_PROFILE` → directory link → active profile.
API origin: flag → `EDKA_API_URL` → selected profile → `https://api.edka.io`.
Cluster: flag → `EDKA_CLUSTER` → matching directory link → profile default.
Deployment: flag → `EDKA_DEPLOYMENT` → matching directory link; with `EDKA_CLUSTER` set, an empty `EDKA_DEPLOYMENT` skips the link.
Organization: flag → `EDKA_ORGANIZATION` → matching directory link → profile binding.
A link applies only when its profile and API origin match. The server rejects a
requested organization that differs from the OAuth authorization; sign in again
to authorize another organization. `profile use` changes the default; directory
links and explicit overrides take precedence.

With `--json`, `profile use` prints the profile as `profile list` does, and `unlink`
prints `project_file` and `removed`. `removed` is `false` when the directory had no link.

## Scripts, CI and automation

```sh
EDKA_TOKEN="$SCOPED_CLI_ACCESS_TOKEN" edka deployments list --all --json --no-input
edka api get /api/deployments --json | jq '.data[].id'
edka clusters delete "$CLUSTER_ID" --yes --json
```

`EDKA_TOKEN` takes an access token issued to the Edka CLI, with its audience and
scopes. Access tokens expire after 15 minutes, and Edka issues no long-lived
machine-to-machine tokens. For long-running local automation, use a saved profile,
whose token the CLI refreshes. A token from the environment is never saved or
refreshed. Browser login needs a terminal, and CI never prompts.

Successful JSON output goes to stdout and retains the API envelope and pagination
fields. Progress and errors go to stderr. `--json` errors are JSON objects on stderr.
Failures exit nonzero, Ctrl+C exits 130, and `run` retains a child's exit code. The
resource commands return one API page/snapshot; use query/cursor parameters where
the endpoint supports them. Mutations are never automatically retried, except that
`env set` and `env unset` send a change again after Edka refuses it as stale. A
refused change saves nothing. A `--wait` reads again when Edka
can't be reached or answers with a server error, and stops with that error after
two minutes of failed reads.

A `--json` error has `error` and `exit_code`. An API error adds `status` and
`request_id`, and `code`, `reason`, `details` and `fields` when the API's error body
has them. Each entry of `fields` names an invalid `field` and its `message`. Without
`--json`, the details and the fields follow the error message.

| Environment variable | Purpose |
| --- | --- |
| `EDKA_TOKEN` | Temporary scoped access token |
| `EDKA_PROFILE` | Profile override |
| `EDKA_API_URL` | API origin override; HTTPS except loopback |
| `EDKA_ORGANIZATION` | Authorized organization ID |
| `EDKA_CLUSTER` | Cluster ID or exact name |
| `EDKA_DEPLOYMENT` | Deployment ID or exact name |
| `EDKA_CONFIG_DIR` | Override the entire configuration directory |
| `EDKA_CREDENTIAL_STORE` | `auto`, `keyring`, or `file` |
| `XDG_CONFIG_HOME` | Configuration parent directory |
| `NO_COLOR` | Disable terminal colors and progress animation |
| `EDKA_NO_INPUT` | Never prompt, like `--no-input`; `0` or `false` turns it off |
| `EDKA_DEBUG` | Print each request to stderr, like `--debug`; `0` or `false` turns it off |
| `EDKA_NO_UPDATE_CHECK` | Never look for a newer release; `0` or `false` turns it off |
| `CI` | Disable interactive prompts and the lookup of a newer release |

Every command takes the global flags. `edka --help` lists all of them. The help of
a command lists `--cluster`, `--deployment`, `--profile`, `--json`, `--output`,
`--no-input` and `--yes`, and leaves out `--api-url`, `--organization`,
`--credential-store`, `--timeout`, `--color` and `--debug`.

Credentials use the system keyring by default. If unavailable, `auto` reports its
fallback and uses private files; `keyring` fails instead of falling back. Tokens
are kept separately from config and project links. See [security](docs/security.md).

## Shell completion and diagnosis

```sh
# zsh
mkdir -p ~/.zsh/completions
edka completion zsh > ~/.zsh/completions/_edka
# Include ~/.zsh/completions in fpath before compinit.

# bash
source <(edka completion bash)

# fish
edka completion fish > ~/.config/fish/completions/edka.fish

edka doctor
edka doctor --json
edka version
```

Tab completes commands, flags and names: clusters, deployments, databases, cronjobs,
installed apps and add-ons, the apps and add-ons of the catalog, and profiles.
`edka logs <TAB>` lists the deployments of the linked cluster, and
`edka logs --cluster staging <TAB>` those of `staging`. Each name comes with its
status or version. Completion waits up to 3 seconds for Edka. It offers no names
when you are signed out or Edka can't be reached.

`doctor` checks the CLI, where `edka diagnose` checks a deployment or a cluster: the config and
profile, the project link, API and OAuth discovery, your identity, and the cluster in
context. It prints the reason for each failed
check and exits 1. With `--json`, the reason is in the check's `detail`. `version`,
`upgrade`, `completion`, `unlink` and `help` work when `config.json` or `.edka.json` is corrupt.
The `profile` commands work when `.edka.json` is corrupt.

`version` prints what the binary is, for a bug report: its version, the commit it
was built from with the time of that commit, its Go release and platform, and the
revision of the Edka API that the `edka api` commands were generated from. A
binary built from a checkout with uncommitted changes says so, and one built
outside a Git checkout has no commit. `--json` prints the same as fields.

`--debug` prints a line to stderr for each request a command sends: the method and
URL, then the status, the time in milliseconds and Edka's request ID. A request
that gets no answer shows the reason instead.

```console
$ edka apps list --debug
debug: GET https://api.edka.io/api/clusters 200 143ms (request 484dd403-11fa-4d06-b1c3-8fcb5b2af4af)
debug: GET https://api.edka.io/api/clusters/35c0c473-e7d1-492e-af0d-170a78e0a8b3/apps/instances 200 96ms (request cfb1759c-7774-44a8-ba93-e6b77aaf4f1b)
```

The lines hold no header and no body, so no token and no secret. Stdout stays the
same, and `EDKA_DEBUG=1` turns the lines on for every command.

Headless browser authorization is supported with `login --no-browser` and an SSH
loopback tunnel. For example, forward port 43871 to the remote machine and run
`edka login --no-browser --callback-port 43871` there. Open the printed URL on the
machine with the browser. The callback binds only to `127.0.0.1` and verifies state,
issuer and PKCE. Authorization times out after five minutes.

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
