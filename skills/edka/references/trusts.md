# `edka trusts`

Let GitHub Actions workflows sign in without a secret.

## `edka trusts create <name>`

Let a GitHub Actions workflow publish and update one app.

Create a trust: jobs of --repository that run on one of the --ref branches
or tags, and in one of the --environment environments when you name any, may
sign in with GitHub's OIDC token. A ref ending in * matches every ref that
starts with it, such as refs/tags/v*. --workflow limits the trust to the jobs
of one workflow file.

The workflow may publish new versions of the custom app --app names, and
update its installed instances: on every cluster, or on the --clusters you
list. --allow keeps one of the two.

Edka reads the repository's ID and owner ID from GitHub, through the GitHub
integration or as a public repository, and accepts tokens of that repository
only, even after a rename. For a repository Edka can't see, pass
--repository-id and --owner-id; `gh api repos/<owner>/<name> --jq '.id, .owner.id'`
prints them.

Flags:

- `--allow strings`: What the workflow may do: publish, update (default [publish,update])
- `--app string`: Slug of the custom app the workflow publishes and updates
- `--clusters strings`: Clusters whose instances the workflow may update; every cluster when left out
- `--environment strings`: GitHub environment the jobs run in; repeatable
- `--owner-id string`: Numeric ID of the owner of a repository Edka can't see
- `--ref strings`: Branch or tag ref the jobs run on, such as refs/heads/main; repeatable
- `--repository string`: GitHub repository as owner/name
- `--repository-id string`: Numeric ID of a repository Edka can't see
- `--workflow string`: Workflow file whose jobs may sign in, such as .github/workflows/release.yml

Examples:

```bash
edka trusts create releases --repository acme/api --ref refs/heads/main --app api
edka trusts create tagged --repository acme/api --ref 'refs/tags/v*' --environment production --app api --clusters production
edka trusts create publish-only --repository acme/api --ref refs/heads/main --app api --allow publish --workflow .github/workflows/release.yml
```

## `edka trusts delete <trust>`

Stop a workflow from signing in.

Delete a trust. Workflows that use it can no longer sign in, and the access
tokens they hold stop working at once.

Examples:

```bash
edka trusts delete releases
edka trusts delete releases --yes
```

## `edka trusts get <trust>`

Show a trust and the job settings that use it.

Examples:

```bash
edka trusts get releases
edka trusts get 7d1e4c62-3c4b-4b8e-9b1f-0a4f2c1d9e01 --json
```

## `edka trusts list`

List the workflows that may sign in.

Examples:

```bash
edka trusts list
edka trusts list --json
```

Every command also takes the global flags in SKILL.md.
