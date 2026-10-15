# Everyday workflows

Deploy, build, read logs, diagnose, change variables, scale, restart and roll back a deployment.

## `edka build [deployment]`

Build a Git deployment from its current branch, or from --ref and --commit.

With --wait, the build log and each build step go to stderr. When the deployment
has auto-deploy on, --wait also waits for the rollout of the new image, so the
command ends when the build runs. With auto-deploy off, deploy the build with
`edka up --wait`.

Flags:

- `--commit string`: Commit SHA override
- `--ref string`: GitHub ref override
- `--wait`: Stream the build log until it finishes, then follow an auto-deploy
- `--wait-timeout duration`: Maximum wait for the build and its rollout (default 15m0s)

Examples:

```bash
edka build api --wait
edka build api --ref refs/heads/main --commit abc1234
```

## `edka diagnose [deployment]`

Find out why a deployment or cluster is not working.

Read a deployment's rollout, pods, Kubernetes events, last revisions and the log
of its failing pod, and report what is wrong with the commands to run next. A
Git deployment's last build is read too. The command changes nothing.

The findings come first, then what they were read from. A source that can't be
read is named and the rest is still reported. The command exits unsuccessfully
when it finds a problem. With --json, stdout has `healthy`, `findings` and
everything that was read.

Without a deployment in the link, in --deployment or as an argument, the
command diagnoses the cluster, as `edka clusters diagnose` does. The other
kinds have their own: `edka apps diagnose`, `edka previews diagnose`,
`edka databases diagnose` and `edka cronjobs diagnose`.

Flags:

- `-n, --tail int`: Number of log lines of the failing pod (at most 2000) (default 30)

Examples:

```bash
edka diagnose
edka diagnose api
edka diagnose api --tail 100
edka diagnose api --json
edka diagnose --cluster production
```

## `edka env`

List and change a deployment's variables and secrets.

List and change the environment variables and secrets of the linked deployment,
or of the one --deployment names. Each change starts a rollout; --wait follows it.

Secret values stay hidden. `edka env` lists secrets by name only, and `set --secret`
reads each value at a hidden prompt, or one value from stdin without its final
newline. Values never go on the command line, where shell history keeps them.

Examples:

```bash
edka env
edka env set PORT=8080 LOG_LEVEL=debug --wait
edka env set --secret DATABASE_URL
printf %s "$TOKEN" | edka env set --secret API_TOKEN
edka env unset LOG_LEVEL
```

## `edka env list`

List variables, and secrets by name.

## `edka env set KEY=value...`

Set variables, or secrets with --secret.

Flags:

- `--diff`: Show what the change would alter, and send nothing
- `--secret`: Set secrets; values are read at a hidden prompt or from stdin
- `--wait`: Wait until the rollout the change starts is healthy
- `--wait-timeout duration`: Maximum rollout wait (default 5m0s)

Examples:

```bash
edka env set PORT=8080 LOG_LEVEL=debug
edka env set PORT=9090 --diff
edka env set --secret DATABASE_URL SESSION_KEY
edka env set --secret API_TOKEN < token.txt
```

## `edka env unset KEY...`

Remove variables or secrets.

Flags:

- `--diff`: Show what the change would alter, and send nothing
- `--wait`: Wait until the rollout the change starts is healthy
- `--wait-timeout duration`: Maximum rollout wait (default 5m0s)

Examples:

```bash
edka env unset LOG_LEVEL DEBUG
edka env unset LOG_LEVEL --diff
```

## `edka logs [deployment]`

Read deployment logs; follow with --follow.

--since reads the lines of the last minutes or hours, up to 2000 lines unless
--tail sets fewer. --timestamps starts each line with its time.

--follow reads the log again every --interval and prints the new lines until
Ctrl+C. Unless --pod names a pod, Edka picks one for each read, so after a
rollout the command follows a new pod. It names the pod it follows on stderr,
and says when lines may be missing because more than --tail lines arrived
between two reads.

Flags:

- `--container string`: Container name
- `-F, --follow`: Poll new log lines until Ctrl+C
- `--interval duration`: Log polling interval (default 2s)
- `--pod string`: Pod name (default: the failing pod with most restarts, else the newest)
- `--previous`: Read the previous container instance, for example after a crash
- `--since duration`: Read the lines of the last duration, such as 15m or 2h
- `-n, --tail int`: Number of log lines (at most 2000) (default 100)
- `--timestamps`: Start each line with its time

## `edka open`

Open the linked cluster in the Edka console.

## `edka restart [deployment]`

Restart a deployment's pods.

## `edka rollback [deployment]`

Restore a previous deployment revision.

Flags:

- `--generation int`: Revision generation to restore

## `edka run -- <command> [args…]`

Run a local command with EDKA_* context variables set.

Run a local command with the current Edka context in its environment, as
EDKA_PROFILE, EDKA_API_URL, EDKA_ORGANIZATION, EDKA_CLUSTER and EDKA_DEPLOYMENT.

`edka` commands inside it read these variables ahead of the directory link and
the active profile, so a script keeps the same target after it changes
directory. A variable with no value is set empty, and those commands fall back
to the link and profile for it. When run has a cluster, an empty EDKA_DEPLOYMENT
instead means no deployment, and no link's deployment applies.

With --kubeconfig, run also sets KUBECONFIG to a private file with your own
kubeconfig for the cluster. It expires after an hour, and run deletes the file
when the command exits. The one-time kubeconfig download stays available.

run passes no API token or application secrets, and exits with the command's
exit code. Ctrl+C and SIGTERM reach the command, and run waits for it to exit.

Flags:

- `--kubeconfig`: Set KUBECONFIG to your own kubeconfig for the cluster, valid for an hour

Examples:

```bash
edka run --cluster staging -- ./scripts/release.sh
edka run --kubeconfig -- kubectl get pods -A
edka run -- sh -c 'echo "$EDKA_CLUSTER $EDKA_DEPLOYMENT"'
```

## `edka scale [deployment]`

Set a deployment's replica count.

Flags:

- `--replicas int`: Desired replica count (0–100) (default -1)

## `edka status`

Inspect your linked cluster and deployment.

## `edka up [deployment]`

Deploy image changes or redeploy a successful Git build.

Create an image deployment with --data, update an existing deployment's settings,
or redeploy its latest successful Git build. An image deployment without new
settings is restarted. To build a new Git revision, run `edka build --wait`; with
auto-deploy on, it also rolls the build out.

With --wait, progress goes to stderr: pods updated and ready, and pods that fail.

With --diff, the command compares the settings it would send with the deployment
and sends nothing. It lists each field that changes, and for a list such as
`config.env_variables` each entry the request adds, changes or removes, since a
request replaces the lists it sends. Secret values are never shown. Edka refuses
a change sent with --expected-generation when the deployment is at another
generation than the one --diff printed.

Flags:

- `-d, --data string`: Deployment JSON, @file.json, or @-
- `--diff`: Show what the settings would change, and send nothing
- `--expected-generation int`: Have Edka refuse the change unless the deployment is at this generation
- `-f, --field stringArray`: Body field: key=value or key:=JSON
- `--wait`: Wait until the submitted revision becomes healthy
- `--wait-timeout duration`: Maximum rollout wait (default 5m0s)

Examples:

```bash
edka up api --wait
edka up api --field config.image_tag=v2 --wait
edka up api --field config.image_tag=v2 --diff
edka up api --field config.image_tag=v2 --expected-generation 5 --wait
edka up --cluster production --data @deployment.json
```

Every command also takes the global flags in SKILL.md.
