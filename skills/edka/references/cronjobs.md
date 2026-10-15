# `edka cronjobs`

Inspect, run and pause scheduled jobs.

## `edka cronjobs delete <cronjob>`

Delete a cronjob and its Kubernetes resources.

## `edka cronjobs diagnose [cronjob]`

Find out why a cronjob's runs fail or don't start.

Read a cronjob as Edka records it, its schedule in the cluster, its last five
runs, Kubernetes events and the log of the last run when it failed, and report
what is wrong with the commands to run next. The command changes nothing.

The findings come first, then what they were read from. A source that can't be
read is named and the rest is still reported. The command exits unsuccessfully
when it finds a problem. With --json, stdout has `healthy`, `findings` and
everything that was read.

Flags:

- `-n, --tail int`: Number of log lines of the failed run (at most 2000) (default 30)

Examples:

```bash
edka cronjobs diagnose nightly-report
edka cronjobs diagnose nightly-report --tail 100
edka cronjobs diagnose nightly-report --json
```

## `edka cronjobs get [cronjob]`

Show a cronjob and its last run.

## `edka cronjobs list`

List cronjobs in the linked or selected cluster, or in every cluster when none
is linked. Use --all to include every cluster.

Flags:

- `--all`: List cronjobs in every cluster, not only the linked one

## `edka cronjobs logs [cronjob]`

Read a run's logs; the newest run by default.

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
- `--run edka cronjobs runs`: Run name from edka cronjobs runs (default: the newest)
- `--since duration`: Read the lines of the last duration, such as 15m or 2h
- `-n, --tail int`: Number of log lines (at most 2000) (default 100)
- `--timestamps`: Start each line with its time

## `edka cronjobs resume <cronjob>`

Restart scheduled runs.

## `edka cronjobs runs [cronjob]`

List recent runs, newest first.

Flags:

- `--limit int`: Number of runs (1–100) (default 20)

## `edka cronjobs suspend <cronjob>`

Stop scheduled runs.

## `edka cronjobs trigger <cronjob>`

Start a run now.

Every command also takes the global flags in SKILL.md.
