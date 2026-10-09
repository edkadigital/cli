package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

const (
	wildcardDomain = `{"id":"w1","cluster_id":"c1","domain":"*.example.com","ingress_class":"eg","is_wildcard":true,"include_apex":true,"validation_method":"dns-01","acme_delegation_id":"abc123","created_at":"2026-06-09T12:35:19.568Z","tls_status":{"status":{"state":"ready","message":"Certificate is up to date and has not expired","not_after":"2026-11-06T10:44:11Z"},"dns":{"state":"ready","message":"DNS verified."}}}`
	hostDomain     = `{"id":"h1","cluster_id":"c1","domain":"app.example.org","ingress_class":"eg","is_wildcard":false,"include_apex":false,"validation_method":"http-01","tls_status":{"status":{"state":"pending","message":"Waiting for the HTTP-01 challenge"},"dns":null}}`
	tailnetDomain  = `{"id":"t1","cluster_id":"c1","domain":"*.ts.example.com","ingress_class":"eg-ts","is_wildcard":true,"include_apex":false,"validation_method":"dns-01","acme_delegation_id":"def456","tls_status":null}`
	trafficClasses = `{"success":true,"data":[` +
		`{"ingress_class":"eg","name":"Public","is_default":true,"exposure_scope":"public","exposure_mode":"cloud-load-balancer","load_balancer_ips":["2a01:db8::1","203.0.113.7"]},` +
		`{"ingress_class":"eg-ts","name":"ts","is_default":false,"exposure_scope":"private","exposure_mode":"tailscale-byod","load_balancer_ips":["gw.tailnet.ts.net","100.64.0.9"]}]}`
)

// tlsView is what Edka answers for one domain's certificate.
func tlsView(domain, delegation, certificate, dns string) string {
	return fmt.Sprintf(`{"success":true,"data":{"domain":%s,"delegation":%s,"status":%s,"dns":%s}}`, domain, delegation, certificate, dns)
}

const (
	wildcardRecord = `{"record_name":"_acme-challenge.example.com","record_type":"CNAME","record_value":"abc123.acme.edka.net"}`
	readyStatus    = `{"state":"ready","message":"Certificate is up to date and has not expired","not_after":"2026-11-06T10:44:11Z"}`
	missingStatus  = `{"state":"not_found","message":"No certificate yet"}`
	readyDNS       = `{"state":"ready","message":"DNS verified."}`
	missingDNS     = `{"state":"pending","message":"The CNAME record was not found yet."}`
)

// domainAPI serves the cluster sinaia with a wildcard, a hostname and a
// tailnet wildcard. Each route in extra replaces its answers.
func domainAPI(t *testing.T, extra map[string][]string) (*stepAPI, string) {
	t.Helper()
	steps := map[string][]string{
		"GET /api/clusters":                             {`{"data":[{"id":"c1","name":"sinaia"}]}`},
		"GET /api/clusters/c1/domains":                  {fmt.Sprintf(`{"success":true,"data":[%s,%s,%s]}`, wildcardDomain, hostDomain, tailnetDomain)},
		"GET /api/clusters/c1/ingress-controllers":      {trafficClasses},
		"GET /api/clusters/c1/domains/w1/wildcard-tls":  {tlsView(wildcardDomain, wildcardRecord, readyStatus, readyDNS)},
		"GET /api/clusters/c1/domains/h1/wildcard-tls":  {tlsView(hostDomain, "null", `{"state":"pending","message":"Waiting for the HTTP-01 challenge"}`, "null")},
		"GET /api/clusters/c1/domains/t1/wildcard-tls":  {tlsView(tailnetDomain, `{"record_name":"_acme-challenge.ts.example.com","record_type":"CNAME","record_value":"def456.acme.edka.net"}`, missingStatus, missingDNS)},
		"POST /api/clusters/c1/domains/w1/wildcard-tls": {tlsView(wildcardDomain, wildcardRecord, readyStatus, readyDNS)},
		"DELETE /api/clusters/c1/domains/h1":            {`{"success":true}`},
	}
	for route, responses := range extra {
		steps[route] = responses
	}
	server, api := newStepAPI(t, steps)
	return api, server.URL
}

func (s *stepAPI) body(route string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[route]
}

func TestDomainsListAndGet(t *testing.T) {
	_, base := domainAPI(t, nil)
	out, _, err := execute(t, base, "domains", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, want := range []string{
		"DOMAIN CLASS VALIDATION CERTIFICATE EXPIRES CLUSTER",
		"*.example.com eg dns-01 ready 2026-11-06 sinaia",
		"app.example.org eg http-01 pending — sinaia",
		"*.ts.example.com eg-ts dns-01 — — sinaia",
	} {
		if i >= len(lines) || squeeze(lines[i]) != want {
			t.Fatalf("line %d: want %q in\n%s", i, want, out)
		}
	}

	// A wildcard needs the record that validates it. Its own address records
	// are optional, and the apex it includes gets address records, IPv4 first.
	out, errOut, err := execute(t, base, "domains", "get", "*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "The *.example.com records are optional. They send every name under example.com that has no record of its own to the cluster.") {
		t.Fatal(errOut)
	}
	for _, want := range []string{
		"Apex example.com included",
		"Traffic class eg (Public, public)",
		"Certificate ready: Certificate is up to date and has not expired Expires 2026-11-06 DNS ready: DNS verified.",
		"RECORD TYPE VALUE FOR " +
			"_acme-challenge.example.com CNAME abc123.acme.edka.net certificate " +
			"*.example.com A 203.0.113.7 traffic, optional *.example.com AAAA 2a01:db8::1 traffic, optional " +
			"example.com A 203.0.113.7 traffic example.com AAAA 2a01:db8::1 traffic",
	} {
		if !strings.Contains(squeeze(out), want) {
			t.Fatalf("want %q in\n%s", want, out)
		}
	}
	// A hostname validated over HTTP has no validation record, and its address
	// records are required.
	out, errOut, err = execute(t, base, "domains", "get", "app.example.org")
	if err != nil || !strings.Contains(squeeze(out), "RECORD TYPE VALUE FOR app.example.org A 203.0.113.7 traffic app.example.org AAAA 2a01:db8::1 traffic") || strings.Contains(out, "Apex") || strings.Contains(out, "certificate\n") || strings.Contains(errOut, "optional") {
		t.Fatal(out, errOut, err)
	}
	// A Tailscale class is reached by its name.
	out, _, err = execute(t, base, "domains", "get", "*.ts.example.com", "--json")
	var view struct {
		Data struct {
			Records []map[string]any `json:"dns_records"`
			Domain  map[string]any   `json:"domain"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal([]byte(out), &view) != nil || len(view.Data.Records) != 2 || view.Data.Records[1]["type"] != "CNAME" || view.Data.Records[1]["value"] != "gw.tailnet.ts.net" || view.Data.Domain["id"] != "t1" {
		t.Fatal(out, err)
	}
	// JSON says which records a domain needs.
	if view.Data.Records[0]["required"] != true || view.Data.Records[1]["required"] != false {
		t.Fatal(view.Data.Records)
	}
}

func TestDomainGetShowsTheRecordedStatusWhenTheClusterIsUnreachable(t *testing.T) {
	_, base := domainAPI(t, map[string][]string{"GET /api/clusters/c1/domains/w1/wildcard-tls": {`500 {"error":"connect ETIMEDOUT 10.0.0.1:6443"}`}})
	out, errOut, err := execute(t, base, "domains", "get", "*.example.com")
	if err != nil || !strings.Contains(errOut, "Could not read the certificate from the cluster, showing the last status Edka recorded: connect ETIMEDOUT") {
		t.Fatal(errOut, err)
	}
	// Edka builds the validation record, so it is missing here.
	if !strings.Contains(squeeze(out), "Certificate ready: Certificate is up to date and has not expired") || strings.Contains(out, "_acme-challenge") || !strings.Contains(squeeze(out), "*.example.com A 203.0.113.7 traffic") {
		t.Fatal(out)
	}
}

func TestDomainAdd(t *testing.T) {
	created := `{"id":"n1","cluster_id":"c1","domain":"*.shop.example.com","ingress_class":"eg","is_wildcard":true,"include_apex":true,"validation_method":"dns-01","acme_delegation_id":"new789"}`
	record := `{"record_name":"_acme-challenge.shop.example.com","record_type":"CNAME","record_value":"new789.acme.edka.net"}`
	api, base := domainAPI(t, map[string][]string{
		"POST /api/clusters/c1/domains":                {`{"success":true,"data":` + created + `,"warning":"_acme-challenge.shop.example.com points at another cluster."}`},
		"GET /api/clusters/c1/domains/n1/wildcard-tls": {tlsView(created, record, missingStatus, missingDNS)},
	})
	// The default traffic class serves the domain, and upper case is folded.
	out, errOut, err := execute(t, base, "domains", "add", "*.Shop.Example.com", "--apex", "--cluster", "sinaia")
	if err != nil {
		t.Fatal(errOut, err)
	}
	if got := api.body("POST /api/clusters/c1/domains"); got != `{"domain":"*.shop.example.com","include_apex":true,"ingress_class":"eg"}` {
		t.Fatal(got)
	}
	inOrder(t, errOut, "✓ Added *.shop.example.com to cluster sinaia\n", "  Note: _acme-challenge.shop.example.com points at another cluster.\n", "Next: create the records, then run `edka domains verify '*.shop.example.com'`\n")
	if !strings.Contains(squeeze(out), "_acme-challenge.shop.example.com CNAME new789.acme.edka.net certificate *.shop.example.com A 203.0.113.7 traffic") || !strings.Contains(squeeze(out), "shop.example.com AAAA 2a01:db8::1 traffic") {
		t.Fatal(out)
	}

	hostname := `{"id":"n2","cluster_id":"c1","domain":"www.example.org","ingress_class":"eg-ts","is_wildcard":false,"validation_method":"dns-01","acme_delegation_id":"host42"}`
	api, base = domainAPI(t, map[string][]string{"POST /api/clusters/c1/domains": {`{"success":true,"data":` + hostname + `}`}})
	if _, _, err := execute(t, base, "domains", "add", "www.example.org", "--class", "eg-ts", "--validation", "dns-01", "--cluster", "sinaia"); err != nil {
		t.Fatal(err)
	}
	if got := api.body("POST /api/clusters/c1/domains"); got != `{"domain":"www.example.org","include_apex":false,"ingress_class":"eg-ts","validation_method":"dns-01"}` {
		t.Fatal(got)
	}

	web := `{"id":"n3","cluster_id":"c1","domain":"www.example.org","ingress_class":"eg","is_wildcard":false,"validation_method":"http-01"}`
	_, base = domainAPI(t, map[string][]string{"POST /api/clusters/c1/domains": {`{"success":true,"data":` + web + `}`}})
	_, errOut, err = execute(t, base, "domains", "add", "www.example.org", "--cluster", "sinaia")
	if err != nil || !strings.Contains(errOut, "Next: create the records. Edka issues the certificate once the hostname reaches the cluster; check it with `edka domains get www.example.org`") {
		t.Fatal(errOut, err)
	}

	// Edka's refusal reaches the user as it is.
	_, base = domainAPI(t, map[string][]string{"POST /api/clusters/c1/domains": {`409 {"error":"The apex hostname example.com is already configured for this cluster"}`}})
	if _, _, err := execute(t, base, "domains", "add", "*.example.com", "--apex", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "The apex hostname example.com is already configured for this cluster") {
		t.Fatal(err)
	}
}

func TestDomainCommandsRefuseBeforeSending(t *testing.T) {
	api, base := domainAPI(t, nil)
	for _, test := range []struct {
		line string
		want string
	}{
		{"add app.example.org --apex", "--apex needs a wildcard such as '*.app.example.org'"},
		{"add *.example.org --validation http-01", "a wildcard is validated over DNS"},
		{"add app.example.org --validation tls", "--validation is dns-01 or http-01"},
		{"add app.example.org --class nginx", `cluster sinaia has no traffic class "nginx"; choose one of eg, eg-ts`},
		{"verify app.example.org", "app.example.org is validated over HTTP"},
		{"verify *.example.com --wait --wait-timeout 0s", "wait-timeout must be positive"},
		{"update *.example.com", "pass --apex to include the apex, or --apex=false to leave it out"},
		{"update app.example.org --apex", "app.example.org is a hostname; only a wildcard has an apex"},
		{"delete *.example.com", "Delete domain *.example.com from cluster sinaia with its certificate requires confirmation; rerun with --yes"},
		{"delete nope.example.com", `domain "nope.example.com" was not found in cluster sinaia`},
	} {
		_, _, err := execute(t, base, append(append([]string{"domains"}, strings.Split(test.line, " ")...), "--cluster", "sinaia")...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("edka domains %s: %v, want %q", test.line, err, test.want)
		}
	}
	if api.writes() != 0 {
		t.Fatal(api.requests)
	}
	_, base = domainAPI(t, map[string][]string{"GET /api/clusters/c1/ingress-controllers": {`{"data":[]}`}})
	if _, _, err := execute(t, base, "domains", "add", "app.example.org", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "cluster sinaia has no traffic class") {
		t.Fatal(err)
	}
	_, base = domainAPI(t, map[string][]string{"GET /api/clusters/c1/ingress-controllers": {`{"data":[{"ingress_class":"nginx","is_default":false},{"ingress_class":"eg","is_default":false}]}`}})
	if _, _, err := execute(t, base, "domains", "add", "app.example.org", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "cluster sinaia has no default traffic class; pass --class with one of nginx, eg") {
		t.Fatal(err)
	}
}

func TestDomainVerify(t *testing.T) {
	// The record is in place: Edka issues the certificate in the same request.
	api, base := domainAPI(t, nil)
	out, errOut, err := execute(t, base, "domains", "verify", "*.example.com", "--json")
	if err != nil || !strings.Contains(out, `"record_value": "abc123.acme.edka.net"`) {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, errOut, "DNS: ready: DNS verified.\n", "Certificate: ready: Certificate is up to date and has not expired\n", "✓ The certificate for *.example.com is ready, valid until 2026-11-06\n")
	if got := api.body("POST /api/clusters/c1/domains/w1/wildcard-tls"); got != `{}` {
		t.Fatal(got)
	}
	if _, _, err := execute(t, base, "domains", "verify", "*.example.com", "--namespace", "default", "--namespace", "shop"); err != nil || api.body("POST /api/clusters/c1/domains/w1/wildcard-tls") != `{"namespaces":["default","shop"]}` {
		t.Fatal(err, api.body("POST /api/clusters/c1/domains/w1/wildcard-tls"))
	}

	// The record is missing: Edka keeps checking, and the command names the record.
	pending := tlsView(wildcardDomain, wildcardRecord, missingStatus, missingDNS)
	_, base = domainAPI(t, map[string][]string{"POST /api/clusters/c1/domains/w1/wildcard-tls": {pending}})
	_, errOut, err = execute(t, base, "domains", "verify", "*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, errOut, "DNS: pending: The CNAME record was not found yet.\n", "Certificate: not_found: No certificate yet\n", "Create this record at your DNS provider: _acme-challenge.example.com CNAME abc123.acme.edka.net\n", "Edka keeps checking. Follow it with `edka domains get '*.example.com'`, or run verify with --wait\n")

	// --wait follows the status until the certificate is ready, and prints a
	// status once while it stays the same.
	issuing := tlsView(wildcardDomain, wildcardRecord, `{"state":"pending","message":"Issuing the certificate"}`, readyDNS)
	_, base = domainAPI(t, map[string][]string{
		"POST /api/clusters/c1/domains/w1/wildcard-tls": {pending},
		"GET /api/clusters/c1/domains/w1/wildcard-tls":  {pending, pending, issuing, issuing, tlsView(wildcardDomain, wildcardRecord, readyStatus, readyDNS)},
	})
	_, errOut, err = execute(t, base, "domains", "verify", "*.example.com", "--wait")
	if err != nil {
		t.Fatal(errOut, err)
	}
	inOrder(t, errOut, "DNS: pending: The CNAME record was not found yet.\n", "Create this record at your DNS provider", "DNS: ready: DNS verified.\n", "Certificate: pending: Issuing the certificate\n", "Certificate: ready: Certificate is up to date and has not expired\n", "✓ The certificate for *.example.com is ready")
	if strings.Count(errOut, "The CNAME record was not found yet.") != 1 || strings.Count(errOut, "Issuing the certificate") != 1 {
		t.Fatal(errOut)
	}

	failed := tlsView(wildcardDomain, wildcardRecord, `{"state":"error","message":"The gateway already serves *.example.com with another certificate"}`, readyDNS)
	_, base = domainAPI(t, map[string][]string{"POST /api/clusters/c1/domains/w1/wildcard-tls": {pending}, "GET /api/clusters/c1/domains/w1/wildcard-tls": {failed}})
	if _, _, err := execute(t, base, "domains", "verify", "*.example.com", "--wait"); err == nil || !strings.Contains(err.Error(), "the certificate for *.example.com failed: The gateway already serves *.example.com with another certificate") {
		t.Fatal(err)
	}

	_, base = domainAPI(t, map[string][]string{"POST /api/clusters/c1/domains/w1/wildcard-tls": {pending}, "GET /api/clusters/c1/domains/w1/wildcard-tls": {pending}})
	if _, _, err := execute(t, base, "domains", "verify", "*.example.com", "--wait", "--wait-timeout", "40ms"); err == nil || !strings.Contains(err.Error(), "the certificate for *.example.com is not ready yet: context deadline exceeded; Edka keeps checking, follow it with `edka domains get '*.example.com'`") {
		t.Fatal(err)
	}
}

func TestDomainUpdateAndDelete(t *testing.T) {
	api, base := domainAPI(t, map[string][]string{"PATCH /api/clusters/c1/domains/t1": {`{"success":true,"data":{"id":"t1","include_apex":true}}`}})
	out, errOut, err := execute(t, base, "domains", "update", "*.ts.example.com", "--apex", "--json")
	if err != nil || api.body("PATCH /api/clusters/c1/domains/t1") != `{"include_apex":true}` || !strings.Contains(errOut, "✓ *.ts.example.com includes ts.example.com\n  Its DNS records: edka domains get '*.ts.example.com'") || !strings.Contains(out, `"include_apex": true`) {
		t.Fatal(out, errOut, err, api.body("PATCH /api/clusters/c1/domains/t1"))
	}
	_, errOut, err = execute(t, base, "domains", "update", "*.ts.example.com", "--apex=false")
	if err != nil || api.body("PATCH /api/clusters/c1/domains/t1") != `{"include_apex":false}` || !strings.Contains(errOut, "✓ *.ts.example.com leaves out ts.example.com") {
		t.Fatal(errOut, err)
	}

	_, errOut, err = execute(t, base, "domains", "delete", "app.example.org", "--yes")
	if err != nil || api.count("DELETE /api/clusters/c1/domains/h1") != 1 || !strings.Contains(errOut, "✓ Deleted domain app.example.org from cluster sinaia") {
		t.Fatal(errOut, err, api.requests)
	}
	_, base = domainAPI(t, map[string][]string{"DELETE /api/clusters/c1/domains/h1": {`409 {"error":"Domain is in use","message":"This domain is selected by a Codex Agent or an active Codex environment. Change the agent domain or delete its environments before deleting the domain."}`}})
	if _, _, err := execute(t, base, "domains", "delete", "app.example.org", "--yes"); err == nil || !strings.Contains(err.Error(), "This domain is selected by a Codex Agent") {
		t.Fatal(err)
	}
}

func TestDomainCompletion(t *testing.T) {
	_, base := domainAPI(t, nil)
	for _, test := range []struct {
		line string
		want []string
	}{
		{"domains get ", []string{"*.example.com\teg · ready · sinaia", "app.example.org\teg · pending · sinaia", "*.ts.example.com\teg-ts · sinaia"}},
		{"domains delete app", []string{"app.example.org\teg · pending · sinaia"}},
		{"domains add x.example.com --cluster sinaia --class ", []string{"eg\tPublic · public", "eg-ts\tts · private"}},
		{"domains add x.example.com --validation ", []string{"http-01", "dns-01"}},
	} {
		got, directive := completions(t, base, strings.Split(test.line, " ")...)
		if strings.Join(got, "\n") != strings.Join(test.want, "\n") || directive != shellNoFiles {
			t.Errorf("edka %s<TAB> offered %q %s, want %q", test.line, got, directive, test.want)
		}
	}
}

func TestRecordTypeFollowsTheAddress(t *testing.T) {
	for address, want := range map[string]string{"203.0.113.7": "A", "2a01:db8::1": "AAAA", "gw.tailnet.ts.net": "CNAME", "100.64.0.9": "A"} {
		if got := recordType(address); got != want {
			t.Errorf("%s: %s, want %s", address, got, want)
		}
	}
}

func TestDomainAddAsksAboutTheApex(t *testing.T) {
	var errOut bytes.Buffer
	a := &App{In: strings.NewReader("maybe\ny\n"), Err: &errOut}
	include, err := a.askApex("example.com")
	if err != nil || !include {
		t.Fatal(include, err)
	}
	if !strings.Contains(errOut.String(), "Include example.com? It shares the DNS validation and the certificate [y/N] ") {
		t.Fatal(errOut.String())
	}
	// An empty answer leaves the apex out, like the console's switch.
	a = &App{In: strings.NewReader("\n"), Err: io.Discard}
	if include, err := a.askApex("example.com"); err != nil || include {
		t.Fatal(include, err)
	}
}
