package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

func isWildcard(m map[string]any) bool { return m["is_wildcard"] == true }

// apexOf is the hostname a wildcard domain is under: example.com for *.example.com.
func apexOf(m map[string]any) string { return strings.TrimPrefix(text(m, "domain"), "*.") }

// validationOf is how Let's Encrypt validates a domain. Edka validates a
// wildcard over DNS, and a hostname over HTTP unless it was added otherwise.
func validationOf(m map[string]any) string {
	if method := text(m, "validation_method"); method != "" {
		return method
	}
	if isWildcard(m) {
		return "dns-01"
	}
	return "http-01"
}

// viaCloudflare says whether a domain validates through its own Cloudflare
// zone: cert-manager writes the challenge TXT record there with the domain's
// API token, and no CNAME record points the challenge at Edka.
func viaCloudflare(m map[string]any) bool { return text(m, "dns_provider") == "cloudflare" }

// challengeName is the name Let's Encrypt reads to validate a domain over DNS.
func challengeName(m map[string]any) string { return "_acme-challenge." + apexOf(m) }

// cloudflareTokenEnv is the variable a Cloudflare API token is read from. It is
// not CLOUDFLARE_API_TOKEN, which wrangler and Terraform read and which often
// holds a token for more zones than the domain's.
const cloudflareTokenEnv = "EDKA_CLOUDFLARE_API_TOKEN"

// cloudflareToken reads a Cloudflare API token from EDKA_CLOUDFLARE_API_TOKEN,
// from a stdin that is not a terminal, or at a hidden prompt. It is never an
// argument, which shell history and the process list would keep. retry is the
// command as the user ran it, which a hint pipes the token to.
func (a *App) cloudflareToken(ctx context.Context, domain, retry string) (string, error) {
	if token := strings.TrimSpace(os.Getenv(cloudflareTokenEnv)); token != "" {
		a.message("Using the Cloudflare API token in %s", cloudflareTokenEnv)
		return token, nil
	}
	token, err := a.oneSecret(ctx, "the Cloudflare API token", fmt.Sprintf("Cloudflare API token for %s: ", ui.Clean(domain)), fmt.Errorf("set %s, or pipe the token on stdin: %s", cloudflareTokenEnv, pipeHint("TOKEN", retry)))
	// Piping the token, or the variable, helps only when stdin held nothing.
	if errors.As(err, new(emptyStdin)) {
		return "", fmt.Errorf("%w; pipe it, or set %s", err, cloudflareTokenEnv)
	}
	return token, err
}

// tlsOf returns the certificate and DNS status Edka last recorded for a domain.
func tlsOf(m map[string]any) (certificate, dns map[string]any) {
	status, _ := m["tls_status"].(map[string]any)
	certificate, _ = status["status"].(map[string]any)
	dns, _ = status["dns"].(map[string]any)
	return certificate, dns
}

// stateLine is a status as "state: message".
func stateLine(status map[string]any) string {
	state, message := text(status, "state"), first(text(status, "message"), text(status, "reason"))
	if state == "" || message == "" {
		return state + message
	}
	return state + ": " + message
}

// expiry is the day a certificate expires, in local time.
func expiry(status map[string]any) string {
	t, err := time.Parse(time.RFC3339, text(status, "not_after"))
	if err != nil {
		return ""
	}
	return t.Local().Format("2006-01-02")
}

var domainColumns = []ui.Column{
	ui.Field("DOMAIN", "domain"),
	ui.Field("CLASS", "ingress_class"),
	{Header: "VALIDATION", Value: validationOf},
	{Header: "CERTIFICATE", Value: func(m map[string]any) string {
		certificate, _ := tlsOf(m)
		return text(certificate, "state")
	}},
	{Header: "EXPIRES", Value: func(m map[string]any) string {
		certificate, _ := tlsOf(m)
		return expiry(certificate)
	}},
	ui.Field("CLUSTER", "cluster_name"),
}

func domainNames(m map[string]any) []string { return []string{text(m, "domain")} }
func domainDetail(m map[string]any) string {
	certificate, _ := tlsOf(m)
	return details(text(m, "ingress_class"), text(certificate, "state"), text(m, "cluster_name"))
}

// resolveDomain selects a domain by ID or name.
func (a *App) resolveDomain(ctx context.Context, cmd *cobra.Command, target string) (*candidate, string, error) {
	c, err := a.resolveScoped(ctx, cmd, "domain", "domains", target, "no domains; add one with `edka domains add '*.example.com'`", domainNames, domainDetail)
	if err != nil {
		return nil, "", err
	}
	path, err := clusterItemPath(c, "domains")
	return c, path, err
}

// domainRecords lists the DNS records a domain needs: the record that
// validates its certificate, then one record for each address of its traffic
// class, for the domain and for an apex it includes. The records of a wildcard
// name are optional. They send every name under it that has no record of its
// own to the cluster, so a zone with other hosts points only the hostnames the
// cluster serves. A domain validated through its Cloudflare zone has no
// validation record to create: a CNAME at the challenge name would block the
// TXT record cert-manager writes there.
func domainRecords(domain, class, delegation map[string]any) []map[string]any {
	records := []map[string]any{}
	if value := text(delegation, "record_value"); value != "" && !viaCloudflare(domain) {
		records = append(records, map[string]any{"name": text(delegation, "record_name"), "type": first(text(delegation, "record_type"), "CNAME"), "value": value, "purpose": "certificate", "required": true})
	}
	names := []string{text(domain, "domain")}
	if isWildcard(domain) && domain["include_apex"] == true {
		names = append(names, apexOf(domain))
	}
	// A Tailscale class is reached by its name, not by its tailnet address.
	tailscale := text(class, "exposure_mode") == "tailscale-byod"
	for _, name := range names {
		optional := strings.HasPrefix(name, "*.")
		purpose := "traffic"
		if optional {
			purpose = "traffic, optional"
		}
		for _, kind := range []string{"A", "AAAA", "CNAME"} {
			for _, item := range asList(class["load_balancer_ips"]) {
				address, _ := item.(string)
				if address = strings.TrimSpace(address); address == "" || recordType(address) != kind || tailscale && kind != "CNAME" {
					continue
				}
				record := map[string]any{"name": name, "type": kind, "value": address, "purpose": purpose, "required": !optional}
				if !slices.ContainsFunc(records, func(r map[string]any) bool {
					return r["name"] == name && r["type"] == kind && r["value"] == address
				}) {
					records = append(records, record)
				}
			}
		}
	}
	return records
}

// recordType is the DNS record that points a name at an address.
func recordType(address string) string {
	ip := net.ParseIP(address)
	switch {
	case ip == nil:
		return "CNAME"
	case ip.To4() != nil:
		return "A"
	}
	return "AAAA"
}

// domainView reads what `domains get` shows: the domain with its live
// certificate status, and the DNS records it needs. When Edka can't read the
// live status, the view holds the status it last recorded.
func (a *App) domainView(ctx context.Context, row map[string]any) (map[string]any, error) {
	cluster, err := safeID(text(row, "cluster_id"))
	if err != nil {
		return nil, err
	}
	id, err := safeID(text(row, "id"))
	if err != nil {
		return nil, err
	}
	view := map[string]any{"domain": row}
	certificate, dns := tlsOf(row)
	view["status"], view["dns"] = certificate, dns
	response, err := a.request(ctx, "GET", "/api/clusters/"+cluster+"/domains/"+id+"/wildcard-tls", nil, nil)
	if err == nil {
		if live, err := identityData(response.Body); err == nil {
			view = live
			if view["domain"] == nil {
				view["domain"] = row
			}
		}
	} else {
		reason, _, _ := strings.Cut(err.Error(), "\n")
		a.message("Could not read the certificate from the cluster, showing the last status Edka recorded: %s", ui.Clean(reason))
	}
	class := map[string]any{}
	if classes, err := a.objects(ctx, "/api/clusters/"+cluster+"/ingress-controllers"); err == nil {
		if found := recordWith(classes, "ingress_class", text(row, "ingress_class")); found != nil {
			class = found
		}
	}
	delegation, _ := view["delegation"].(map[string]any)
	domain, _ := view["domain"].(map[string]any)
	view["traffic_class"] = class
	view["dns_records"] = domainRecords(domain, class, delegation)
	// cert-manager writes this record itself, so it is not among the records to create.
	if viaCloudflare(domain) {
		view["challenge"] = map[string]any{"provider": "cloudflare", "zone_name": text(domain, "dns_zone_name"), "record_name": challengeName(domain), "record_type": "TXT"}
	}
	return view, nil
}

// showDomain prints a domain view: the summary, then the DNS records.
func (a *App) showDomain(view map[string]any, clusterName string) error {
	if a.output != "table" {
		return ui.Render(a.Out, jsonBody(map[string]any{"data": view}), a.output, false)
	}
	domain, _ := view["domain"].(map[string]any)
	class, _ := view["traffic_class"].(map[string]any)
	certificate, _ := view["status"].(map[string]any)
	dns, _ := view["dns"].(map[string]any)
	challenge, _ := view["challenge"].(map[string]any)
	apex := ""
	if isWildcard(domain) {
		apex = "not included"
		if domain["include_apex"] == true {
			apex = apexOf(domain) + " included"
		}
	}
	trafficClass := text(domain, "ingress_class")
	if name := text(class, "name"); name != "" {
		trafficClass += " (" + strings.Trim(name+", "+text(class, "exposure_scope"), ", ") + ")"
	}
	if err := ui.Fields(a.Out, [][2]string{
		{"Domain", text(domain, "domain")},
		{"Apex", apex},
		{"Traffic class", trafficClass},
		{"Validation", validationOf(domain)},
		{"Cloudflare zone", text(challenge, "zone_name")},
		{"Challenge record", strings.TrimSpace(text(challenge, "record_name") + " " + text(challenge, "record_type"))},
		{"Certificate", stateLine(certificate)},
		{"Expires", expiry(certificate)},
		{"DNS", stateLine(dns)},
		{"Cluster", clusterName},
		{"Created", when(domain["created_at"])},
		{"ID", text(domain, "id")},
	}, a.color); err != nil {
		return err
	}
	if records, _ := view["dns_records"].([]map[string]any); len(records) > 0 {
		fmt.Fprintln(a.Out)
		if err := ui.Table(a.Out, []ui.Column{ui.Field("RECORD", "name"), ui.Field("TYPE", "type"), ui.Field("VALUE", "value"), ui.Field("FOR", "purpose")}, records, a.color); err != nil {
			return err
		}
		if slices.ContainsFunc(records, func(r map[string]any) bool { return r["required"] == false }) {
			a.message("The %s records are optional. They send every name under %s that has no record of its own to the cluster. With other hosts in the zone, point only the hostnames the cluster serves at the same addresses.", ui.Clean(text(domain, "domain")), ui.Clean(apexOf(domain)))
		}
	}
	if challenge != nil {
		a.message("cert-manager writes the TXT record at %s with the Cloudflare API token, for each issuance and renewal. A CNAME record at that name blocks it.", ui.Clean(text(challenge, "record_name")))
	}
	return nil
}

// askApex asks whether a wildcard's certificate covers its apex too, as the
// console's Include switch does.
func (a *App) askApex(apex string) (bool, error) {
	return a.askYesNo(fmt.Sprintf("Include %s? It shares the DNS validation and the certificate", ui.Clean(apex)), false)
}

func (a *App) addDomains(root *cobra.Command) {
	domains := &cobra.Command{Use: "domains", Aliases: []string{"domain"}, Short: "Add domains, issue their certificates and delete them", Long: "Manage the domains of a cluster. A domain is a hostname, such as\napp.example.com, or a wildcard, such as *.example.com, that a traffic class of\nthe cluster serves over HTTPS with a Let's Encrypt certificate.\n\nA wildcard is validated over DNS: you create one CNAME record, then run\n`edka domains verify <domain>`. With\n`edka domains add <domain> --dns-provider cloudflare`, cert-manager writes the\nchallenge TXT record in your Cloudflare zone with an API token instead. A\nhostname is validated over HTTP once it points at the cluster.\n`edka domains get` lists the DNS records a domain needs. Quote a wildcard, so\nthe shell leaves the * alone.", GroupID: "resources", Example: "  edka domains list\n  edka domains add '*.example.com' --apex\n  edka domains get '*.example.com'\n  edka domains verify '*.example.com' --wait\n  edka domains add '*.example.org' --dns-provider cloudflare\n  edka domains add app.example.com\n  edka domains delete app.example.com"}
	a.strictGroup(domains)
	var all bool
	list := &cobra.Command{Use: "list", Short: "List domains and their certificates", Long: "List the domains of the linked or selected cluster, or of every cluster when\nnone is linked. Use --all to include every cluster. CERTIFICATE is the state\nEdka last recorded; `edka domains get` reads it from the cluster.", Args: cobra.NoArgs, Example: "  edka domains list\n  edka domains list --all --json", RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.clusterScopedRows(cmd.Context(), all, "domains")
		if err != nil {
			return err
		}
		return a.renderRows(rows, domainColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List domains in every cluster, not only the linked one")
	get := &cobra.Command{Use: "get [domain]", Short: "Show a domain, its certificate and the DNS records it needs", Long: "Show a domain with the state of its certificate, read from the cluster, and\nthe DNS records it needs: the CNAME that validates a certificate over DNS, and\nthe records that point the domain at its traffic class. A domain added with\n--dns-provider cloudflare needs no CNAME. For it, get shows the Cloudflare zone\nand the challenge record cert-manager writes there.", Args: cobra.MaximumNArgs(1), Example: "  edka domains get '*.example.com'\n  edka domains get app.example.com --json", RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := a.resolveDomain(cmd.Context(), cmd, argument(args))
		if err != nil {
			return err
		}
		view, err := a.domainView(cmd.Context(), c.Record)
		if err != nil {
			return err
		}
		return a.showDomain(view, text(c.Record, "cluster_name"))
	}}

	var class, validation, dnsProvider string
	var apex bool
	add := &cobra.Command{Use: "add <domain>", Short: "Add a domain to a cluster", Long: "Add a hostname or a wildcard to the linked or selected cluster. The traffic\nclass --class names serves it, or the cluster's default one.\n\nA wildcard such as *.example.com is validated over DNS, and --apex makes its\ncertificate cover example.com too. In a terminal, a wildcard without --apex\nasks whether to include it. A hostname is validated over HTTP, which\nneeds a public traffic class, or over DNS with --validation dns-01.\n\nOver DNS, you point _acme-challenge under the domain at Edka with a CNAME\nrecord. With --dns-provider cloudflare, cert-manager writes the challenge TXT\nrecord in the domain's Cloudflare zone instead, with an API token that has Zone\nRead and DNS Edit on the zone. The command reads the token from\nEDKA_CLOUDFLARE_API_TOKEN, from stdin when stdin is not a terminal, or at a\nhidden prompt, never from an argument. Edka checks the token against the zone\nbefore it adds the domain, then starts issuing the certificate.\n\nThe command prints the DNS records to create. For a domain validated with a\nCNAME record, create them and run `edka domains verify <domain>`.", Args: cobra.ExactArgs(1), Example: "  edka domains add '*.example.com' --apex\n  edka domains add app.example.com\n  edka domains add internal.example.com --class eg-ts --validation dns-01\n  printf %s \"$TOKEN\" | edka domains add '*.example.org' --dns-provider cloudflare", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, name := cmd.Context(), strings.ToLower(strings.TrimSpace(args[0]))
		wildcard := strings.HasPrefix(name, "*.")
		dnsProvider = strings.ToLower(strings.TrimSpace(dnsProvider))
		switch {
		case validation != "" && validation != "dns-01" && validation != "http-01":
			return fmt.Errorf("--validation is dns-01 or http-01")
		case wildcard && validation == "http-01":
			return fmt.Errorf("a wildcard is validated over DNS; leave --validation out")
		case apex && !wildcard:
			return fmt.Errorf("--apex needs a wildcard such as '*.%s'", name)
		case dnsProvider != "" && dnsProvider != "cloudflare":
			return fmt.Errorf("--dns-provider is cloudflare")
		case dnsProvider != "" && validation == "http-01":
			return fmt.Errorf("--dns-provider validates over DNS; leave --validation out")
		}
		// The provider writes the challenge record, so a hostname is validated over DNS.
		if dnsProvider != "" && !wildcard {
			validation = "dns-01"
		}
		cluster, err := a.resolveCluster(ctx, "")
		if err != nil {
			return err
		}
		id, err := safeID(cluster.ID)
		if err != nil {
			return err
		}
		classes, err := a.objects(ctx, "/api/clusters/"+id+"/ingress-controllers")
		if err != nil {
			return err
		}
		known := []string{}
		for _, c := range classes {
			known = append(known, text(c, "ingress_class"))
			if class == "" && c["is_default"] == true {
				class = text(c, "ingress_class")
			}
		}
		switch {
		case len(known) == 0:
			return fmt.Errorf("cluster %s has no traffic class; install a Gateway API or ingress controller from Gateway in the console", ui.Clean(cluster.Name))
		case class == "":
			return fmt.Errorf("cluster %s has no default traffic class; pass --class with one of %s", ui.Clean(cluster.Name), ui.Clean(strings.Join(known, ", ")))
		case !slices.Contains(known, class):
			return fmt.Errorf("cluster %s has no traffic class %q; choose one of %s", ui.Clean(cluster.Name), class, ui.Clean(strings.Join(known, ", ")))
		}
		// In a terminal, a wildcard asks about its apex unless --apex answers,
		// or the cluster has the apex as a domain of its own.
		if wildcard && !cmd.Flags().Changed("apex") && !a.noInput && ui.IsTerminal(a.Err) {
			existing, err := a.objects(ctx, "/api/clusters/"+id+"/domains")
			if err != nil {
				return err
			}
			if apexName := strings.TrimPrefix(name, "*."); recordWith(existing, "domain", apexName) == nil {
				if apex, err = a.askApex(apexName); err != nil {
					return err
				}
			}
		}
		body := map[string]any{"domain": name, "ingress_class": class, "include_apex": apex}
		if validation != "" {
			body["validation_method"] = validation
		}
		if dnsProvider != "" {
			token, err := a.cloudflareToken(ctx, name, invocation(cmd, args))
			if err != nil {
				return err
			}
			body["dns_provider"], body["cloudflare_api_token"] = dnsProvider, token
		}
		response, err := a.request(ctx, "POST", "/api/clusters/"+id+"/domains", nil, jsonBody(body))
		if err != nil {
			return err
		}
		created, err := record(response.Body, "data")
		if err != nil {
			return err
		}
		created["cluster_id"], created["cluster_name"] = cluster.ID, cluster.Name
		a.message("✓ Added %s to cluster %s", ui.Clean(text(created, "domain")), ui.Clean(cluster.Name))
		if warning := responseWarning(response.Body); warning != "" {
			a.message("  Note: %s", ui.Clean(warning))
		}
		// An Edka API without the option ignores it and adds a domain with a CNAME record.
		if dnsProvider != "" && !viaCloudflare(created) {
			a.message("  Note: Edka added the domain without the Cloudflare API token. It validates with the CNAME record below.")
		}
		view, err := a.domainView(ctx, created)
		if err != nil {
			return err
		}
		if err := a.showDomain(view, cluster.Name); err != nil {
			return err
		}
		domain := text(created, "domain")
		switch {
		case viaCloudflare(created):
			a.message("Next: create the records. Edka issues the certificate through the Cloudflare zone; follow it with `%s`", followUp(cmd, "domains", "verify", domain, "--wait"))
		case validationOf(created) == "dns-01":
			a.message("Next: create the records, then run `%s`", followUp(cmd, "domains", "verify", domain))
		default:
			a.message("Next: create the records. Edka issues the certificate once the hostname reaches the cluster; check it with `%s`", followUp(cmd, "domains", "get", domain))
		}
		return nil
	}}
	add.Flags().StringVar(&class, "class", "", "Traffic class that serves the domain (default: the cluster's default)")
	add.Flags().StringVar(&validation, "validation", "", "How Let's Encrypt validates a hostname: http-01 or dns-01 (default: http-01)")
	add.Flags().BoolVar(&apex, "apex", false, "For a wildcard, cover the hostname it is under too")
	add.Flags().StringVar(&dnsProvider, "dns-provider", "", "Write the challenge record in the domain's zone at this DNS provider: cloudflare")
	a.completesFlag(add, "validation", values("http-01", "dns-01"))
	a.completesFlag(add, "dns-provider", values("cloudflare"))
	a.completesFlag(add, "class", a.offers(func(ctx context.Context) ([]candidate, error) {
		cluster, err := a.clusterID(ctx)
		if err != nil {
			return nil, err
		}
		classes, err := a.objects(ctx, "/api/clusters/"+cluster+"/ingress-controllers")
		choices := []candidate{}
		for _, c := range classes {
			choices = append(choices, candidate{Name: text(c, "ingress_class"), Detail: details(text(c, "name"), text(c, "exposure_scope"))})
		}
		return choices, err
	}))

	var namespaces []string
	var wait bool
	var waitTimeout time.Duration
	verify := &cobra.Command{Use: "verify <domain>", Short: "Check a domain's DNS record and issue its certificate", Long: "Check the CNAME record of a domain validated over DNS. When the record is in\nplace, Edka issues the certificate. Until then it keeps checking, and\n`edka domains get` shows the record to create. For a domain added with\n--dns-provider cloudflare, Edka checks that no CNAME record blocks the\nchallenge name in the Cloudflare zone, then issues the certificate.\n\nWith --wait, the DNS and certificate status go to stderr until the certificate\nis ready.\n\nOn a cluster with an ingress controller, --namespace names each namespace that\ngets a copy of a wildcard certificate. A Gateway API class needs none.", Args: cobra.ExactArgs(1), Example: "  edka domains verify '*.example.com' --wait\n  edka domains verify '*.example.com' --namespace default --namespace shop", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		ctx := cmd.Context()
		c, path, err := a.resolveDomain(ctx, cmd, args[0])
		if err != nil {
			return err
		}
		name := ui.Clean(c.Name)
		if validationOf(c.Record) != "dns-01" {
			return fmt.Errorf("%s is validated over HTTP: Edka issues its certificate once the hostname reaches the cluster; check it with `%s`", name, followUp(cmd, "domains", "get", c.Name))
		}
		body := map[string]any{}
		if len(namespaces) > 0 {
			body["namespaces"] = namespaces
		}
		response, err := a.request(ctx, "POST", path+"/wildcard-tls", nil, jsonBody(body))
		if err != nil {
			return err
		}
		view, err := identityData(response.Body)
		if err != nil {
			return err
		}
		p := a.startProgress()
		report := func(view map[string]any) (certificate map[string]any) {
			certificate, _ = view["status"].(map[string]any)
			dns, _ := view["dns"].(map[string]any)
			p.say("dns", "DNS: "+stateLine(dns))
			p.say("certificate", "Certificate: "+stateLine(certificate))
			return certificate
		}
		certificate := report(view)
		dns, _ := view["dns"].(map[string]any)
		if delegation, _ := view["delegation"].(map[string]any); text(dns, "state") != "ready" && text(delegation, "record_value") != "" && !viaCloudflare(c.Record) {
			a.message("Create this record at your DNS provider: %s %s %s", ui.Clean(text(delegation, "record_name")), ui.Clean(first(text(delegation, "record_type"), "CNAME")), ui.Clean(text(delegation, "record_value")))
		}
		if wait {
			waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
			defer cancel()
			// The wait ran out of time, or it stopped for another reason.
			stopped := func(err error) error {
				if waitCtx.Err() == nil {
					return err
				}
				return fmt.Errorf("the certificate for %s is not ready yet: %w; Edka keeps checking, follow it with `%s`", name, waitCtx.Err(), followUp(cmd, "domains", "get", c.Name))
			}
			for text(certificate, "state") != "ready" {
				if text(certificate, "state") == "error" {
					return fmt.Errorf("the certificate for %s failed: %s", name, first(ui.Clean(text(certificate, "message")), "Edka gave no reason"))
				}
				if err := pause(waitCtx, pollInterval); err != nil {
					return stopped(err)
				}
				response, err := a.poll(waitCtx, p, path+"/wildcard-tls", nil)
				if err != nil {
					return stopped(err)
				}
				if view, err = identityData(response.Body); err != nil {
					return err
				}
				certificate = report(view)
			}
		}
		if text(certificate, "state") == "ready" {
			valid := ""
			if day := expiry(certificate); day != "" {
				valid = ", valid until " + day
			}
			a.message("✓ The certificate for %s is ready%s", name, valid)
		} else {
			a.message("Edka keeps checking. Follow it with `%s`, or run verify with --wait", followUp(cmd, "domains", "get", c.Name))
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": view}), a.output, false)
		}
		return nil
	}}
	verify.Flags().StringArrayVar(&namespaces, "namespace", nil, "Namespace that gets a copy of the certificate, with an ingress controller")
	verify.Flags().BoolVar(&wait, "wait", false, "Wait until the certificate is ready")
	verify.Flags().DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "Maximum wait for the certificate")

	var includeApex, replaceToken bool
	update := &cobra.Command{Use: "update <domain>", Short: "Change a wildcard's apex or a domain's Cloudflare token", Long: "Change whether the certificate of a wildcard covers the hostname it is under:\nexample.com for *.example.com. --apex includes it, and --apex=false leaves it\nout. An included apex needs its own DNS records, which `edka domains get` lists.\n\n--replace-token replaces the Cloudflare API token of a domain added with\n--dns-provider cloudflare. The command reads the new token from\nEDKA_CLOUDFLARE_API_TOKEN, from stdin when stdin is not a terminal, or at a\nhidden prompt, never from an argument. Edka checks it against the zone before\nit stores it, and the certificate renews with it from then on.", Args: cobra.ExactArgs(1), Example: "  edka domains update '*.example.com' --apex\n  edka domains update '*.example.com' --apex=false\n  printf %s \"$TOKEN\" | edka domains update '*.example.org' --replace-token", RunE: func(cmd *cobra.Command, args []string) error {
		changeApex := cmd.Flags().Changed("apex")
		if !changeApex && !replaceToken {
			return fmt.Errorf("pass --apex to include the apex, --apex=false to leave it out, or --replace-token to replace the Cloudflare API token")
		}
		ctx := cmd.Context()
		c, path, err := a.resolveDomain(ctx, cmd, args[0])
		if err != nil {
			return err
		}
		name := ui.Clean(c.Name)
		switch {
		case changeApex && !isWildcard(c.Record):
			return fmt.Errorf("%s is a hostname; only a wildcard has an apex", name)
		case replaceToken && !viaCloudflare(c.Record):
			return fmt.Errorf("%s has no Cloudflare API token; only a domain added with --dns-provider cloudflare has one", name)
		}
		// The token and the apex go in separate requests, so each change is
		// reported once Edka confirms it.
		var response *api.Response
		if replaceToken {
			token, err := a.cloudflareToken(ctx, c.Name, invocation(cmd, args))
			if err != nil {
				return err
			}
			if response, err = a.request(ctx, "PATCH", path, nil, jsonBody(map[string]string{"cloudflare_api_token": token})); err != nil {
				return err
			}
			updated, _ := record(response.Body, "data")
			done := "✓ Replaced the Cloudflare API token of " + name
			if zone := first(text(updated, "dns_zone_name"), text(c.Record, "dns_zone_name")); zone != "" {
				done += ", for zone " + ui.Clean(zone)
			}
			a.message("%s", done)
		}
		if changeApex {
			apexResponse, err := a.request(ctx, "PATCH", path, nil, jsonBody(map[string]bool{"include_apex": includeApex}))
			if err != nil {
				// The new token stands: a script still gets the domain Edka confirmed with it.
				if response != nil && a.output != "table" {
					if renderErr := a.render(response); renderErr != nil {
						return renderErr
					}
				}
				return err
			}
			response = apexResponse
			if includeApex {
				a.message("✓ %s includes %s\n  Its DNS records: %s", name, ui.Clean(apexOf(c.Record)), followUp(cmd, "domains", "get", c.Name))
			} else {
				a.message("✓ %s leaves out %s", name, ui.Clean(apexOf(c.Record)))
			}
		}
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	update.Flags().BoolVar(&includeApex, "apex", false, "Cover the hostname the wildcard is under")
	update.Flags().BoolVar(&replaceToken, "replace-token", false, "Replace the Cloudflare API token, read from EDKA_CLOUDFLARE_API_TOKEN, stdin or a prompt")

	remove := &cobra.Command{Use: "delete <domain>", Short: "Delete a domain and its certificate", Long: "Delete a domain from its cluster, with its certificate and its HTTPS listener.\nHostnames under it stop being served over HTTPS. Edka refuses while a Codex\nagent or environment uses the domain.", Args: cobra.ExactArgs(1), Example: "  edka domains delete app.example.com\n  edka domains delete '*.example.com' --yes", RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveDomain(cmd.Context(), cmd, args[0])
		if err != nil {
			return err
		}
		if err := a.confirm(fmt.Sprintf("Delete domain %s from cluster %s with its certificate", c.Name, text(c.Record, "cluster_name"))); err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "DELETE", path, nil, nil)
		if err != nil {
			return err
		}
		a.message("✓ Deleted domain %s from cluster %s", ui.Clean(c.Name), ui.Clean(text(c.Record, "cluster_name")))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	a.completes(a.scopedChoices("domains", domainNames, domainDetail), get, verify, update, remove)
	domains.AddCommand(list, get, add, verify, update, remove)
	root.AddCommand(domains)
}

// responseWarning is the warning a response carries beside its data.
func responseWarning(body []byte) string {
	var envelope struct {
		Warning string `json:"warning"`
	}
	_ = json.Unmarshal(body, &envelope)
	return envelope.Warning
}
