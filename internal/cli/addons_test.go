package cli

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// catalogEntry is a catalog add-on whose template names its dependencies.
func catalogEntry(name, version, category string, required bool, dependencies string) map[string]any {
	return map[string]any{
		"name": name, "default_version": version, "category": category, "required": required, "namespace": name, "description": name + " chart",
		"template": "apiVersion: helm.cattle.io/v1\nkind: HelmChart\nmetadata:\n  name: " + name + "\n  annotations:\n    meta_dependencies: \"" + dependencies + "\"\nspec:\n  version: \"{{{ version }}}\"\n---\nkind: ConfigMap\n",
	}
}

func addonList(rows ...string) string { return `{"data":[` + strings.Join(rows, ",") + `]}` }

const (
	certManager  = `{"id":"x1","cluster_id":"c1","addon_name":"cert-manager","version":"v1.16.2","status":"installed","progress":100,"created_at":"2026-09-01T10:00:00Z"}`
	trustManager = `{"id":"x2","cluster_id":"c1","addon_name":"trust-manager","version":"0.11.0","status":"installed","progress":100}`
	zot          = `{"id":"x3","cluster_id":"c1","addon_name":"zot","version":"0.1.100","status":"installed","progress":100}`
	keel         = `{"id":"x4","cluster_id":"c1","addon_name":"keel","version":"1.0.0","status":"upgrading","progress":40}`
	metrics      = `{"id":"x5","cluster_id":"c1","addon_name":"metrics-server","version":"3.12.0","status":"failed","addons":{"lastError":"helm timed out"}}`
	reflector    = `{"id":"y1","cluster_id":"c2","addon_name":"reflector","version":"9.1.0","status":"installed","progress":100}`
)

// addonAPI serves two clusters with add-ons and the catalog. Each route in
// extra replaces the default answers.
func addonAPI(t *testing.T, extra map[string][]string) (*httptest.Server, *stepAPI) {
	t.Helper()
	catalog, err := json.Marshal(map[string]any{"data": []any{
		catalogEntry("cert-manager", "1.16.2", "security", true, ""),
		catalogEntry("keel", "1.0.5", "deployment", false, ""),
		catalogEntry("metrics-server", "3.12.2", "monitoring", false, ""),
		catalogEntry("reflector", "9.1.7", "other", false, ""),
		catalogEntry("trust-manager", "0.12.0", "security", false, "cert-manager"),
		catalogEntry("zot", "0.1.122", "other", false, "cert-manager, trust-manager"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string][]string{
		"GET /api/clusters":           {`{"data":[{"id":"c1","name":"sinaia"},{"id":"c2","name":"staging"}]}`},
		"GET /api/addons/catalog":     {string(catalog)},
		"GET /api/clusters/c1/addons": {addonList(zot, trustManager, metrics, keel, certManager)},
		"GET /api/clusters/c2/addons": {addonList(reflector, strings.NewReplacer("x2", "y2", "c1", "c2", "0.11.0", "0.12.0").Replace(trustManager))},
	}
	for route, responses := range extra {
		steps[route] = responses
	}
	return newStepAPI(t, steps)
}

// writes counts the requests that change something.
func (s *stepAPI) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if !strings.HasPrefix(r, "GET ") {
			n++
		}
	}
	return n
}

func TestAddonVersionsFollowSemVerPrecedence(t *testing.T) {
	for _, tc := range []struct {
		candidate, current string
		newer              bool
	}{
		{"0.29.0", "0.28.0", true},
		{"0.28.0", "0.29.0", false},
		{"0.28.0", "0.28.0", false},
		{"1.10.0", "1.9.3", true},
		{"v1.2.0", "1.1.9", true},
		{"1.2", "1.2.0", false},
		{"1.2.1", "1.2", true},
		{"1.2.3-rc.2", "1.2.3-rc.1", true},
		{"1.2.3-rc.10", "1.2.3-rc.9", true},
		{"1.2.3", "1.2.3-rc.1", true},
		{"1.2.3-rc.1", "1.2.3", false},
		{"1.2.3-rc.1", "1.2.2", true},
		{"1.0.0-beta", "1.0.0-alpha", true},
		{"1.0.0-alpha", "1.0.0-1", true},
		{"1.0.0-alpha.1", "1.0.0-alpha", true},
		{"1.0.0-alpha", "1.0.0-alpha.1", false},
		{"1.2.3+build.5", "1.2.3", false},
		{"1.2.3-rc.1+build.2", "1.2.3-rc.1+build.1", false},
		{"99999999999999999999.0", "9.0", true},
		{"latest", "1.2.0", true},
		{"stable", "stable", false},
	} {
		if got := newerVersion(tc.candidate, tc.current); got != tc.newer {
			t.Errorf("newerVersion(%q, %q) = %v", tc.candidate, tc.current, got)
		}
	}
}

func TestAddonDependenciesComeFromTheFirstTemplateDocument(t *testing.T) {
	template := "apiVersion: helm.cattle.io/v1\nmetadata:\n    name: zot\n    annotations:\n        meta_dependencies: 'cert-manager,trust-manager'\nspec:\n    version: '{{{ version }}}'\n---\nmetadata:\n    annotations:\n        meta_dependencies: 'ignored'\n"
	if got := strings.Join(addonDependencies(map[string]any{"template": template}), " "); got != "cert-manager trust-manager" {
		t.Fatal(got)
	}
	for _, template := range []string{"", "{{ not yaml", "metadata: []"} {
		if got := addonDependencies(map[string]any{"template": template}); got == nil || len(got) != 0 {
			t.Fatalf("%q: %v", template, got)
		}
	}
}

func TestAddonsListShowsVersionsAndUpdates(t *testing.T) {
	server, api := addonAPI(t, nil)
	out, _, err := execute(t, server.URL, "addons", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	if got, want := strings.Join(lines, "\n"), strings.Join([]string{
		"NAME VERSION UPDATE STATUS CATEGORY CLUSTER",
		"cert-manager v1.16.2 — installed security sinaia",
		"keel 1.0.0 — upgrading 40% deployment sinaia",
		"metrics-server 3.12.0 — failed monitoring sinaia",
		"trust-manager 0.11.0 0.12.0 installed security sinaia",
		// The console manages Zot from another view, so no update is offered.
		"zot 0.1.100 — installed other sinaia",
		"reflector 9.1.0 9.1.7 installed other staging",
		"trust-manager 0.12.0 — installed security staging",
	}, "\n"); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	out, _, err = execute(t, server.URL, "addon", "list", "--outdated")
	if err != nil || strings.Count(strings.TrimSpace(out), "\n") != 2 || !strings.Contains(out, "0.12.0") || !strings.Contains(out, "9.1.7") {
		t.Fatal(out, err)
	}

	api.requests = nil
	out, _, err = execute(t, server.URL, "addons", "list", "--cluster", "sinaia", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(out), &body) != nil || len(body.Data) != 5 {
		t.Fatal(out)
	}
	certManager, trustManager, zot := body.Data[0], body.Data[3], body.Data[4]
	if certManager["cluster_name"] != "sinaia" || certManager["catalog_version"] != "1.16.2" || certManager["update_available"] != false || certManager["required"] != true || strings.Join(strings.Fields(strings.Trim(jsonText(t, certManager["dependents"]), "[]")), "") != `"trust-manager","zot"` {
		t.Fatal(out)
	}
	if trustManager["update_available"] != true || jsonText(t, trustManager["depends_on"]) != `["cert-manager"]` || trustManager["managed_from"] != nil {
		t.Fatal(out)
	}
	if zot["update_available"] != false || zot["managed_from"] != "Registries" || jsonText(t, zot["dependents"]) != `[]` {
		t.Fatal(out)
	}
	if got := strings.Join(api.requests, ","); got != "GET /api/clusters,GET /api/clusters/c1/addons,GET /api/addons/catalog" {
		t.Fatal(got)
	}
}

func jsonText(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAddonsGetAndCatalog(t *testing.T) {
	server, _ := addonAPI(t, nil)
	out, _, err := execute(t, server.URL, "addons", "get", "cert-manager", "--cluster", "sinaia")
	for _, want := range []string{"v1.16.2", "installed", "security", "Used by", "trust-manager, zot", "sinaia", "x1"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s %v", want, out, err)
		}
	}
	if strings.Contains(out, "Update") {
		t.Fatalf("offered an update to the installed version:\n%s", out)
	}
	for _, tc := range []struct{ addon, want string }{
		{"trust-manager", "0.12.0"},
		{"trust-manager", "cert-manager"},
		{"metrics-server", "helm timed out"},
		{"zot", "Cluster > Registries in the console"},
	} {
		if out, _, err := execute(t, server.URL, "addons", "get", tc.addon, "--cluster", "sinaia"); err != nil || !strings.Contains(out, tc.want) {
			t.Errorf("%s: missing %q in\n%s %v", tc.addon, tc.want, out, err)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"addons", "get"}, "name the add-on; list them with `edka addons list`"},
		{[]string{"addons", "get", "trust-manager"}, "2 add-ons match; pass an ID: trust-manager (x2), trust-manager (y2)"},
		{[]string{"addons", "get", "reflector", "--cluster", "sinaia"}, `add-on "reflector" was not found in cluster sinaia; choose another with --cluster`},
		{[]string{"addons", "catalog", "--category", "storage"}, `no add-ons in category "storage"; choose one of deployment, monitoring, other, security`},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}

	out, _, err = execute(t, server.URL, "addons", "catalog")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || len(lines) != 7 || strings.Join(strings.Fields(lines[0]), " ") != "NAME CATEGORY VERSION REQUIRED DESCRIPTION" || strings.Join(strings.Fields(lines[1]), " ") != "cert-manager security 1.16.2 yes cert-manager chart" {
		t.Fatal(out, err)
	}
	out, _, err = execute(t, server.URL, "addons", "catalog", "--category", "security")
	if err != nil || strings.Count(strings.TrimSpace(out), "\n") != 2 || !strings.Contains(out, "trust-manager") {
		t.Fatal(out, err)
	}
}

func TestAddonsInstall(t *testing.T) {
	queued := `{"message":"Addon installation initiated","data":{"cluster_id":"c1","addon_name":"trust-manager","version":"0.12.0","status":"installing","task_id":"7"}}`
	server, api := addonAPI(t, map[string][]string{"POST /api/clusters/c2/addons": {queued}})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"addons", "install"}, "accepts 1 arg(s), received 0"},
		{[]string{"addons", "install", "zot", "--cluster", "staging"}, "zot is managed from Cluster > Registries in the console; open the cluster with `edka open`"},
		{[]string{"addons", "install", "nope", "--cluster", "staging"}, "add-on \"nope\" is not in the catalog; list them with `edka addons catalog`"},
		{[]string{"addons", "install", "keel"}, "choose a cluster with --cluster or run `edka link`"},
		{[]string{"addons", "install", "keel", "--cluster", "staging", "--wait", "--wait-timeout", "0s"}, "wait-timeout must be positive"},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	if api.writes() != 0 {
		t.Fatalf("installed without a valid request: %v", api.requests)
	}

	out, errOut, err := execute(t, server.URL, "addons", "install", "trust-manager", "--cluster", "staging")
	if err != nil || out != "" {
		t.Fatal(out, err)
	}
	inOrder(t, errOut, "✓ Installing trust-manager 0.12.0 in cluster staging", "It depends on cert-manager. Edka installs the missing ones first.", "Next: edka addons get trust-manager")
	if got := api.bodies["POST /api/clusters/c2/addons"]; got != `{"addonName":"trust-manager"}` {
		t.Fatal(got)
	}
	out, _, err = execute(t, server.URL, "addons", "install", "trust-manager", "--cluster", "staging", "--version", "0.11.0", "--json")
	if err != nil || !strings.Contains(out, `"task_id": "7"`) || api.bodies["POST /api/clusters/c2/addons"] != `{"addonName":"trust-manager","version":"0.11.0"}` {
		t.Fatal(out, err, api.bodies)
	}

	server, _ = addonAPI(t, map[string][]string{"POST /api/clusters/c1/addons": {`409 {"error":"Addon already installed","message":"Addon 'keel' is already installed in this cluster"}`}})
	if _, _, err := execute(t, server.URL, "addons", "install", "keel", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "Addon 'keel' is already installed in this cluster (HTTP 409)\nUpdate it with `edka addons update keel`") {
		t.Fatal(err)
	}
}

func TestAddonsChangesEnvoyGatewayLikeTheGatewayView(t *testing.T) {
	catalog, err := json.Marshal(map[string]any{"data": []any{
		catalogEntry("envoy-gateway", "1.5.1", "networking", false, ""),
		catalogEntry("tailscale-operator", "1.86.0", "networking", false, ""),
	}})
	if err != nil {
		t.Fatal(err)
	}
	envoy := `{"id":"e1","cluster_id":"c1","addon_name":"envoy-gateway","version":"1.5.0","status":"installed","progress":100}`
	queued := `{"message":"Addon installation initiated","data":{"cluster_id":"c2","addon_name":"envoy-gateway","version":"1.5.1","status":"installing","task_id":"8"}}`
	server, api := addonAPI(t, map[string][]string{
		"GET /api/addons/catalog":      {string(catalog)},
		"GET /api/clusters/c1/addons":  {addonList(envoy)},
		"GET /api/clusters/c2/addons":  {addonList()},
		"POST /api/clusters/c2/addons": {queued},
		"PUT /api/clusters/c1/addons":  {`{"message":"Addon upgrade initiated","version":"1.5.1"}`},
	})

	if _, _, err := execute(t, server.URL, "addons", "install", "envoy-gateway", "--cluster", "staging"); err != nil {
		t.Fatal(err)
	}
	if got := api.bodies["POST /api/clusters/c2/addons"]; got != `{"addonName":"envoy-gateway"}` {
		t.Fatal(got)
	}

	_, errOut, err := execute(t, server.URL, "addons", "update", "envoy-gateway", "--cluster", "sinaia", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, errOut, "The Envoy proxies roll out again, so every gateway class briefly serves traffic from new pods.", "✓ Updating envoy-gateway to 1.5.1 in cluster sinaia")
	if got := api.bodies["PUT /api/clusters/c1/addons"]; got != `{"addonName":"envoy-gateway","version":"1.5.1"}` {
		t.Fatal(got)
	}

	// The Tailscale operator still needs the credentials the Gateway view asks for.
	if _, _, err := execute(t, server.URL, "addons", "install", "tailscale-operator", "--cluster", "staging"); err == nil || !strings.Contains(err.Error(), "tailscale-operator is managed from Cluster > Gateway in the console") {
		t.Fatal(err)
	}
}

func TestAddonsUpdateNamesBothVersionsBeforeChanging(t *testing.T) {
	server, api := addonAPI(t, map[string][]string{"PUT /api/clusters/c1/addons": {`{"message":"Addon upgrade initiated","version":"0.12.0"}`}})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"addons", "update"}, "name the add-on, or pass --all for every add-on with an update"},
		{[]string{"addons", "update", "trust-manager", "--all"}, "pass an add-on or --all, not both"},
		{[]string{"addons", "update", "--all", "--version", "1.0.0"}, "--version needs one add-on"},
		{[]string{"addons", "update", "--all", "--yes"}, "choose a cluster with --cluster or run `edka link`"},
		{[]string{"addons", "update", "trust-manager", "--cluster", "sinaia"}, "Update add-on trust-manager in cluster sinaia from 0.11.0 to 0.12.0 requires confirmation; rerun with --yes"},
		{[]string{"addons", "update", "trust-manager", "--yes"}, "2 add-ons match; pass an ID"},
		{[]string{"addons", "update", "zot", "--cluster", "sinaia", "--yes"}, "zot is managed from Cluster > Registries in the console"},
		{[]string{"addons", "update", "keel", "--cluster", "sinaia", "--yes"}, "keel is upgrading in cluster sinaia; follow it with `edka addons get keel`"},
		{[]string{"addons", "update", "metrics-server", "--cluster", "sinaia", "--yes"}, "metrics-server failed in cluster sinaia: helm timed out\nRetry with `edka addons install metrics-server`, or remove it with `edka addons uninstall metrics-server`"},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	// v1.16.2 is the catalog's 1.16.2, so there is nothing to send.
	_, errOut, err := execute(t, server.URL, "addons", "upgrade", "cert-manager", "--cluster", "sinaia", "--yes")
	if err != nil || !strings.Contains(errOut, "cert-manager v1.16.2 is up to date in cluster sinaia.") {
		t.Fatal(errOut, err)
	}
	if api.writes() != 0 {
		t.Fatalf("updated without a change to confirm: %v", api.requests)
	}

	out, errOut, err := execute(t, server.URL, "addons", "update", "trust-manager", "--cluster", "sinaia", "--yes")
	if err != nil || out != "" {
		t.Fatal(out, err)
	}
	inOrder(t, errOut, "✓ Updating trust-manager to 0.12.0 in cluster sinaia", "Check progress: edka addons get trust-manager")
	if got := api.bodies["PUT /api/clusters/c1/addons"]; got != `{"addonName":"trust-manager","version":"0.12.0"}` {
		t.Fatal(got)
	}
	// An explicit version is sent even when the catalog's is not newer.
	if _, _, err := execute(t, server.URL, "addons", "update", "cert-manager", "--cluster", "sinaia", "--version", "1.15.0", "--yes"); err != nil || api.bodies["PUT /api/clusters/c1/addons"] != `{"addonName":"cert-manager","version":"1.15.0"}` {
		t.Fatal(err, api.bodies)
	}
}

func TestAddonsUpdateAllFollowsDependencies(t *testing.T) {
	// approver sorts first by name and depends on trust-manager, which depends on cert-manager.
	catalog, err := json.Marshal(map[string]any{"data": []any{
		catalogEntry("approver", "2.0.0", "security", false, "trust-manager"),
		catalogEntry("cert-manager", "1.16.2", "security", true, ""),
		catalogEntry("reflector", "9.1.7", "other", false, ""),
		catalogEntry("trust-manager", "0.12.0", "security", false, "cert-manager"),
		catalogEntry("zot", "0.1.122", "other", false, "cert-manager, trust-manager"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	approver := `{"id":"x7","cluster_id":"c1","addon_name":"approver","version":"1.0.0","status":"installed","progress":100}`
	outdated := addonList(approver, strings.Replace(certManager, "v1.16.2", "1.15.0", 1), trustManager, zot)
	updated := addonList(strings.Replace(approver, "1.0.0", "2.0.0", 1), strings.Replace(certManager, "v1.16.2", "1.16.2", 1), strings.Replace(trustManager, "0.11.0", "0.12.0", 1), zot)
	routes := func(addons ...string) map[string][]string {
		return map[string][]string{
			"GET /api/addons/catalog":     {string(catalog)},
			"GET /api/clusters/c1/addons": addons,
			"GET /api/clusters/c2/addons": {addonList(strings.Replace(reflector, "9.1.0", "9.1.7", 1))},
			"PUT /api/clusters/c1/addons": {`{"message":"Addon upgrade initiated","version":"1.16.2"}`, `{"message":"Addon upgrade initiated","version":"0.12.0"}`, `{"message":"Addon upgrade initiated","version":"2.0.0"}`},
		}
	}

	server, api := addonAPI(t, routes(outdated))
	_, errOut, err := execute(t, server.URL, "addons", "update", "--all", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Update 3 add-ons in cluster sinaia requires confirmation; rerun with --yes") {
		t.Fatal(err)
	}
	inOrder(t, errOut, "Not updated in cluster sinaia:\n  zot is managed from Cluster > Registries in the console\n", "Updates in cluster sinaia:", "  cert-manager 1.15.0 to 1.16.2", "  trust-manager 0.11.0 to 0.12.0", "  approver 1.0.0 to 2.0.0")
	_, errOut, err = execute(t, server.URL, "addons", "update", "--all", "--cluster", "staging", "--yes")
	if err != nil || !strings.Contains(errOut, "The add-ons in cluster staging are up to date.") || strings.Contains(errOut, "Not updated") {
		t.Fatal(errOut, err)
	}
	if api.writes() != 0 {
		t.Fatalf("updated without confirmation or without updates: %v", api.requests)
	}

	out, errOut, err := execute(t, server.URL, "addons", "upgrade", "--all", "--cluster", "sinaia", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, errOut, "✓ Updating cert-manager to 1.16.2", "✓ Updating trust-manager to 0.12.0", "✓ Updating approver to 2.0.0", "Check progress: edka addons list")
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(out), &body) != nil || len(body.Data) != 3 || body.Data[0]["addon_name"] != "cert-manager" || body.Data[2]["version"] != "2.0.0" {
		t.Fatal(out)
	}
	if api.count("PUT /api/clusters/c1/addons") != 3 || api.bodies["PUT /api/clusters/c1/addons"] != `{"addonName":"approver","version":"2.0.0"}` {
		t.Fatal(api.requests, api.bodies)
	}

	// With --wait, each update finishes before the next one is sent.
	server, api = addonAPI(t, routes(outdated, updated))
	out, errOut, err = execute(t, server.URL, "addons", "update", "--all", "--cluster", "sinaia", "--yes", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, errOut, "Updating cert-manager to 1.16.2…", "✓ Updated cert-manager to 1.16.2", "Updating trust-manager to 0.12.0…", "✓ Updated trust-manager to 0.12.0", "✓ Updated approver to 2.0.0")
	inOrder(t, strings.Join(api.requests, "\n"), "PUT /api/clusters/c1/addons", "GET /api/clusters/c1/addons", "PUT /api/clusters/c1/addons", "GET /api/clusters/c1/addons", "PUT /api/clusters/c1/addons")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "approver ") || !strings.Contains(lines[1], "2.0.0") || strings.Contains(out, "zot") {
		t.Fatal(out)
	}

	// A failed update stops the ones after it.
	failed := addonList(approver, strings.NewReplacer("v1.16.2", "1.15.0", `"installed"`, `"failed","addons":{"lastError":"chart not found"}`).Replace(certManager), trustManager, zot)
	server, api = addonAPI(t, routes(outdated, failed))
	_, _, err = execute(t, server.URL, "addons", "update", "--all", "--cluster", "sinaia", "--yes", "--wait")
	if err == nil || !strings.Contains(err.Error(), "cert-manager failed: chart not found") || !strings.HasSuffix(err.Error(), "\nNot started: trust-manager, approver") {
		t.Fatal(err)
	}
	if api.count("PUT /api/clusters/c1/addons") != 1 {
		t.Fatal(api.requests)
	}
}

func TestAddonsUpdateAllNamesWhatItSkips(t *testing.T) {
	// sinaia has a failed add-on, one mid-update, a console-managed one with a
	// newer catalog version, and one update to make.
	server, _ := addonAPI(t, nil)
	_, errOut, err := execute(t, server.URL, "addons", "update", "--all", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Update 1 add-on in cluster sinaia requires confirmation; rerun with --yes") {
		t.Fatal(err)
	}
	inOrder(t, errOut,
		"Not updated in cluster sinaia:\n",
		"  keel is upgrading\n",
		"  metrics-server failed; retry with `edka addons install metrics-server`\n",
		"  zot is managed from Cluster > Registries in the console\n",
		"Updates in cluster sinaia:\n",
		"  trust-manager 0.11.0 to 0.12.0\n",
	)

	// "Up to date" doesn't cover a failed add-on.
	server, api := addonAPI(t, map[string][]string{"GET /api/clusters/c1/addons": {addonList(certManager, metrics)}})
	_, errOut, err = execute(t, server.URL, "addons", "update", "--all", "--cluster", "sinaia", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, errOut, "Not updated in cluster sinaia:\n  metrics-server failed; retry with `edka addons install metrics-server`\n", "The other add-ons in cluster sinaia are up to date.\n")
	if api.writes() != 0 {
		t.Fatalf("updated an add-on it should skip: %v", api.requests)
	}
}

func TestAddonsUninstallChecksWhatUsesTheAddon(t *testing.T) {
	server, api := addonAPI(t, map[string][]string{"DELETE /api/clusters/c2/addons": {`{"message":"Addon uninstallation initiated"}`}})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"addons", "uninstall"}, "accepts 1 arg(s), received 0"},
		{[]string{"addons", "uninstall", "cert-manager", "--cluster", "sinaia", "--yes"}, "cert-manager is a required add-on and can't be uninstalled"},
		{[]string{"addons", "uninstall", "trust-manager", "--cluster", "sinaia", "--yes"}, "trust-manager is used by zot in cluster sinaia; uninstall those add-ons first"},
		{[]string{"addons", "uninstall", "zot", "--cluster", "sinaia", "--yes"}, "zot is managed from Cluster > Registries in the console"},
		{[]string{"addons", "uninstall", "keel", "--cluster", "sinaia", "--yes"}, "keel is upgrading in cluster sinaia; follow it with `edka addons get keel`"},
		// A failed add-on can still be cleaned up.
		{[]string{"addons", "uninstall", "metrics-server", "--cluster", "sinaia"}, "Uninstall add-on metrics-server 3.12.0 from cluster sinaia requires confirmation; rerun with --yes"},
		{[]string{"addons", "uninstall", "reflector"}, "Uninstall add-on reflector 9.1.0 from cluster staging requires confirmation; rerun with --yes"},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	if api.writes() != 0 {
		t.Fatalf("uninstalled without confirmation: %v", api.requests)
	}
	_, errOut, err := execute(t, server.URL, "addons", "uninstall", "reflector", "--yes")
	if err != nil || !strings.Contains(errOut, "✓ Uninstalling reflector from cluster staging") {
		t.Fatal(errOut, err)
	}
	if got := api.bodies["DELETE /api/clusters/c2/addons"]; got != `{"addonName":"reflector"}` {
		t.Fatal(got)
	}
}

func TestAddonsWaitFollowsTheOperation(t *testing.T) {
	installing := func(progress string) string {
		return addonList(certManager, `{"id":"x9","cluster_id":"c1","addon_name":"reflector","version":"9.1.7","status":"installing","progress":`+progress+`}`)
	}
	server, api := addonAPI(t, map[string][]string{
		"POST /api/clusters/c1/addons": {`{"message":"Addon installation initiated","data":{"addon_name":"reflector","version":"9.1.7","status":"installing"}}`},
		"GET /api/clusters/c1/addons": {installing("0"), installing("20"), installing("20"), installing("60"),
			addonList(certManager, `{"id":"x9","cluster_id":"c1","addon_name":"reflector","version":"9.1.7","status":"installed","progress":100}`)},
	})
	out, errOut, err := execute(t, server.URL, "addons", "install", "reflector", "--cluster", "sinaia", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, errOut, "Installing reflector 9.1.7 in cluster sinaia…", "Installing reflector: 20%", "Installing reflector: 60%", "✓ Installed reflector in cluster sinaia")
	if strings.Count(errOut, "Installing reflector: 20%") != 1 || strings.Contains(errOut, ": 0%") {
		t.Fatalf("progress should print once per change:\n%s", errOut)
	}
	for _, want := range []string{"reflector", "9.1.7", "installed", "sinaia", "x9"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if api.count("POST /api/clusters/c1/addons") != 1 {
		t.Fatal(api.requests)
	}

	// The reason arrives a moment after the failed status.
	updating := func(status string) string {
		return addonList(certManager, strings.NewReplacer(`"installed"`, status, "100", "40").Replace(trustManager))
	}
	server, _ = addonAPI(t, map[string][]string{
		"PUT /api/clusters/c1/addons": {`{"message":"Addon upgrade initiated","version":"0.12.0"}`},
		"GET /api/clusters/c1/addons": {addonList(certManager, trustManager), updating(`"upgrading"`), updating(`"failed"`), updating(`"failed","addons":"{\"lastError\":\"chart not found\"}"`)},
	})
	_, errOut, err = execute(t, server.URL, "addons", "update", "trust-manager", "--cluster", "sinaia", "--yes", "--wait")
	if err == nil || !strings.Contains(err.Error(), "trust-manager failed: chart not found\nRetry with `edka addons install trust-manager`, or remove it with `edka addons uninstall trust-manager`") {
		t.Fatal(err)
	}
	inOrder(t, errOut, "Updating trust-manager to 0.12.0 in cluster sinaia…", "Updating trust-manager: 40%")

	server, _ = addonAPI(t, map[string][]string{
		"PUT /api/clusters/c1/addons": {`{"message":"Addon upgrade initiated","version":"0.12.0"}`},
		"GET /api/clusters/c1/addons": {addonList(certManager, trustManager), updating(`"upgrading"`)},
	})
	if _, _, err := execute(t, server.URL, "addons", "update", "trust-manager", "--cluster", "sinaia", "--yes", "--wait", "--wait-timeout", "50ms"); err == nil || !strings.Contains(err.Error(), "trust-manager is still upgrading: context deadline exceeded; the operation continues, follow it with `edka addons list`") {
		t.Fatal(err)
	}

	server, _ = addonAPI(t, map[string][]string{
		"DELETE /api/clusters/c2/addons": {`{"message":"Addon uninstallation initiated"}`},
		"GET /api/clusters/c2/addons":    {addonList(reflector), addonList(strings.NewReplacer(`"installed"`, `"uninstalling"`, "100", "50").Replace(reflector)), addonList()},
	})
	out, errOut, err = execute(t, server.URL, "addons", "uninstall", "reflector", "--cluster", "staging", "--yes", "--wait", "--json")
	if err != nil || !strings.Contains(out, "Addon uninstallation initiated") {
		t.Fatal(out, err)
	}
	inOrder(t, errOut, "Uninstalling reflector from cluster staging…", "Uninstalling reflector: 50%", "✓ Uninstalled reflector from cluster staging")
}

func TestAddonsWaitSurvivesFailedReads(t *testing.T) {
	updating := addonList(certManager, strings.NewReplacer(`"installed"`, `"upgrading"`, "100", "40").Replace(trustManager))
	routes := func(reads ...string) map[string][]string {
		return map[string][]string{
			"PUT /api/clusters/c1/addons": {`{"message":"Addon upgrade initiated","version":"0.12.0"}`},
			"GET /api/clusters/c1/addons": append([]string{addonList(certManager, trustManager), updating}, reads...),
		}
	}
	server, api := addonAPI(t, routes(`502 <html>Bad Gateway</html>`, `502 <html>Bad Gateway</html>`, `503 {"message":"Service unavailable"}`,
		addonList(certManager, strings.Replace(trustManager, "0.11.0", "0.12.0", 1))))
	out, errOut, err := execute(t, server.URL, "addons", "update", "trust-manager", "--cluster", "sinaia", "--yes", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut,
		"Updating trust-manager: 40%\n",
		"Could not read from Edka, trying again: Bad Gateway (HTTP 502)\n",
		"Could not read from Edka, trying again: Service unavailable (HTTP 503)\n",
		"✓ Updated trust-manager to 0.12.0 in cluster sinaia\n",
	)
	if strings.Count(errOut, "Bad Gateway") != 1 || !strings.Contains(out, "0.12.0") || api.count("PUT /api/clusters/c1/addons") != 1 {
		t.Fatalf("stdout:\n%s\nstderr:\n%s\n%v", out, errOut, api.requests)
	}

	// A timeout before any read succeeds still says the update continues.
	failing := routes()
	failing["GET /api/clusters/c1/addons"] = []string{addonList(certManager, trustManager), `503 {"message":"Service unavailable"}`}
	server, _ = addonAPI(t, failing)
	_, errOut, err = execute(t, server.URL, "addons", "update", "trust-manager", "--cluster", "sinaia", "--yes", "--wait", "--wait-timeout", "50ms")
	if err == nil || !strings.Contains(err.Error(), "trust-manager has not finished: context deadline exceeded; the operation continues, follow it with `edka addons list`") || !strings.Contains(errOut, "trying again: Service unavailable (HTTP 503)") {
		t.Fatal(err, errOut)
	}

	// Reading again can't change an answer such as a missing permission.
	server, api = addonAPI(t, routes(`403 {"message":"No access"}`))
	_, errOut, err = execute(t, server.URL, "addons", "update", "trust-manager", "--cluster", "sinaia", "--yes", "--wait")
	if err == nil || !strings.Contains(err.Error(), "No access (HTTP 403)") || strings.Contains(errOut, "trying again") || api.count("GET /api/clusters/c1/addons") != 3 {
		t.Fatal(err, errOut, api.requests)
	}
}
