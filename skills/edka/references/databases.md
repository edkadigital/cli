# `edka databases`

Inspect, back up and delete databases.

## `edka databases backup <database>`

Start a backup now.

## `edka databases backups [database]`

List a database's backups, newest first. PostgreSQL reports its ten most recent.

## `edka databases delete <database>`

Delete a database and its data.

Flags:

- `--force`: Delete even while apps use it or it is still provisioning

## `edka databases diagnose [database]`

Find out what is wrong with a database.

Read a database as Edka records it, its instances, the conditions and warnings
its operator reports, its backups and the log of an instance that is not ready,
and report what is wrong with the commands to run next. The command changes
nothing.

The findings come first, then what they were read from. A source that can't be
read is named and the rest is still reported. The command exits unsuccessfully
when it finds a problem. With --json, stdout has `healthy`, `findings` and
everything that was read.

Flags:

- `-n, --tail int`: Number of log lines of the instance that is not ready (at most 2000) (default 30)

Examples:

```bash
edka databases diagnose orders
edka databases diagnose orders --tail 100
edka databases diagnose orders --json
```

## `edka databases get [database]`

Show a database, its instances and recovery window.

## `edka databases list`

List databases in the linked or selected cluster, or in every cluster when
none is linked. Use --all to include every cluster.

Flags:

- `--all`: List databases in every cluster, not only the linked one

## `edka databases logs [database]`

Read a database's logs; follow with --follow.

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

Every command also takes the global flags in SKILL.md.
