# `edka domains`

Add domains, issue their certificates and delete them.

## `edka domains add <domain>`

Add a domain to a cluster.

Add a hostname or a wildcard to the linked or selected cluster. The traffic
class --class names serves it, or the cluster's default one.

A wildcard such as *.example.com is validated over DNS, and --apex makes its
certificate cover example.com too. In a terminal, a wildcard without --apex
asks whether to include it. A hostname is validated over HTTP, which
needs a public traffic class, or over DNS with --validation dns-01.

Over DNS, you point _acme-challenge under the domain at Edka with a CNAME
record. With --dns-provider cloudflare, cert-manager writes the challenge TXT
record in the domain's Cloudflare zone instead, with an API token that has Zone
Read and DNS Edit on the zone. The command reads the token from
EDKA_CLOUDFLARE_API_TOKEN, from stdin when stdin is not a terminal, or at a
hidden prompt, never from an argument. Edka checks the token against the zone
before it adds the domain, then starts issuing the certificate.

The command prints the DNS records to create. For a domain validated with a
CNAME record, create them and run `edka domains verify <domain>`.

Flags:

- `--apex`: For a wildcard, cover the hostname it is under too
- `--class string`: Traffic class that serves the domain (default: the cluster's default)
- `--dns-provider string`: Write the challenge record in the domain's zone at this DNS provider: cloudflare
- `--validation string`: How Let's Encrypt validates a hostname: http-01 or dns-01 (default: http-01)

Examples:

```bash
edka domains add '*.example.com' --apex
edka domains add app.example.com
edka domains add internal.example.com --class eg-ts --validation dns-01
printf %s "$TOKEN" | edka domains add '*.example.org' --dns-provider cloudflare
```

## `edka domains delete <domain>`

Delete a domain and its certificate.

Delete a domain from its cluster, with its certificate and its HTTPS listener.
Hostnames under it stop being served over HTTPS. Edka refuses while a Codex
agent or environment uses the domain.

Examples:

```bash
edka domains delete app.example.com
edka domains delete '*.example.com' --yes
```

## `edka domains get [domain]`

Show a domain, its certificate and the DNS records it needs.

Show a domain with the state of its certificate, read from the cluster, and
the DNS records it needs: the CNAME that validates a certificate over DNS, and
the records that point the domain at its traffic class. A domain added with
--dns-provider cloudflare needs no CNAME. For it, get shows the Cloudflare zone
and the challenge record cert-manager writes there.

Examples:

```bash
edka domains get '*.example.com'
edka domains get app.example.com --json
```

## `edka domains list`

List domains and their certificates.

List the domains of the linked or selected cluster, or of every cluster when
none is linked. Use --all to include every cluster. CERTIFICATE is the state
Edka last recorded; `edka domains get` reads it from the cluster.

Flags:

- `--all`: List domains in every cluster, not only the linked one

Examples:

```bash
edka domains list
edka domains list --all --json
```

## `edka domains update <domain>`

Change a wildcard's apex or a domain's Cloudflare token.

Change whether the certificate of a wildcard covers the hostname it is under:
example.com for *.example.com. --apex includes it, and --apex=false leaves it
out. An included apex needs its own DNS records, which `edka domains get` lists.

--replace-token replaces the Cloudflare API token of a domain added with
--dns-provider cloudflare. The command reads the new token from
EDKA_CLOUDFLARE_API_TOKEN, from stdin when stdin is not a terminal, or at a
hidden prompt, never from an argument. Edka checks it against the zone before
it stores it, and the certificate renews with it from then on.

Flags:

- `--apex`: Cover the hostname the wildcard is under
- `--replace-token`: Replace the Cloudflare API token, read from EDKA_CLOUDFLARE_API_TOKEN, stdin or a prompt

Examples:

```bash
edka domains update '*.example.com' --apex
edka domains update '*.example.com' --apex=false
printf %s "$TOKEN" | edka domains update '*.example.org' --replace-token
```

## `edka domains verify <domain>`

Check a domain's DNS record and issue its certificate.

Check the CNAME record of a domain validated over DNS. When the record is in
place, Edka issues the certificate. Until then it keeps checking, and
`edka domains get` shows the record to create. For a domain added with
--dns-provider cloudflare, Edka checks that no CNAME record blocks the
challenge name in the Cloudflare zone, then issues the certificate.

With --wait, the DNS and certificate status go to stderr until the certificate
is ready.

On a cluster with an ingress controller, --namespace names each namespace that
gets a copy of a wildcard certificate. A Gateway API class needs none.

Flags:

- `--namespace stringArray`: Namespace that gets a copy of the certificate, with an ingress controller
- `--wait`: Wait until the certificate is ready
- `--wait-timeout duration`: Maximum wait for the certificate (default 10m0s)

Examples:

```bash
edka domains verify '*.example.com' --wait
edka domains verify '*.example.com' --namespace default --namespace shop
```

Every command also takes the global flags in SKILL.md.
