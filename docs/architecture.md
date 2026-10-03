# Architecture

The binary uses Cobra for discoverable command groups and completion; Bubble Tea
provides keyboard-driven, filterable selectors, and Lip Gloss provides restrained
terminal styling. Requests and OAuth run through Go's HTTP client and contexts.

`cmd/edka` owns process signals, exit codes and error formatting. `internal/cli`
wires commands and resolves flags/environment/project/profile context.
`internal/catalog` embeds the Edka API endpoints that `edka api` calls.
`internal/api` handles origin-bound HTTP, response limits, errors, and envelopes.
`internal/auth` owns discovery, public native client registration, PKCE, loopback
callbacks, token refresh and revocation. `internal/credential` separates secrets
from configuration and uses the OS keyring or private files. `internal/config`
handles atomic config writes and project link discovery. `internal/selfupdate`
reads the latest release from GitHub, verifies its archive and replaces the binary,
for `edka upgrade` and the notice of a newer release. `internal/ui` owns output,
curated tables, filterable pickers, progress, and terminal escape sanitization.

Top-level resource commands (`clusters`, `nodepools`, `domains`, `registries`,
`secrets`, `deployments`, `apps`, `addons`, `databases`, `cronjobs`, `previews`) are written by
hand: fixed verbs, name resolution, curated
columns, and confirmations that name the resource. They compose the same API routes
and may read several (for example one app, database or cronjob list per cluster) to
present one consistent view. The lists of several clusters are read six at a time,
and a list that fails stops the command. `addons` also reads the add-on catalog: it holds the
version an update installs, each add-on's dependencies and whether Edka requires it. Every other endpoint is generated under `edka api`,
where the hierarchy follows the API path. Endpoint names typed without `api`
print the command to run instead of executing anything.

Edka has one route for a cluster's node pools: it takes every pool, and deletes the
ones a request leaves out. `nodepools add`, `scale` and `delete` read the pools,
change one, and send all of them back with the fields the console sends for a
Hetzner Cloud pool. A request replaces the labels, the taints and the autoscaling
switch of each pool it names, so every entry carries them. The request also names
the time each pool last changed, and Edka refuses it when a pool changed after the
read. A metal pool carries its servers, its vSwitch and its Robot account, so the
commands refuse a cluster that has one and send nothing.

`domains get` reads a domain's certificate from the cluster and lists the DNS
records the console lists: the CNAME Edka returns for a certificate validated over
DNS, and one record for each address of the domain's traffic class, CNAME only for
a Tailscale class. When the cluster can't be reached, it shows the status Edka last
recorded and no validation record, which Edka alone can build.

Each generated command maps to one method and path, and takes path flags, query
parameters, a JSON body and typed fields. The catalog records the revision of the
Edka API it was generated from. The commands are fixed when the binary is built, and
the server can't add or replace them.

`env set --diff` and `env unset --diff` make the settings body the change would
send, from the lists as they are read, and compare it with the same read the way
`up --diff` does. They read no secret value, since the diff shows names only. They
name no generation to apply at: a change of the lists is planned again from the
lists as they are when it is sent.

`previews` reads the previews of one Git deployment and selects one by its pull
request number. Edka has no route for one preview's record, so `status` takes the
record from the list and adds what the status route reads from the cluster.
`open` passes a URL to the browser only when it is an `http` or `https` address
with a host, since the address comes from Edka.

`up --diff` reads the deployment and compares the settings body with it, key by
key, since Edka merges the `config` of a request into the stored one and replaces a
list the request sends. `env_variables` and `secrets` are compared by name,
`volumes` by mount path, and the hostnames as Edka reads them: `hostname` first,
then `hostnames`, with the top level of the body deciding when it names either. Edka
keeps no hostname of a deployment whose merged config has `expose_via_ingress` off,
so for such a request the diff lists the stored hostnames as removed and the ones
the request sends as ignored. Edka refuses a request whose `config` turns
`expose_via_ingress` on and names no hostname, and one that leaves an exposed
deployment no hostname, and the diff lists both as refused. A request that moves the
image and names no digest shows the digest it unpins. A top-level field that the
settings schema in the catalog lacks is listed as ignored. The diff holds no secret
value: `secrets` gives names, and `secret_values` the names it sets, as an object of
values by name or as a list of `name` and `value`. Edka writes a value only for a
secret the merged config lists, so a value for another name is listed as ignored.
It refuses a secret the deployment lacks when `secret_values` has no value for it,
and the diff lists that secret as refused.
`--expected-generation` adds `expected_generation` to the body, which Edka
answers with 409 `Change refused` when the deployment has another generation.

`diagnose` reads a deployment, its runtime status, its last five revisions, the
events of its Deployment, ReplicaSets and pods, and for a Git deployment its last
build. Only the deployment has to be readable. A read that fails is listed as
unread, and one of the runtime status is a finding. It then reads the log of one
pod: the failed pod with the most restarts, else a pod that is not ready, and the
container before the last restart when the pod restarted. The findings come from a
fixed table of the reasons Kubernetes gives for a pod and of four event reasons
(`FailedScheduling`, `Unhealthy`, `FailedMount` and `FailedAttachVolume`,
`FailedCreate`), read only while the rollout is not `deployed`. The events include
those of ReplicaSets kept from earlier rollouts, so a pod's warning counts while
that pod is listed and not ready, and a ReplicaSet's while the Deployment has the
`ReplicaFailure` condition. That condition is a finding with its own reason and
message when no `FailedCreate` event was read. An automatic rollback is reported by
the status of its revision: applied, still rolling out, or failed. A failed pod with
a reason outside the table is reported with that reason and no advice. The pods,
the warnings and the log are printed either way.

`edka diagnose` with no deployment as an argument, in `--deployment` or in the link
diagnoses the cluster in context, as `edka status` shows the cluster then.
`edka deployments diagnose` takes a deployment in every case.

`clusters diagnose` reads the cluster, `/connectivity`, `/drift`,
`/deployments/status`, `/pods/problematic` and `/kubernetes-events`, and for a cluster
that is not active the five newest rows of `/events`. Only the cluster has to be
readable, and its record is printed without its webhook token. Connectivity other
than `connected` is a finding that quotes the `finding` and `suggested_action` of the
open incident's investigation, or else the probe error. Each drift record that is not
resolved is a finding, and a problem unless its severity is `info`. A deployment
whose status is `failed` or `not_found` is a finding. Kubernetes names a Deployment's
pods after it, so a failing pod belongs to the deployment of its namespace with the
longest name that the pod's name starts with, before a hyphen. The pod of a
deployment that has a finding gets none. Five other pods get a finding each, and one
more finding counts the rest.

`apps diagnose` reads the app and `/pods`, which lists the app's pods with the
status, container, reason and message Edka gives each. Only the app has to be
readable. A pod of a Job that finished has the status `succeeded` and gets no
finding. The command then reads the events and the log of one pod: the failed pod
with the most restarts, else a pod that is not ready. The log is the one of the
container that pod's row names. The findings come from the table of a deployment's
pods and events, with the commands of `apps`. Edka gives a pod no node can run the
reason `Unschedulable`, which is the finding of the event `FailedScheduling`, so the
two make one finding. A health check that fails is a problem when a pod failed, and
a note otherwise. An install or update that failed and an uninstall that failed are
findings with the message of the app's `progress`. Edka answers `/pods` with an
error for a cluster it can't read and with an empty list for an app that runs
nothing, so a `/pods` that can't be read is a finding, and an installed app with no
pods a note.

`previews diagnose` reads the preview from its deployment's list, then its status.
When the status answers, the command reads the events of the preview's Deployment by
the name and namespace the status gives. The status has a pod's phase and no reason.
When a pod is not ready, the command reads `/pods/problematic` for the namespace and
gives each pod the status lists the reason and message of its first container that
has one. A pod the cluster reports as crashing, failed or in error counts as failed.
The rollout is failed when a pod failed or the `Progressing` condition is false,
deployed when every replica is ready and updated and the Deployment has no
`ReplicaFailure` condition, and pending otherwise. Ready replicas alone may be the
pods of the change before. The findings come from
the table of a deployment's pods and events, with the commands of `previews`. A
preview has no revisions, so no finding names a rollback. A status that can't be read
is a finding for a preview that is `active` or `updating`. In the other statuses the
preview may run nothing.

`databases diagnose` reads the database and its runtime status, then the log of the
first instance that is not ready. An instance that is not ready is a finding. When
none is listed, a count of ready instances below the total is one. `available: false`
with a `phase` is a problem, and without one a note, since Edka sends it both for a
database it can't read and for an engine it reads no status of. The conditions
`ContinuousArchiving` and `LastBackupSucceeded` are findings when false. A
`lastFailedBackup` later than `lastSuccessfulBackup` is one too, with the error of the
newest backup that has one. Valkey, MySQL and ClickHouse send no `lastFailedBackup`.
Without it, the last backup is the newest record in `backups` whose phase is
`completed` or `failed`, and it is a finding when it failed. A completed backup can
carry a warning in `error`, so the phase decides. Up to five `provisioningWarnings`
are findings. All of these are notes while the database is `pending`, `provisioning`
or `deleting`.

`cronjobs diagnose` reads the cronjob, its runtime status, its five newest runs and
the events of its CronJob, Jobs and pods. The last run is the newest one that
succeeded or failed. When it failed, the finding quotes the newest warning about its
Job or a pod named after it, and the command reads that run's log. The newest warning
about the CronJob is a problem for the reasons `FailedCreate` and `FailedNeedsStart`,
and a note for another reason. The newest run that has not ended is `Running`, or
`Unknown` while its Job has no pod. `FailedScheduling` on a `Running` run is a problem,
and so is `FailedCreate` on an `Unknown` run. A `FailedCreate` event outlives the
failure, so it does not count once the run has its pod. Edka answers the runtime
status with no times when it can't read the cluster, so `No run has started yet`
needs a list of runs that was read and is empty.

The catalog holds the routes that Edka lets a CLI token call. A route's request body
has a JSON Schema in the catalog when Edka describes it, with one example that the
schema accepts. `--schema` and `deployments create --example` print them from the
binary. The CLI does not check a body against its schema; the API does.

A route whose last path word starts with a verb (`restart`, `drain-node`) is named
after that word; other writes end in the method's verb, and reads end in `get` or
`list`. No two routes share a name, and no command name is also a group of other
commands. A route has a summary when Edka's comment on it starts with a verb. The
others show their method and path.

Each route in the catalog says whether it asks for confirmation. Every DELETE does,
every route with a word such as `rollback` or `unprotect` as a path segment, and each
route that destroys, replaces or revokes something without such a word. A generated
command reads the mark of its route. A raw `edka api` path reads the mark of the
route it names. A path that names no route in the catalog asks when it is a DELETE
or has one of the words in `confirm_words`.

The short workflows compose those same APIs: login → whoami → link, deploy → poll
the deployment and runtime status, build → poll the build log → find the auto-deploy
revision → poll its rollout, deployment selection → log polling, env → read the
deployment's stored configuration → send back each list it changes, and apps
install → resolve the catalog app → in a terminal, ask for each setting the catalog
requires without a default, and for the switches that show one → send the chosen
settings → ask for any setting the API still reports missing → poll the app's
status. The guide evaluates each setting's show_if as Edka does, and offers only
hostnames under domains managed on the chosen traffic class, as the console does.
A settings request replaces every list it sends, so `env` sends the whole list, and
it refuses to write when the read returns no configuration. The request names the
spec generation the lists were read at, as `expected_generation`. Edka answers 409
`Change refused` when the deployment has another generation, and `env` reads the
lists again and sends the change from those, three times at most. `env set --secret`
reads the values before the lists. Waits print each
progress line only when it changes, to stderr, and hold back a half-written log line
until it ends. Names resolve through
current authorized API lists; ambiguous names require an explicit ID or selection.
No user-supplied API origin can inherit credentials issued for another origin.

Shell completion offers names from the lists that resolve a name, read with the
profile, link and flags of the command line being completed. A completion never
prompts, stops reading after 3 seconds, and prints nothing when a read fails.
Arguments and flags that take no path offer no file names. Cobra keeps a flag's
completion until the process exits, so the CLI registers flag completions only
when the shell asks for one.

Credentials are refreshed under a per-profile filesystem lock. The saved session
is re-read after locking, avoiding concurrent rotation of the same refresh token.
Persisting the rotated token precedes use of the replacement access token.
Mutations do not retry, avoiding duplicate resources after ambiguous responses.
A wait repeats a read that fails because Edka can't be reached, or that Edka
answers with 408, 429 or a 5xx status. The pause between reads grows, and the
wait stops with the error after two minutes of failed reads.

Tests exercise the entire PKCE client handshake against an HTTP authorization
fixture, actual loopback callback state/issuer validation, concurrent refresh,
origin/redirect handling, generated command registration, request body/query types,
destructive confirmations, output formats and project context.
