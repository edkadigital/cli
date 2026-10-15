# `edka clusters`

Create, inspect, protect and delete clusters.

## `edka clusters create <name>`

Create a cluster on Hetzner with the console's defaults: deletion protection
on, a daily etcd backup at a random time between 00:00 and 06:00 UTC, and one
node pool named default. Edka creates the servers in your Hetzner project with
the organization's stored Hetzner token.

Before creating anything, the command shows the estimated monthly price and asks
for confirmation; --yes skips it, and --dry-run prints the request instead.
--data and --field set or replace top-level request fields for options without
a flag, such as worker_node_pools or nat_gateway_enabled.

With --wait, provisioning progress goes to stderr until the cluster is active.
With --merge-kubeconfig, your kubeconfig is then merged as by
`edka clusters kubeconfig <cluster> --merge`.

Flags:

- `-d, --data string`: Request fields as JSON, @file.json, or @-
- `--dry-run`: Print the request and price without creating the cluster
- `-f, --field stringArray`: Request field: key=value or key:=JSON
- `--ha`: Run three control plane servers instead of one
- `--k3s-version string`: k3s version (default: the one Edka recommends for new clusters)
- `--location string`: Hetzner location of the control plane and the default pool (default "fsn1")
- `--master-type string`: Server type of the control plane (default "cx23")
- `--merge-kubeconfig`: With --wait, merge your kubeconfig into the one kubectl uses
- `--no-etcd-backup`: Skip the daily etcd backup
- `--no-protect`: Allow the cluster to be deleted without unprotecting it first
- `--node-type string`: Server type of the default node pool (default "cx23")
- `--nodes int`: Servers in the default node pool (default 1)
- `--use`: With --merge-kubeconfig, switch kubectl to the new context
- `--wait`: Wait until the cluster is active
- `--wait-timeout duration`: Maximum provisioning wait (default 30m0s)

Examples:

```bash
edka clusters create staging --wait
edka clusters create production --ha --nodes 3 --node-type cx33 --wait --merge-kubeconfig --use
edka clusters create edge --location ash --dry-run
edka clusters create batch --field 'worker_node_pools:=[{"name":"spot","instance_type":"cx43","instance_count":2}]'
```

## `edka clusters delete <cluster>`

Delete a cluster, its servers and data.

Delete a cluster with its servers, volumes and data. Deletion continues in the
background. Protected clusters must be unprotected first.

To remove a disconnected or self-hosted cluster from Edka without touching its
servers, use `edka api clusters delete <cluster> --field remove_from_edka_only:=true`.

## `edka clusters diagnose [cluster]`

Find out what is wrong with a cluster.

Read whether Edka reaches a cluster, what its last check of the node pools
found, the status of its deployments, the pods that fail and the warnings
Kubernetes recorded, and report what is wrong with the commands to run next.
The command changes nothing.

The findings come first, then what they were read from. A source that can't be
read is named and the rest is still reported. The command exits unsuccessfully
when it finds a problem. With --json, stdout has `healthy`, `findings` and
everything that was read.

Examples:

```bash
edka clusters diagnose
edka clusters diagnose production
edka clusters diagnose production --json
```

## `edka clusters get [cluster]`

Show a cluster and its node pools.

## `edka clusters kubeconfig <cluster>`

Download your kubeconfig for a cluster.

Download your own kubeconfig for a cluster to a new private file with
--output-file, or into the kubeconfig kubectl uses with --merge.

--merge adds the cluster, context and user as edka-<organization>-<cluster>,
replaces the entries an earlier merge wrote for the same cluster, and keeps the
rest of the file. It writes to the first file in KUBECONFIG that exists, or to
~/.kube/config, and keeps the previous version next to it with an .edka-backup
suffix. --use also makes the new context kubectl's current one.

Edka issues each kubeconfig once. --rotate issues a new one and revokes the
previous kubeconfig.

Flags:

- `--merge`: Merge into the kubeconfig kubectl uses
- `--output-file string`: New file to write, readable only by you
- `--rotate`: Rotate your credentials first, which revokes your previous kubeconfig
- `--use`: With --merge, switch kubectl to the merged context

Examples:

```bash
edka clusters kubeconfig production --merge --use
edka clusters kubeconfig production --rotate --merge
edka clusters kubeconfig production --output-file ~/.kube/production.yaml
KUBECONFIG=~/.kube/production.yaml kubectl get nodes
```

## `edka clusters list`

List clusters.

## `edka clusters protect <cluster>`

Protect a cluster from deletion.

## `edka clusters unprotect <cluster>`

Allow a cluster to be deleted.

Every command also takes the global flags in SKILL.md.
