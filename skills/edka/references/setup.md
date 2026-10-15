# Sign-in and project link

Sign in, link a directory to a cluster and deployment, and confirm your identity.

## `edka link`

Choose a cluster and deployment for this directory.

Save a default cluster, and optionally a deployment, for commands run in this
directory and below it, up to the Git repository root. The file holds IDs only.

Without --cluster, a picker lists your clusters. In a terminal, a picker then
offers the cluster's deployments; choose "Cluster only" to skip.

Flags:

- `--no-deployment`: Link only the cluster

Examples:

```bash
edka link
edka link --cluster production --deployment api
edka link --cluster staging --no-deployment
```

## `edka login`

Sign in securely through your browser.

Flags:

- `--callback-port int`: Loopback callback port (0 chooses an available port)
- `--no-browser`: Print the authorization URL without opening it
- `--read-only`: Request read access only

Examples:

```bash
edka login
edka login --read-only
edka login --profile local --api-url http://localhost:8080
```

## `edka logout`

Revoke access and remove saved credentials.

Flags:

- `--local`: Remove local credentials without server revocation

## `edka verify`

Confirm your identity with a passkey in the Edka console. An organization can
ask for this check before sensitive actions, such as a kubeconfig download.
After you confirm, sensitive actions from this login work for 5 minutes.

verify opens the console in your browser, prints its address and waits up to 5
minutes for you to confirm. At a terminal, a command that needs the check runs
it on its own. A command without a terminal does not wait, so run verify before
a script.

Flags:

- `--no-browser`: Print the console address without opening it

Examples:

```bash
edka verify
edka verify --no-browser
```

## `edka whoami`

Show your identity and authorized organization.

Every command also takes the global flags in SKILL.md.
