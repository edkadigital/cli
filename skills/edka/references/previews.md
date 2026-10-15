# `edka previews`

List, inspect and delete pull request previews.

## `edka previews delete <pr>`

Delete a preview and what runs for it.

## `edka previews diagnose [pr]`

Find out why a preview is not running.

Read a preview as Edka records it, its rollout, pods, Kubernetes events and the
log of its failing pod, and report what is wrong with the commands to run next.
The command changes nothing.

The findings come first, then what they were read from. A source that can't be
read is named and the rest is still reported. The command exits unsuccessfully
when it finds a problem. With --json, stdout has `healthy`, `findings` and
everything that was read.

Flags:

- `-n, --tail int`: Number of log lines of the failing pod (at most 2000) (default 30)

Examples:

```bash
edka previews diagnose 12
edka previews diagnose 12 --deployment api --tail 100
edka previews diagnose 12 --json
```

## `edka previews list [deployment]`

List a deployment's previews.

Flags:

- `--deleted`: Include deleted previews

Examples:

```bash
edka previews list
edka previews list api --deleted --json
```

## `edka previews logs [pr]`

Read a preview's logs; follow with --follow.

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

## `edka previews open [pr]`

Open a preview in the browser.

Print a preview's URL to stderr and open it in the browser. With --json, or
without a terminal, the command prints the URL on stdout and opens nothing.

## `edka previews status [pr]`

Show a preview, its replicas and pods.

Show a preview as Edka records it, and what runs for it in the cluster. A
preview that is still building or that failed runs nothing yet; the command
then shows the record with its error and says so on stderr.

Every command also takes the global flags in SKILL.md.
