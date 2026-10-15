# `edka deployments`

Inspect and manage deployments.

## `edka deployments build [deployment]`

Same as `edka build`.

## `edka deployments create`

Create a deployment from an image or a GitHub repository.

Create a deployment in the linked or selected cluster from a JSON body. The body
names a container image, or with --git a GitHub repository that Edka builds. Its
`github_repository_id` is Edka's ID of a connected repository, or its GitHub ID.

--example prints a body to start from, and --schema prints the JSON Schema of
every field. Neither sends a request.

With --link, this directory is linked to the new deployment. With --wait, the
command follows the first rollout, and with --git the first build before it.
Progress goes to stderr.

Flags:

- `-d, --data string`: Deployment JSON, @file.json, or @-
- `--dry-run`: Print the request without sending it
- `--example`: Print an example body and send nothing
- `-f, --field stringArray`: Body field: key=value or key:=JSON
- `--git`: Create a deployment that Edka builds from a GitHub repository
- `--link`: Link this directory to the new deployment
- `--schema`: Print the JSON Schema of the body and send nothing
- `--wait`: Wait until the first rollout is healthy; with --git, follow the first build too
- `--wait-timeout duration`: Maximum wait; 15m by default with --git (default 5m0s)

Examples:

```bash
edka deployments create --example > deployment.json
edka deployments create --data @deployment.json --link --wait
edka deployments create --data @deployment.json --field image_tag=1.30 --dry-run
edka deployments create --schema
edka deployments create --git --example
```

## `edka deployments delete <deployment>`

Delete a deployment and its Kubernetes resources.

## `edka deployments diagnose [deployment]`

Find out why a deployment is not running.

Read a deployment's rollout, pods, Kubernetes events, last revisions and the log
of its failing pod, and report what is wrong with the commands to run next. A
Git deployment's last build is read too. The command changes nothing.

The findings come first, then what they were read from. A source that can't be
read is named and the rest is still reported. The command exits unsuccessfully
when it finds a problem. With --json, stdout has `healthy`, `findings` and
everything that was read.

Flags:

- `-n, --tail int`: Number of log lines of the failing pod (at most 2000) (default 30)

Examples:

```bash
edka diagnose
edka diagnose api
edka diagnose api --tail 100
edka diagnose api --json
```

## `edka deployments env`

Same as `edka env`.

## `edka deployments env list`

Same as `edka env list`.

## `edka deployments env set KEY=value...`

Same as `edka env set`.

## `edka deployments env unset KEY...`

Same as `edka env unset`.

## `edka deployments get [deployment]`

Show a deployment's configuration.

## `edka deployments list`

List deployments in the linked or selected cluster, or in every cluster when
none is linked. Use --all to include every cluster.

Flags:

- `--all`: List deployments in every cluster, not only the linked one

Examples:

```bash
edka deployments list
edka deployments list --all --json
```

## `edka deployments logs [deployment]`

Same as `edka logs`.

## `edka deployments restart [deployment]`

Same as `edka restart`.

## `edka deployments revisions [deployment]`

List revisions to roll back to.

Flags:

- `--limit int`: Number of revisions (default 20)

Examples:

```bash
edka deployments revisions api
edka deployments rollback api --generation 4
```

## `edka deployments rollback [deployment]`

Same as `edka rollback`.

## `edka deployments scale [deployment]`

Same as `edka scale`.

## `edka deployments status [deployment]`

Show runtime status, replicas and pods.

Every command also takes the global flags in SKILL.md.
