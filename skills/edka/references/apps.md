# `edka apps`

Install and manage apps, and publish and share your own.

## `edka apps catalog [app]`

List apps you can install, or an app's settings.

List the apps you can install. Name an app to list its settings, with their
defaults and whether they are required, for `edka apps install <app> --set key=value`.

Flags:

- `--category string`: Only apps in this category

Examples:

```bash
edka apps catalog
edka apps catalog --category databases
edka apps catalog umami
```

## `edka apps custom`

List the custom apps your organization published.

Examples:

```bash
edka apps custom
edka apps custom --json
```

## `edka apps diagnose [app]`

Find out why an app is not running.

Read an installed app as Edka records it, its pods, the Kubernetes events and the
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
edka apps diagnose strapi
edka apps diagnose strapi --tail 100
edka apps diagnose strapi --json
```

## `edka apps get [app]`

Show an installed app.

Examples:

```bash
edka apps get strapi
edka apps get strapi --json
```

## `edka apps init <slug>`

Write the starting files of a new custom app.

Write the starting files of a custom app: a template whose standard
block leaves the namespace, image, resources, placement and gateway parts to
Edka, and a small Helm chart. The files pass `edka apps validate` as they are.
Change what the app needs, then validate and publish.

--with adds parts to the app: access for a hostname on the gateway,
storage for a data volume, postgres for a PostgreSQL database.

Flags:

- `--data-path string`: Where the app keeps its data in the container, with --with storage
- `--description string`: One sentence about the app
- `--dir string`: Directory to write; defaults to the slug
- `--image string`: Image repository without a tag, such as ghcr.io/usememos/memos
- `--name string`: Name shown in the catalog; defaults to the slug
- `--port int`: Port the container listens on
- `--tag string`: Image tag to start from
- `--with strings`: Parts to add: access, storage, postgres

Examples:

```bash
edka apps init memos --image ghcr.io/usememos/memos --tag 0.25.1 --port 5230 --with access,storage
edka apps init my-api --image ghcr.io/acme/api --port 8080 --with access,postgres --dir packages/my-api
```

## `edka apps install <app>`

Install an app from the catalog into the linked or selected cluster. Settings you
leave out take the catalog's defaults, and generated passwords are created for
you. List an app's settings with `edka apps catalog <app>`.

In a terminal, install asks for what the app needs and --set left out: the
PostgreSQL instance, database and user it uses, whether to expose it, its traffic
class, and a hostname from the cluster's domains. It then shows the settings and
asks to go ahead; --yes skips that question.

Scripts and agents get no prompts. Settings with a list of options take the
option's name; list them with `edka apps options <app> <setting>`. A missing
setting fails the install with an error that names it. Secrets are never taken
from --set: enter them at the prompt, or put them in a file for --data @file.

With --wait, progress goes to stderr until the app is installed.

Flags:

- `-d, --data string`: Settings as a JSON object, @file.json, or @-
- `--name string`: Name for this instance; defaults to the app's name
- `--set stringArray`: Setting as key=value, or key:=JSON
- `--wait`: Wait until the app is installed
- `--wait-timeout duration`: Maximum installation wait (default 15m0s)

Examples:

```bash
edka apps install excalidraw --wait
edka apps install umami --set postgres_instance=postgres --set hostname=stats.example.com
edka apps install n8n --name n8n-staging --data @n8n.json
```

## `edka apps list`

List installed apps in the linked or selected cluster, or in every cluster
when none is linked. Use --all to include every cluster.

Flags:

- `--all`: List apps in every cluster, not only the linked one

Examples:

```bash
edka apps list
edka apps list --cluster production
edka apps list --all --json
```

## `edka apps logs [app]`

Read an app's logs; follow with --follow.

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

## `edka apps options <app> <setting>`

List the choices for an app setting.

List what an app setting can be set to in the linked or selected cluster, such
as its PostgreSQL instances or traffic classes. A list that depends on another
setting, such as the databases of an instance, needs that setting with --set.

`edka apps install <app> --set key=value` takes the VALUE, or the LABEL's name
before any parenthesis.

Flags:

- `--set stringArray`: A setting the list depends on, as key=value

Examples:

```bash
edka apps options umami postgres_instance
edka apps options umami postgres_database --set postgres_instance=postgres --json
```

## `edka apps publish [directory]`

Publish a custom app to your organization's catalog.

Publish a custom app, or a new version of one, to the catalog of your
organization. The files are checked first and stored only when they are valid.
A new version carries a higher version in template.yaml.

Publishing changes no installed app. Each one stays on its version until
`edka apps update <app>` moves it. Needs the admin role.

Examples:

```bash
edka apps publish ./memos
edka apps publish ./memos --json
```

## `edka apps share <directory or app>`

Offer a custom app to the community catalog: check it against the rules
of the catalog, then open a pull request to edkadigital/apps from your
GitHub account. Name a package directory, or a custom app your organization
published.

The pull request is opened with the GitHub CLI, as the account `gh` is signed
in to. That account is one of the maintainers template.yaml lists. Edka
maintainers review the pull request, and a merged app is listed with a
following Edka release.

The commit carries the user.name and user.email git config sets in the
package directory, or in the current directory for a published app. Without
both, GitHub picks the commit email from the account.

The command asks for confirmation before it opens anything; --yes skips it.

Examples:

```bash
edka apps share ./memos
edka apps share memos --yes
```

## `edka apps uninstall <app>`

Uninstall an app from its cluster.

Examples:

```bash
edka apps uninstall onlinetest
edka apps uninstall onlinetest --yes
```

## `edka apps update <app>`

Move an installed custom or community app to another version.

Move an installed app to another version: the latest one, or the one
--version names. A setting you changed keeps its value, the other
settings take the defaults of the new version, and the stored secrets stay.

The command lists what the update changes, such as images, kinds of objects,
endpoints, storage, secrets and settings, then asks for confirmation; --yes
skips the question. A line marked "!" reaches beyond the namespace of the
app, asks for input, or is a setting that keeps its value while its default
changed. With --wait, progress goes to stderr until the update is installed.

Flags:

- `--version string`: Version to move to; defaults to the latest
- `--wait`: Wait until the update is installed
- `--wait-timeout duration`: Maximum wait for the update (default 15m0s)

Examples:

```bash
edka apps update memos --wait
edka apps update memos --version 1.3.0 --cluster production --yes
```

## `edka apps validate [directory]`

Check the files of a custom app against the rules Edka installs by. Each finding names
the file, the line and a fix. Nothing is stored.

--community also applies the rules for listing in the public catalog.

Flags:

- `--community`: Also apply the rules for the public catalog

Examples:

```bash
edka apps validate ./memos
edka apps validate ./memos --community
edka apps validate --json
```

Every command also takes the global flags in SKILL.md.
