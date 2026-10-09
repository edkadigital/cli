package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

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
func (a *App) resolveDomain(ctx context.Context, target string) (*candidate, string, error) {
	c, err := a.resolveScoped(ctx, "domain", "domains", target, "no domains; add one with `edka domains add '*.example.com'`", domainNames, domainDetail)
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
// cluster serves.
func domainRecords(domain, class, delegation map[string]any) []map[string]any {
	records := []map[string]any{}
	if value := text(delegation, "record_value"); value != "" {
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
		{"Certificate", stateLine(certificate)},
		{"Expires", expiry(certificate)},
		{"DNS", stateLine(dns)},
		{"Cluster", clusterName},
		{"Created", when(domain["created_at"])},
		{"ID", text(domain, "id")},
	}, a.color); err != nil {
		return err
	}
	records, _ := view["dns_records"].([]map[string]any)
	if len(records) == 0 {
		return nil
	}
	fmt.Fprintln(a.Out)
	if err := ui.Table(a.Out, []ui.Column{ui.Field("RECORD", "name"), ui.Field("TYPE", "type"), ui.Field("VALUE", "value"), ui.Field("FOR", "purpose")}, records, a.color); err != nil {
		return err
	}
	if slices.ContainsFunc(records, func(r map[string]any) bool { return r["required"] == false }) {
		a.message("The %s records are optional. They send every name under %s that has no record of its own to the cluster. With other hosts in the zone, point only the hostnames the cluster serves at the same addresses.", ui.Clean(text(domain, "domain")), ui.Clean(apexOf(domain)))
	}
	return nil
}

// askApex asks whether a wildcard's certificate covers its apex too, as the
// console's Include switch does.
func (a *App) askApex(apex string) (bool, error) {
	return a.askYesNo(fmt.Sprintf("Include %s? It shares the DNS validation and the certificate", ui.Clean(apex)), false)
}

// quoted is a domain as a shell takes it: a wildcard needs quotes.
func quoted(domain string) string { return shellJoin([]string{domain}) }

func (a *App) addDomains(root *cobra.Command) {
	domains := &cobra.Command{Use: "domains", Aliases: []string{"domain"}, Short: "Add domains, issue their certificates and delete them", Long: "Manage the domains of a cluster. A domain is a hostname, such as\napp.example.com, or a wildcard, such as *.example.com, that a traffic class of\nthe cluster serves over HTTPS with a Let's Encrypt certificate.\n\nA wildcard is validated over DNS: you create one CNAME record, then run\n`edka domains verify <domain>`. A hostname is validated over HTTP once it points at the\ncluster. `edka domains get` lists the DNS records a domain needs. Quote a\nwildcard, so the shell leaves the * alone.", GroupID: "resources", Example: "  edka domains list\n  edka domains add '*.example.com' --apex\n  edka domains get '*.example.com'\n  edka domains verify '*.example.com' --wait\n  edka domains add app.example.com\n  edka domains delete app.example.com"}
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
	get := &cobra.Command{Use: "get [domain]", Short: "Show a domain, its certificate and the DNS records it needs", Long: "Show a domain with the state of its certificate, read from the cluster, and\nthe DNS records it needs: the CNAME that validates a certificate over DNS, and\nthe records that point the domain at its traffic class.", Args: cobra.MaximumNArgs(1), Example: "  edka domains get '*.example.com'\n  edka domains get app.example.com --json", RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := a.resolveDomain(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		view, err := a.domainView(cmd.Context(), c.Record)
		if err != nil {
			return err
		}
		return a.showDomain(view, text(c.Record, "cluster_name"))
	}}

	var class, validation string
	var apex bool
	add := &cobra.Command{Use: "add <domain>", Short: "Add a domain to a cluster", Long: "Add a hostname or a wildcard to the linked or selected cluster. The traffic\nclass --class names serves it, or the cluster's default one.\n\nA wildcard such as *.example.com is validated over DNS, and --apex makes its\ncertificate cover example.com too. In a terminal, a wildcard without --apex\nasks whether to include it. A hostname is validated over HTTP, which\nneeds a public traffic class, or over DNS with --validation dns-01.\n\nThe command prints the DNS records to create. For a domain validated over DNS,\ncreate them and run `edka domains verify <domain>`.", Args: cobra.ExactArgs(1), Example: "  edka domains add '*.example.com' --apex\n  edka domains add app.example.com\n  edka domains add internal.example.com --class eg-ts --validation dns-01", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, name := cmd.Context(), strings.ToLower(strings.TrimSpace(args[0]))
		wildcard := strings.HasPrefix(name, "*.")
		switch {
		case validation != "" && validation != "dns-01" && validation != "http-01":
			return fmt.Errorf("--validation is dns-01 or http-01")
		case wildcard && validation == "http-01":
			return fmt.Errorf("a wildcard is validated over DNS; leave --validation out")
		case apex && !wildcard:
			return fmt.Errorf("--apex needs a wildcard such as '*.%s'", name)
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
		view, err := a.domainView(ctx, created)
		if err != nil {
			return err
		}
		if err := a.showDomain(view, cluster.Name); err != nil {
			return err
		}
		domain := quoted(text(created, "domain"))
		if validationOf(created) == "dns-01" {
			a.message("Next: create the records, then run `edka domains verify %s`", domain)
		} else {
			a.message("Next: create the records. Edka issues the certificate once the hostname reaches the cluster; check it with `edka domains get %s`", domain)
		}
		return nil
	}}
	add.Flags().StringVar(&class, "class", "", "Traffic class that serves the domain (default: the cluster's default)")
	add.Flags().StringVar(&validation, "validation", "", "How Let's Encrypt validates a hostname: http-01 or dns-01 (default: http-01)")
	add.Flags().BoolVar(&apex, "apex", false, "For a wildcard, cover the hostname it is under too")
	a.completesFlag(add, "validation", values("http-01", "dns-01"))
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
	verify := &cobra.Command{Use: "verify <domain>", Short: "Check a domain's DNS record and issue its certificate", Long: "Check the CNAME record of a domain validated over DNS. When the record is in\nplace, Edka issues the certificate. Until then it keeps checking, and\n`edka domains get` shows the record to create.\n\nWith --wait, the DNS and certificate status go to stderr until the certificate\nis ready.\n\nOn a cluster with an ingress controller, --namespace names each namespace that\ngets a copy of a wildcard certificate. A Gateway API class needs none.", Args: cobra.ExactArgs(1), Example: "  edka domains verify '*.example.com' --wait\n  edka domains verify '*.example.com' --namespace default --namespace shop", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		ctx := cmd.Context()
		c, path, err := a.resolveDomain(ctx, args[0])
		if err != nil {
			return err
		}
		name := ui.Clean(c.Name)
		if validationOf(c.Record) != "dns-01" {
			return fmt.Errorf("%s is validated over HTTP: Edka issues its certificate once the hostname reaches the cluster; check it with `edka domains get %s`", name, quoted(c.Name))
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
		if delegation, _ := view["delegation"].(map[string]any); text(dns, "state") != "ready" && text(delegation, "record_value") != "" {
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
				return fmt.Errorf("the certificate for %s is not ready yet: %w; Edka keeps checking, follow it with `edka domains get %s`", name, waitCtx.Err(), quoted(c.Name))
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
			a.message("Edka keeps checking. Follow it with `edka domains get %s`, or run verify with --wait", quoted(c.Name))
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": view}), a.output, false)
		}
		return nil
	}}
	verify.Flags().StringArrayVar(&namespaces, "namespace", nil, "Namespace that gets a copy of the certificate, with an ingress controller")
	verify.Flags().BoolVar(&wait, "wait", false, "Wait until the certificate is ready")
	verify.Flags().DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "Maximum wait for the certificate")

	var includeApex bool
	update := &cobra.Command{Use: "update <domain>", Short: "Include or leave out the apex of a wildcard", Long: "Change whether the certificate of a wildcard covers the hostname it is under:\nexample.com for *.example.com. --apex includes it, and --apex=false leaves it\nout. An included apex needs its own DNS records, which `edka domains get` lists.", Args: cobra.ExactArgs(1), Example: "  edka domains update '*.example.com' --apex\n  edka domains update '*.example.com' --apex=false", RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("apex") {
			return fmt.Errorf("pass --apex to include the apex, or --apex=false to leave it out")
		}
		c, path, err := a.resolveDomain(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		name := ui.Clean(c.Name)
		if !isWildcard(c.Record) {
			return fmt.Errorf("%s is a hostname; only a wildcard has an apex", name)
		}
		response, err := a.request(cmd.Context(), "PATCH", path, nil, jsonBody(map[string]bool{"include_apex": includeApex}))
		if err != nil {
			return err
		}
		if includeApex {
			a.message("✓ %s includes %s\n  Its DNS records: edka domains get %s", name, ui.Clean(apexOf(c.Record)), quoted(c.Name))
		} else {
			a.message("✓ %s leaves out %s", name, ui.Clean(apexOf(c.Record)))
		}
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	update.Flags().BoolVar(&includeApex, "apex", false, "Cover the hostname the wildcard is under")

	remove := &cobra.Command{Use: "delete <domain>", Short: "Delete a domain and its certificate", Long: "Delete a domain from its cluster, with its certificate and its HTTPS listener.\nHostnames under it stop being served over HTTPS. Edka refuses while a Codex\nagent or environment uses the domain.", Args: cobra.ExactArgs(1), Example: "  edka domains delete app.example.com\n  edka domains delete '*.example.com' --yes", RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveDomain(cmd.Context(), args[0])
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
