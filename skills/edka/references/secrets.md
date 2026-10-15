# `edka secrets`

Create, change and delete Kubernetes secrets in a cluster.

## `edka secrets delete <secret>`

Delete a secret from its namespace, after confirmation or with --yes. A pod
that reads it keeps running, and fails to start again without it.

Flags:

- `--namespace string`: Namespace of the secret (default "default")

Examples:

```bash
edka secrets delete database --namespace shop
edka secrets delete database --yes
```

## `edka secrets get <secret>`

Show a secret's keys, type and age.

Show a secret: its keys, its type and when it was created. Edka never returns
the values.

Flags:

- `--namespace string`: Namespace of the secret (default "default")

Examples:

```bash
edka secrets get database --namespace shop
edka secrets get database --json
```

## `edka secrets list`

List the secrets Edka manages, with their keys.

List the secrets Edka created in the linked or selected cluster, or in every
cluster when none is linked. Use --all to include every cluster, and
--namespace for one namespace. The list shows the keys of each secret and never
a value.

Flags:

- `--all`: List secrets in every cluster, not only the linked one
- `--namespace string`: Only secrets in this namespace

Examples:

```bash
edka secrets list
edka secrets list --namespace shop --json
```

## `edka secrets set <secret> [KEY...]`

Create a secret, or set keys of one.

Set keys of a secret, and create the secret when the namespace has none of that
name. The keys left out keep their values.

Each value is read at a hidden prompt. When stdin is not a terminal, the
command reads one value from stdin and drops the final newline. A value on the
command line is refused, because shell history keeps it and other local users
can read it in the process list. --from-file KEY=path takes the content of a
file as the value of a key.

A pod that reads the secret as environment variables gets a new value when it
restarts.

Flags:

- `--from-file stringArray`: Take the content of a file as a value: KEY=path
- `--namespace string`: Namespace of the secret (default "default")

Examples:

```bash
edka secrets set database PASSWORD REPLICA_PASSWORD --namespace shop
printf %s "$TOKEN" | edka secrets set github TOKEN
edka secrets set tls-ca --from-file ca.crt=./ca.crt
```

## `edka secrets unset <secret> KEY...`

Remove keys from a secret and keep the others. The command fails without a
change when a key is not set.

Flags:

- `--namespace string`: Namespace of the secret (default "default")

Examples:

```bash
edka secrets unset database OLD_PASSWORD --namespace shop
```

Every command also takes the global flags in SKILL.md.
