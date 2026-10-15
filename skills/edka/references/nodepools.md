# `edka nodepools`

List, add, scale and delete node pools.

## `edka nodepools add <name>`

Add a node pool to a cluster.

Add a Hetzner Cloud node pool to the linked or selected cluster. The pool runs
in the cluster's location unless --location names another.

--nodes sets how many servers the pool has. With --min and --max the pool
autoscales between them instead, and autoscaling stays on for the life of the
pool.

A label is key=value. A taint is key=value:effect, with the effect NoSchedule,
PreferNoSchedule or NoExecute.

Flags:

- `--dry-run`: Print the request without sending it
- `--label stringArray`: Node label as key=value
- `--location string`: Hetzner location (default: the cluster's)
- `--max int`: Most servers of an autoscaling pool
- `--min int`: Fewest servers of an autoscaling pool
- `--nodes int`: Servers in the pool (default 1)
- `--taint stringArray`: Node taint as key=value:effect
- `--type string`: Hetzner server type, such as cx33
- `--wait`: Wait until the pool's servers have joined the cluster
- `--wait-timeout duration`: Maximum wait for the change (default 20m0s)

Examples:

```bash
edka nodepools add workers --type cx33 --nodes 2 --wait
edka nodepools add burst --type cpx32 --min 0 --max 5
edka nodepools add gpu-jobs --type ccx33 --label workload=batch --taint dedicated=batch:NoSchedule
edka nodepools add workers --type cx33 --cluster production --dry-run
```

## `edka nodepools delete <pool>`

Delete a node pool and its servers.

Delete a node pool. Edka drains its servers, then deletes them. It refuses
while a deployment, an app, a database or another resource is pinned to the
pool, and the error names them.

Flags:

- `--dry-run`: Print the request without sending it
- `--wait`: Wait until the pool's servers are deleted
- `--wait-timeout duration`: Maximum wait for the change (default 20m0s)

Examples:

```bash
edka nodepools delete workers
edka nodepools delete workers --cluster production --yes --wait
```

## `edka nodepools get [pool]`

Show a node pool, its labels and taints.

Examples:

```bash
edka nodepools get workers
edka nodepools get workers --cluster production --json
```

## `edka nodepools list`

List node pools.

List the node pools of the linked or selected cluster, or of every cluster when
none is linked. Use --all to include every cluster. NODES shows the servers that
exist and the servers the pool is set to have.

Flags:

- `--all`: List node pools in every cluster, not only the linked one

Examples:

```bash
edka nodepools list
edka nodepools list --all --json
```

## `edka nodepools scale <pool>`

Change how many servers a node pool has.

Set how many servers a node pool has with --nodes. Edka creates the servers a
larger pool needs. For a smaller pool it drains the servers it removes, then
deletes them.

An autoscaling pool takes --min and --max instead. On a pool with a set number
of servers, --min and --max turn autoscaling on, and it stays on for the life
of the pool.

Flags:

- `--dry-run`: Print the request without sending it
- `--max int`: Most servers of an autoscaling pool
- `--min int`: Fewest servers of an autoscaling pool
- `--nodes int`: Servers the pool has
- `--wait`: Wait until the pool has its servers
- `--wait-timeout duration`: Maximum wait for the change (default 20m0s)

Examples:

```bash
edka nodepools scale workers --nodes 4 --wait
edka nodepools scale burst --max 8
edka nodepools scale workers --min 2 --max 6
```

Every command also takes the global flags in SKILL.md.
