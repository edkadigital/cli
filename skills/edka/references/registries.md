# `edka registries`

Store registry credentials and browse a cluster's registry.

## `edka registries add <name>`

Store the credentials of a registry.

Store the credentials of a container registry for the organization. The
password, or the token that stands for it, is read at a hidden prompt, or from
stdin when stdin is not a terminal. It is never an argument, because shell
history keeps arguments and other local users can read them in the process
list.

Every type but docker-hub needs --url. For aws-ecr the username is the access
key ID and the password the secret access key; --aws-region is for a URL that
does not hold the region.

--replace stores new credentials for a registry that exists. Flags left out
keep their values, the password is asked again, and the clusters that use the
registry get the new credentials.

Flags:

- `--aws-region string`: Region of an aws-ecr registry whose URL does not hold it
- `--email string`: Email address of a docker-hub account
- `--replace`: Store new credentials for a registry that exists
- `--type string`: Kind of registry: docker-hub, github-registry, google-artifact, aws-ecr, custom
- `--url string`: Address of the registry, such as ghcr.io
- `--username string`: User the password or token belongs to

Examples:

```bash
edka registries add ghcr --type github-registry --url ghcr.io --username octocat
edka registries add hub --type docker-hub --username acme
printf %s "$GHCR_TOKEN" | edka registries add ghcr --replace
```

## `edka registries apply <registry>`

Let a cluster pull from a registry.

Apply a registry to the linked or selected cluster. Edka creates the registry's
pull secret in every namespace.

Examples:

```bash
edka registries apply ghcr
edka registries apply ghcr --cluster production
```

## `edka registries default [registry]`

Show or set a cluster's default registry.

Without an argument, show the default registry of the linked or selected
cluster: the one a Git deployment builds to and pulls from when it names none.

With a registry, make it the default, which also applies it to the cluster.
--own makes the cluster's own registry the default, and --none leaves the
cluster without one.

Flags:

- `--none`: Leave the cluster without a default registry
- `--own`: Make the cluster's own registry the default

Examples:

```bash
edka registries default
edka registries default ghcr
edka registries default --own
edka registries default --none
```

## `edka registries delete <registry>`

Delete a registry's credentials.

Delete the credentials of a registry from the organization. A registry that a
cluster still uses is refused: take it from each cluster first, with
`edka registries remove <registry> --cluster <cluster>`.

Examples:

```bash
edka registries delete ghcr
edka registries delete ghcr --yes
```

## `edka registries delete-tags <repository> <tag>...`

Delete tags from a cluster's own registry.

Delete tags of a repository in the registry that runs in the linked or selected
cluster, after confirmation or with --yes. A deployment that runs a deleted
image keeps running, and can no longer pull it.

The registry deletes an image, and with it every tag of that image. When a tag
you did not name shares an image with one you named, Edka refuses, and --force
deletes those tags too.

Flags:

- `--force`: Also delete the tags that share an image with the named ones

Examples:

```bash
edka registries delete-tags api v1 v2
edka registries delete-tags api latest --force --yes
```

## `edka registries get <registry>`

Show a registry and the clusters that use it.

Examples:

```bash
edka registries get ghcr
edka registries get ghcr --json
```

## `edka registries images`

Show a cluster's own registry and the images it holds.

Show the registry that runs in the linked or selected cluster: its address
inside the cluster, its pods, its storage, and each repository with how many
tags it has. `edka registries tags <repository>` lists the tags.

Examples:

```bash
edka registries images
edka registries images --cluster production --json
```

## `edka registries list`

List the organization's registries.

List the registries of the organization. With a linked or selected cluster, the
IN CLUSTER column says how that cluster uses each one: whether it is the
default, and the state of its pull secrets. A registry the cluster uses whose
credentials were deleted is listed with the type Edka last knew.

Examples:

```bash
edka registries list
edka registries list --cluster production --json
```

## `edka registries remove <registry>`

Take a registry from a cluster.

Remove a registry from the linked or selected cluster. Edka deletes its pull
secret from every namespace, so the cluster can no longer pull the private
images of that registry. A registry that was the cluster's default leaves the
cluster without one. The credentials stay in the organization.

Examples:

```bash
edka registries remove ghcr
edka registries remove ghcr --cluster production --yes
```

## `edka registries tags <repository>`

List the tags of a repository in a cluster's own registry.

Examples:

```bash
edka registries tags api
edka registries tags acme/api --cluster production --json
```

Every command also takes the global flags in SKILL.md.
