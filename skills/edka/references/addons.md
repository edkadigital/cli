# `edka addons`

Install, update and uninstall add-ons.

## `edka addons catalog`

List add-ons you can install.

Flags:

- `--category string`: Only add-ons in this category

Examples:

```bash
edka addons catalog
edka addons catalog --category monitoring
```

## `edka addons get [add-on]`

Show an installed add-on.

Examples:

```bash
edka addons get cert-manager
edka addons get cert-manager --cluster production --json
```

## `edka addons install <add-on>`

Install an add-on from the catalog.

Install an add-on in the linked or selected cluster, at the catalog version
unless --version names another. Edka first installs the add-ons it depends on.

Installing an add-on that failed retries it. With --wait, progress goes to
stderr until the add-on is installed.

Flags:

- `--version string`: Chart version to install (default: the catalog version)
- `--wait`: Wait until the add-on is installed
- `--wait-timeout duration`: Maximum wait for each add-on (default 15m0s)

Examples:

```bash
edka addons install reflector --wait
edka addons install cert-manager --cluster staging --version 1.16.2
```

## `edka addons list`

List installed add-ons and their updates.

List installed add-ons in the linked or selected cluster, or in every cluster
when none is linked. Use --all to include every cluster.

UPDATE shows the catalog version when it is newer than the installed one.
Update an add-on with `edka addons update <add-on>`.

Flags:

- `--all`: List add-ons in every cluster, not only the linked one
- `--outdated`: Only add-ons with a newer version in the catalog

Examples:

```bash
edka addons list
edka addons list --outdated --cluster production
edka addons list --all --json
```

## `edka addons uninstall <add-on>`

Uninstall an add-on from its cluster.

Uninstall an add-on after confirmation, or with --yes. Edka refuses to
uninstall a required add-on, or one that another add-on, an app, a database or
a domain still uses. The error names what uses it.

With --wait, progress goes to stderr until the add-on is gone.

Flags:

- `--wait`: Wait until the add-on is gone
- `--wait-timeout duration`: Maximum wait for each add-on (default 15m0s)

Examples:

```bash
edka addons uninstall reflector
edka addons uninstall reflector --cluster staging --yes --wait
```

## `edka addons update [add-on]`

Update add-ons to the catalog version.

Update an installed add-on to the catalog version, or to the one --version
names. The add-on keeps its configuration. The command asks for confirmation
and names both versions; --yes skips it. When the add-on already runs the
catalog version, nothing is sent.

--all updates every add-on in the linked or selected cluster that has a newer
catalog version. It lists them with both versions and asks once. An add-on
updates after the add-ons it depends on. The command names the add-ons it
leaves out and why: one that failed or is mid-operation, and one the console
manages from Registries, Gateway or Actions.

With --wait, progress goes to stderr until the update is installed. With --all,
each update finishes before the next starts, and a failure stops the rest.

Flags:

- `--all`: Update every add-on in the cluster that has a newer catalog version
- `--version string`: Chart version to update to (default: the catalog version)
- `--wait`: Wait until the add-on is updated
- `--wait-timeout duration`: Maximum wait for each add-on (default 15m0s)

Examples:

```bash
edka addons update cert-manager --wait
edka addons update cert-manager --cluster production --version 1.16.2 --yes
edka addons update --all --cluster production --wait
```

Every command also takes the global flags in SKILL.md.
