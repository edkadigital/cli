package cli

import (
	"strings"
	"testing"
)

const (
	ghcrRegistry = `{"name":"ghcr","type":"github-registry","url":"ghcr.io","username":"octocat","timestamp":"2026-09-01T10:00:00.000Z","created_at":"2026-08-01T10:00:00.000Z"}`
	hubRegistry  = `{"name":"hub","type":"docker-hub","username":"acme","email":"ops@acme.dev"}`
	ownOverview  = `{"installed":true,"status":"installed","endpoint":"zot.registry.svc.cluster.local","pods":{"ready":1,"total":1},"volume":{"usedBytes":637521920,"capacityBytes":10737418240,"usagePercent":6},"warnings":[],"repositories":[{"name":"api","tagCount":12,"tagCountTruncated":true,"tags":["v4","v3","v2","v1"]},{"name":"acme/worker","tagCount":1,"tags":["latest"]}]}`
)

// registryAPI serves an organization with two registries. sinaia uses ghcr as
// its default and a registry whose credentials were deleted; staging uses ghcr.
func registryAPI(t *testing.T, extra map[string][]string) (*stepAPI, string) {
	t.Helper()
	steps := map[string][]string{
		"GET /api/clusters":                                   {`{"data":[{"id":"c1","name":"sinaia"},{"id":"c2","name":"staging"}]}`},
		"GET /api/registry":                                   {`[` + ghcrRegistry + `,` + hubRegistry + `]`},
		"GET /api/registry/ghcr":                              {ghcrRegistry},
		"GET /api/registry/hub":                               {hubRegistry},
		"GET /api/registry/ghcr/exists":                       {`{"exists":true}`},
		"GET /api/registry/quay/exists":                       {`{"exists":false}`},
		"GET /api/clusters/c1/registries":                     {`{"defaultRegistry":"ghcr","operator":{"enabled":true},"registries":[{"name":"ghcr","type":"github-registry","url":"ghcr.io","isDefault":true,"syncStatus":"synced","lastError":null},{"name":"gone","type":"unknown","url":null,"isDefault":false,"syncStatus":"failed","lastError":"credentials not found"}]}`},
		"GET /api/clusters/c2/registries":                     {`{"defaultRegistry":"__managed_in_cluster_zot__","registries":[{"name":"ghcr","type":"github-registry","url":"ghcr.io","isDefault":false,"syncStatus":null}]}`},
		"POST /api/registry":                                  {`{"message":"Registry saved successfully"}`},
		"GET /api/clusters/c1/registries/in-cluster/overview": {ownOverview},
		"GET /api/clusters/c1/registries/in-cluster/tags":     {`{"repository":"api","tagCount":4,"tags":["v4","v3","v2","v1"],"truncated":false}`},
	}
	for route, responses := range extra {
		steps[route] = responses
	}
	server, api := newStepAPI(t, steps)
	return api, server.URL
}

func TestRegistriesListAndGet(t *testing.T) {
	_, base := registryAPI(t, nil)
	out, _, err := execute(t, base, "registries", "list")
	if err != nil || squeeze(out) != "NAME TYPE URL USERNAME ghcr github-registry ghcr.io octocat hub docker-hub — acme" {
		t.Fatal(out, err)
	}
	// With a cluster, the list says how it uses each registry, and names one
	// it still uses whose credentials are gone.
	out, _, err = execute(t, base, "registries", "list", "--cluster", "sinaia")
	if err != nil || squeeze(out) != "NAME TYPE URL USERNAME IN CLUSTER ghcr github-registry ghcr.io octocat default, synced hub docker-hub — acme — gone unknown — — failed: credentials not found" {
		t.Fatal(out, err)
	}
	out, _, err = execute(t, base, "registries", "list", "--cluster", "staging")
	if err != nil || !strings.Contains(squeeze(out), "ghcr github-registry ghcr.io octocat applied hub") {
		t.Fatal(out, err)
	}
	out, _, err = execute(t, base, "registries", "get", "ghcr")
	if err != nil || !strings.Contains(squeeze(out), "Name ghcr Type github-registry URL ghcr.io Username octocat Clusters sinaia (default), staging Saved 2026-09-01") {
		t.Fatal(out, err)
	}
	if _, _, err := execute(t, base, "registries", "get", "nope"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatal(err)
	}
}

func TestRegistryAddReadsThePasswordFromStdin(t *testing.T) {
	api, base := registryAPI(t, nil)
	out, errOut, err := executeInput(t, base, "s3cret-token\n", "registries", "add", "quay", "--type", "custom", "--url", "quay.io", "--username", "robot", "--json")
	if err != nil || !strings.Contains(errOut, "✓ Stored registry quay\n  Next: edka registries apply quay") {
		t.Fatal(errOut, err)
	}
	if got := api.body("POST /api/registry"); got != `{"name":"quay","password":"s3cret-token","type":"custom","url":"quay.io","username":"robot"}` {
		t.Fatal(got)
	}
	// The password goes to Edka and nowhere else.
	if strings.Contains(out+errOut, "s3cret") {
		t.Fatal(out, errOut)
	}

	// A registry that exists keeps its credentials unless --replace says otherwise.
	_, _, err = executeInput(t, base, "new-token", "registries", "add", "ghcr", "--type", "github-registry", "--url", "ghcr.io", "--username", "octocat")
	if err == nil || !strings.Contains(err.Error(), "registry ghcr exists; store new credentials for it with --replace") || api.count("POST /api/registry") != 1 {
		t.Fatal(err, api.requests)
	}
	// --replace keeps the flags left out.
	_, errOut, err = executeInput(t, base, "new-token\r\n", "registries", "add", "ghcr", "--replace", "--username", "hubot")
	if err != nil || !strings.Contains(errOut, "✓ Stored new credentials for registry ghcr\n  The clusters that use it get them.") {
		t.Fatal(errOut, err)
	}
	if got := api.body("POST /api/registry"); got != `{"name":"ghcr","password":"new-token","type":"github-registry","url":"ghcr.io","username":"hubot"}` {
		t.Fatal(got)
	}

	for _, test := range []struct {
		line string
		want string
	}{
		{"add quay --username robot", "pass --type with one of docker-hub, github-registry, google-artifact, aws-ecr, custom"},
		{"add quay --type quay --username robot", `unknown registry type "quay"`},
		{"add quay --type custom --url quay.io", "pass --username"},
		{"add quay --type custom --username robot", "a custom registry needs --url"},
		{"add a/b --type docker-hub --username robot", `invalid registry name "a/b"`},
	} {
		_, _, err := executeInput(t, base, "token", append([]string{"registries"}, strings.Split(test.line, " ")...)...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("edka registries %s: %v, want %q", test.line, err, test.want)
		}
	}
	// No password on stdin stores nothing.
	if _, _, err := executeInput(t, base, "", "registries", "add", "quay", "--type", "docker-hub", "--username", "robot"); err == nil || !strings.Contains(err.Error(), "no value for the password on stdin") || api.count("POST /api/registry") != 2 {
		t.Fatal(err, api.requests)
	}
}

func TestRegistryDeleteRefusesWhileAClusterUsesIt(t *testing.T) {
	api, base := registryAPI(t, map[string][]string{"DELETE /api/registry/hub": {`{"message":"Registry deleted successfully"}`}})
	_, _, err := execute(t, base, "registries", "delete", "ghcr", "--yes")
	if err == nil || !strings.Contains(err.Error(), "registry ghcr is used by clusters sinaia (default), staging; take it from each cluster first with `edka registries remove ghcr --cluster <cluster>`") || api.writes() != 0 {
		t.Fatal(err, api.requests)
	}
	if _, _, err := execute(t, base, "registries", "delete", "hub"); err == nil || !strings.Contains(err.Error(), "Delete registry hub and its credentials requires confirmation") || api.writes() != 0 {
		t.Fatal(err, api.requests)
	}
	_, errOut, err := execute(t, base, "registries", "delete", "hub", "--yes")
	if err != nil || api.count("DELETE /api/registry/hub") != 1 || !strings.Contains(errOut, "✓ Deleted registry hub") {
		t.Fatal(errOut, err)
	}
}

func TestRegistryApplyRemoveAndDefault(t *testing.T) {
	api, base := registryAPI(t, map[string][]string{
		"POST /api/registry/hub/apply/c1":                     {`{"message":"Registry application queued successfully","queued":true}`},
		"DELETE /api/registry/ghcr/apply/c1":                  {`{"message":"Registry removal queued successfully","queued":true}`},
		"POST /api/clusters/c1/registries/default":            {`{"message":"ok","registry":"hub"}`},
		"POST /api/clusters/c2/registries/default":            {`{"message":"ok","registry":null}`},
		"POST /api/registry/missing/apply/c1":                 {`404 {"error":"Registry not found"}`},
		"GET /api/clusters/c3/registries":                     {`{"defaultRegistry":null,"registries":[]}`},
		"GET /api/clusters":                                   {`{"data":[{"id":"c1","name":"sinaia"},{"id":"c2","name":"staging"},{"id":"c3","name":"edge"}]}`},
		"DELETE /api/registry/ghcr/apply/c2":                  {`{"message":"ok"}`},
		"POST /api/registry/ghcr/apply/c2":                    {`{"message":"ok"}`},
		"POST /api/clusters/c3/registries/default":            {`400 {"error":"Cluster registry not installed","message":"Install the in-cluster Zot registry before selecting it as default."}`},
		"GET /api/clusters/c3/registries/in-cluster/overview": {`{"installed":false,"status":"not_installed"}`},
	})
	_, errOut, err := execute(t, base, "registries", "apply", "hub", "--cluster", "sinaia")
	if err != nil || api.count("POST /api/registry/hub/apply/c1") != 1 || !strings.Contains(errOut, "✓ Applying registry hub to cluster sinaia") {
		t.Fatal(errOut, err)
	}
	if _, _, err := execute(t, base, "registries", "apply", "missing", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "Registry not found") {
		t.Fatal(err)
	}
	_, _, err = execute(t, base, "registries", "remove", "ghcr", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Remove registry ghcr from cluster sinaia, with its pull secret in every namespace requires confirmation") || api.count("DELETE /api/registry/ghcr/apply/c1") != 0 {
		t.Fatal(err, api.requests)
	}
	if _, errOut, err := execute(t, base, "registries", "remove", "ghcr", "--cluster", "sinaia", "--yes"); err != nil || api.count("DELETE /api/registry/ghcr/apply/c1") != 1 || !strings.Contains(errOut, "✓ Removing registry ghcr from cluster sinaia") {
		t.Fatal(errOut, err)
	}

	// The default is a registry, the cluster's own one, or none.
	for cluster, want := range map[string]string{"sinaia": "ghcr\n", "staging": "the cluster's own registry\n", "edge": ""} {
		out, errOut, err := execute(t, base, "registries", "default", "--cluster", cluster)
		if err != nil || out != want || cluster == "edge" && !strings.Contains(errOut, "Cluster edge has no default registry.") {
			t.Fatalf("%s: %q %q %v", cluster, out, errOut, err)
		}
	}
	if out, _, err := execute(t, base, "registries", "default", "--cluster", "staging", "--json"); err != nil || !strings.Contains(out, `"own": true`) {
		t.Fatal(out, err)
	}
	for _, test := range []struct {
		line, route, body, message string
	}{
		{"default hub --cluster sinaia", "POST /api/clusters/c1/registries/default", `{"registry":"hub"}`, "✓ Cluster sinaia uses registry hub by default"},
		{"default --own --cluster sinaia", "POST /api/clusters/c1/registries/default", `{"registry":"__managed_in_cluster_zot__"}`, "✓ Cluster sinaia uses its own registry by default"},
		{"default --none --cluster staging", "POST /api/clusters/c2/registries/default", `{"registry":null}`, "✓ Cluster staging has no default registry"},
	} {
		_, errOut, err := execute(t, base, append([]string{"registries"}, strings.Split(test.line, " ")...)...)
		if err != nil || api.body(test.route) != test.body || !strings.Contains(errOut, test.message) {
			t.Errorf("edka registries %s: %q %q %v", test.line, api.body(test.route), errOut, err)
		}
	}
	if _, _, err := execute(t, base, "registries", "default", "hub", "--none", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "not more than one") {
		t.Fatal(err)
	}
	// Edka's refusal reaches the user as it is.
	if _, _, err := execute(t, base, "registries", "default", "--own", "--cluster", "edge"); err == nil || !strings.Contains(err.Error(), "Install the in-cluster Zot registry before selecting it as default.") {
		t.Fatal(err)
	}
	if _, _, err := execute(t, base, "registries", "images", "--cluster", "edge"); err == nil || !strings.Contains(err.Error(), "cluster edge has no registry of its own; install it from Registries in the Edka console") {
		t.Fatal(err)
	}
}

func TestRegistryImagesAndTags(t *testing.T) {
	api, base := registryAPI(t, map[string][]string{"DELETE /api/clusters/c1/registries/in-cluster/tags": {`{"success":true,"repository":"api","deletedTags":["v1","v2"],"implicitlyDeletedTags":["old"],"failedTags":[]}`}})
	out, _, err := execute(t, base, "registries", "images", "--cluster", "sinaia")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Registry zot.registry.svc.cluster.local Status installed Pods 1 of 1 ready Storage 608 MiB of 10 GiB used (6%) Cluster sinaia",
		"REPOSITORY TAGS SAMPLE api 12+ v4, v3, v2 acme/worker 1 latest",
	} {
		if !strings.Contains(squeeze(out), want) {
			t.Fatalf("want %q in\n%s", want, out)
		}
	}
	out, _, err = execute(t, base, "registries", "tags", "api", "--cluster", "sinaia")
	if err != nil || squeeze(out) != "TAG v4 v3 v2 v1" {
		t.Fatal(out, err)
	}
	if got := api.requests[len(api.requests)-1]; got != "GET /api/clusters/c1/registries/in-cluster/tags" {
		t.Fatal(got)
	}

	_, _, err = execute(t, base, "registries", "delete-tags", "api", "v1", "v2", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Delete 2 tags of api from the registry of cluster sinaia: v1, v2 requires confirmation") || api.writes() != 0 {
		t.Fatal(err, api.requests)
	}
	_, errOut, err := execute(t, base, "registries", "delete-tags", "api", "v1", "v2", "--cluster", "sinaia", "--yes")
	if err != nil || api.body("DELETE /api/clusters/c1/registries/in-cluster/tags") != `{"force":false,"repository":"api","tags":["v1","v2"]}` {
		t.Fatal(errOut, err, api.body("DELETE /api/clusters/c1/registries/in-cluster/tags"))
	}
	inOrder(t, errOut, "✓ Deleted from api: v1, v2\n", "  Deleted with them, as tags of the same images: old\n")

	// A tag left out that shares an image stops the delete, and --force goes on.
	_, base = registryAPI(t, map[string][]string{"DELETE /api/clusters/c1/registries/in-cluster/tags": {`409 {"error":"Selection shares images with other tags","message":"The selected tag shares images with 1 other tag. Deleting the selection will remove those tags too.","conflictingTags":["latest"]}`}})
	_, _, err = execute(t, base, "registries", "delete-tags", "api", "v4", "--cluster", "sinaia", "--yes")
	if err == nil || !strings.Contains(err.Error(), "The selected tag shares images with 1 other tag.") || !strings.Contains(err.Error(), "To delete those tags too, rerun with --force") {
		t.Fatal(err)
	}
	api, base = registryAPI(t, map[string][]string{"DELETE /api/clusters/c1/registries/in-cluster/tags": {`{"success":false,"repository":"api","deletedTags":["v4"],"implicitlyDeletedTags":["latest"],"failedTags":["v3"]}`}})
	_, errOut, err = execute(t, base, "registries", "delete-tags", "api", "v4", "v3", "--force", "--cluster", "sinaia", "--yes")
	if err == nil || !strings.Contains(err.Error(), "the registry did not delete v3") || !strings.Contains(errOut, "✓ Deleted from api: v4") || api.body("DELETE /api/clusters/c1/registries/in-cluster/tags") != `{"force":true,"repository":"api","tags":["v4","v3"]}` {
		t.Fatal(errOut, err)
	}
	if _, _, err := execute(t, base, "registries", "delete-tags", "api", "--cluster", "sinaia"); err == nil {
		t.Fatal("deleted tags without naming one")
	}
}

func TestRegistryCompletion(t *testing.T) {
	_, base := registryAPI(t, nil)
	for _, test := range []struct {
		line string
		want []string
	}{
		{"registries apply ", []string{"ghcr\tgithub-registry · ghcr.io", "hub\tdocker-hub"}},
		{"registries remove --cluster sinaia ", []string{"ghcr\tgithub-registry · synced", "gone\tunknown · failed"}},
		{"registries tags --cluster sinaia ", []string{"api\t12 tags", "acme/worker\t1 tag"}},
		{"registries delete-tags --cluster sinaia api v4 ", []string{"v3", "v2", "v1"}},
		{"registries add quay --type g", []string{"docker-hub", "github-registry", "google-artifact", "aws-ecr", "custom"}},
	} {
		got, directive := completions(t, base, strings.Split(test.line, " ")...)
		if strings.Join(got, "\n") != strings.Join(test.want, "\n") || directive != shellNoFiles {
			t.Errorf("edka %s<TAB> offered %q %s, want %q", test.line, got, directive, test.want)
		}
	}
}

func TestBytesAreInBinaryUnits(t *testing.T) {
	for size, want := range map[float64]string{0: "0 B", 512: "512 B", 637521920: "608 MiB", 10737418240: "10 GiB", 1610612736: "1.5 GiB", 80797696: "77.1 MiB"} {
		if got := bytesOf(size); got != want {
			t.Errorf("%v: %q, want %q", size, got, want)
		}
	}
}
