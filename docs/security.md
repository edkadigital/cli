# Security model

The CLI receives organization-bound OAuth credentials, not browser cookies or
passwords. Consent grants read access and optionally write access. Existing Edka
roles and cluster permissions remain authoritative, and are evaluated on every
request. Tokens cannot cross from MCP to CLI because audiences and scopes differ.
Browser identity and platform administration endpoints are excluded from CLI
access. Existing identity step-up requirements remain enforced.

An organization can ask for a passkey check before sensitive actions. The CLI
never handles the passkey. At a terminal, a command that Edka refuses for the
check opens it in the Edka console in your browser, prints the address, and waits
up to 5 minutes for you to confirm there. A command without a terminal, in CI, or
with `--no-input` starts the check, prints the address and stops. `edka verify`
starts a check and waits for it. A confirmed check lets sensitive actions from the
same CLI login run for 5 minutes. The CLI opens only http and https addresses that
Edka returns.

PKCE verifiers and states use 256 bits of cryptographic randomness. Authorization
codes are accepted only on a loopback listener after state and issuer validation.
The callback never renders codes or tokens. Access/refresh token exchanges use
endpoints on the explicitly configured API origin. HTTPS is mandatory except for
loopback development. Authenticated requests refuse HTTP redirects, and secrets
are never forwarded to another origin. Request paths start with `/api/`, and a
path with a `.` or `..` segment is refused. The CLI does not log credential values or
include them in project context. `--debug` prints the method, URL, status, time and
request ID of each request, and no header or body. The CLI sends tokens, codes and
secret values in headers and bodies only. Response sizes are capped at 32 MiB.

Credential storage defaults to the system keyring. If it is unavailable, `auto`
reports its file fallback; `--credential-store keyring` requires keyring storage.
`--credential-store file` chooses private files directly. On Unix, files use mode
0600, directories use 0700, and updates use same-directory temporary files plus
rename. Windows uses the operating system's user-directory ACLs; use keyring mode
when strict credential-store policy is required. Configuration never contains
credentials, and `.edka.json` contains only profile/origin/resource IDs.

Access tokens expire after 15 minutes, and refresh tokens rotate. `edka logout`
revokes the refresh token, then deletes the saved credentials; `logout --local`
deletes them without asking Edka. An access token issued before a revocation works
until it expires, but a user who is disabled or leaves the organization fails the
next request. A token is bound to one organization, and a request can't switch it to
another.

`EDKA_TOKEN` supports short-lived automation and is never saved. Keep access tokens
out of shell arguments, repository files and logs. Edka issues no long-lived
machine-to-machine CLI tokens. The CLI collects no telemetry and runs no shell
scripts.

A GitHub Actions job signs in with a trust instead of a stored secret. With
`EDKA_TRUST` or `--trust` set to a trust's ID, the CLI asks the runner for an OIDC
token through `ACTIONS_ID_TOKEN_REQUEST_URL`, with the API origin as its audience,
and Edka exchanges it for an access token of the trust. The runner's request token
goes only to that URL, over HTTPS (HTTP only on loopback), and never through a
redirect. The access token lasts 15 minutes and stays in memory. The CLI exchanges
a new GitHub token two minutes before it expires, so a long `--wait` keeps working,
and saves nothing. Edka accepts each GitHub token once and checks its signature,
issuer, audience, expiry, repository ID, owner ID, ref, environment and workflow
against the trust. A trust's token reaches only the routes of `edka apps publish`
and `edka apps update`, for the one app the trust names, and never passes a
passkey check. `EDKA_TOKEN` and `EDKA_TRUST` together are refused, and so is
`edka login` while `EDKA_TRUST` is set.

The CLI replaces its binary only when `edka upgrade` runs. The upgrade reads the
latest release of github.com/edkadigital/cli over HTTPS, without a token. It follows a
redirect only over HTTPS to github.com or a host of githubusercontent.com, where
GitHub keeps the files of a release. It checks the archive against the SHA-256 in
the release's `checksums.txt`, unpacks the binary into the directory of the old one,
and replaces the old binary last. A mismatch replaces nothing. The checksum and the
archive come from the same release, so the check finds a damaged or cut-off
download. Whether the release is Edka's rests on GitHub's TLS certificate and on who
may publish releases in the repository. The archives are not signed.

The release workflow signs the macOS binary in each archive with a Developer ID
certificate of the Apple team `U8GNBT24CW`, and Apple notarizes it, so macOS doesn't
block a copy that a browser downloaded. `codesign -dv edka` prints the team as
`TeamIdentifier=U8GNBT24CW`, and the workflow fails a release signed by any other team.
The job that signs it runs only Apple's `codesign` and `notarytool` on the archives of
the build. It alone reads the certificate and the App Store Connect key, which are
secrets of an environment that only release tags may use. `edka upgrade` and the
install scripts don't check the signature.

`install.sh` and `install.ps1` check an archive the same way, against the
`checksums.txt` of its release, and install nothing on a mismatch. With curl,
`install.sh` stays on HTTPS, redirects included. `edka.io/install.sh` and
`edka.io/install.ps1` redirect to the scripts attached to the latest release.
The Homebrew formula names the SHA-256 of each archive, and Homebrew checks the
download against it. The release workflow writes the formula from the
release's `checksums.txt` into edkadigital/homebrew-tap, with a token that may
write to that repository alone.

A released binary also asks github.com for the tag of the latest release once a day,
while a command runs at a terminal, and prints a notice when that release is newer. The
request has no token and no cookie, and names no account, cluster or project. It
installs nothing. It does not run in CI, without a terminal on stdout and stderr, in a
binary built from a checkout, or with `EDKA_NO_UPDATE_CHECK=1`. The time of the last
lookup is in `update-check.json` in the configuration directory.

A per-profile lock serializes refresh rotation and ensures the replacement refresh
token is saved before a command uses it. Mutations are not retried automatically,
with two exceptions. `env set` and `env unset` send a settings change again when Edka
refused it because the deployment changed after they read it. A request that Edka
refused for a passkey check is sent once more after you confirm the check. Each
refusal saves nothing, so sending the request again cannot apply it twice.
Deletes and known destructive lifecycle routes require explicit confirmation in
interactive terminals and `--yes` in CI. The route catalog marks those routes. A raw
path's query string, percent-encoding and letter case do not change which route it
names. Confirmation is a UX safeguard; server permissions and step-up remain the
security boundary.

`env set --secret` reads values at a hidden prompt or from stdin, never from
arguments, and sends them only in the settings request. `env` lists secrets by name.
A prompted value that contains an escape character is refused, because a terminal's
late answer to a query would otherwise become part of the secret.
`apps install` refuses secret settings in `--set` and takes them from a hidden prompt
or a `--data` file. `registries add` reads a registry's password the same way, at a
hidden prompt or from stdin, and Edka never returns it.
`domains add --dns-provider cloudflare` and `domains update --replace-token` read a
Cloudflare API token from `EDKA_CLOUDFLARE_API_TOKEN`, from stdin or at a hidden
prompt, never from an argument, and Edka never returns it either. They leave
`CLOUDFLARE_API_TOKEN` alone: wrangler and Terraform read it, and it often holds a
token for more zones than the cluster needs. `secrets set` reads each
value at a hidden prompt, from stdin or from a file, never from an argument, and
`secrets list` and `secrets get` show keys only. The terminal stops echoing before a hidden prompt appears.

Server-controlled values displayed as terminal text have escape and control
sequences removed. JSON preserves the API response data through JSON encoding.
Downloads are written atomically to private files. Raw output is intended for
machine consumers and does not sanitize bytes. Shell completion offers a name
only when it has no control characters, and the resource's ID otherwise.

Report a vulnerability privately, as [SECURITY.md](../SECURITY.md) describes. Keep
live credentials out of issues, test fixtures and reports.
