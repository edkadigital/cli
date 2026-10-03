package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// stepAPI answers each route with its responses in turn, repeating the last,
// and records every request with its body.
type stepAPI struct {
	mu       sync.Mutex
	steps    map[string][]string
	requests []string
	bodies   map[string]string
	// queries holds the query string of the last request to each route.
	queries map[string]string
}

func newStepAPI(t *testing.T, steps map[string][]string) (*httptest.Server, *stepAPI) {
	t.Helper()
	interval := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = interval })
	api := &stepAPI{steps: steps, bodies: map[string]string{}, queries: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		route := r.Method + " " + r.URL.Path
		body, _ := io.ReadAll(r.Body)
		api.requests = append(api.requests, route)
		api.bodies[route] = string(body)
		api.queries[route] = r.URL.RawQuery
		responses, ok := api.steps[route]
		if !ok || len(responses) == 0 {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
			return
		}
		response := responses[0]
		if len(responses) > 1 {
			api.steps[route] = responses[1:]
		}
		var code int
		if n, _ := fmt.Sscanf(response, "%d ", &code); n == 1 && code >= 400 {
			w.WriteHeader(code)
			response = response[4:]
		}
		fmt.Fprint(w, response)
	}))
	t.Cleanup(server.Close)
	return server, api
}

func (s *stepAPI) count(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r == route {
			n++
		}
	}
	return n
}

func createSteps() map[string][]string {
	return map[string][]string{
		"GET /api/clusters/locations":        {`{"data":["nbg1","fsn1","hel1","ash","hil","sin"]}`},
		"GET /api/clusters/k3s-versions":     {`{"data":{"default":"v1.36.4+k3s1","versions":["v1.37.0+k3s1","v1.36.4+k3s1","v1.35.6+k3s1"]}}`},
		"POST /api/clusters/pricing-preview": {`{"pricing":{"mode":"project_pricing","exact":true,"currency":"EUR","monthly_net":13.4,"line_items":[],"warnings":["Prices exclude traffic"]}}`},
		"POST /api/clusters":                 {`{"message":"Cluster created successfully","clusterId":"c9"}`},
	}
}

func TestClusterCreateUsesTheConsoleDefaultsAfterConfirmation(t *testing.T) {
	server, api := newStepAPI(t, createSteps())

	_, errOut, err := execute(t, server.URL, "clusters", "create", "staging")
	if err == nil || !strings.Contains(err.Error(), "--yes") || api.count("POST /api/clusters") != 0 {
		t.Fatal(api.requests, err)
	}
	for _, want := range []string{
		"Cluster staging in fsn1, k3s v1.36.4+k3s1",
		"Control plane: 1 × cx23",
		"Node pool default: 1 × cx23",
		"Deletion protection on, etcd backup daily at ",
		"Estimated 13.40 EUR per month, excluding VAT",
		"Note: Prices exclude traffic",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("missing %q in\n%s", want, errOut)
		}
	}

	out, errOut, err := execute(t, server.URL, "clusters", "create", "staging", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Name               string `json:"name"`
		Location           string `json:"location"`
		K3sVersion         string `json:"k3s_version"`
		MasterInstanceType string `json:"master_instance_type"`
		MasterHA           bool   `json:"master_ha"`
		Protected          bool   `json:"protected"`
		EtcdBackup         struct {
			Enabled bool   `json:"enabled"`
			Cron    string `json:"snapshot_schedule_cron"`
		} `json:"etcd_backup"`
		Pools []struct {
			Name     string `json:"name"`
			Type     string `json:"instance_type"`
			Count    int    `json:"instance_count"`
			Location string `json:"location"`
		} `json:"worker_node_pools"`
	}
	if err := json.Unmarshal([]byte(api.bodies["POST /api/clusters"]), &body); err != nil {
		t.Fatal(err, api.bodies["POST /api/clusters"])
	}
	if body.Name != "staging" || body.Location != "fsn1" || body.K3sVersion != "v1.36.4+k3s1" || body.MasterInstanceType != "cx23" || body.MasterHA || !body.Protected {
		t.Fatalf("%+v", body)
	}
	var minute, hour int
	if n, _ := fmt.Sscanf(body.EtcdBackup.Cron, "%d %d * * *", &minute, &hour); !body.EtcdBackup.Enabled || n != 2 || hour*60+minute > 6*60 {
		t.Fatalf("%+v", body.EtcdBackup)
	}
	if len(body.Pools) != 1 || body.Pools[0].Name != "default" || body.Pools[0].Type != "cx23" || body.Pools[0].Count != 1 || body.Pools[0].Location != "" {
		t.Fatalf("%+v", body.Pools)
	}
	var preview map[string]any
	if err := json.Unmarshal([]byte(api.bodies["POST /api/clusters/pricing-preview"]), &preview); err != nil {
		t.Fatal(err)
	}
	if preview["use_global_token"] != true || preview["master_instance_type"] != "cx23" {
		t.Fatal(preview)
	}
	if !strings.Contains(out, "c9") || !strings.Contains(errOut, "✓ Creating cluster staging\n  Next: edka clusters get staging") {
		t.Fatal(out, errOut)
	}
}

func TestClusterCreateFlagsAndOverrides(t *testing.T) {
	server, api := newStepAPI(t, createSteps())

	out, _, err := execute(t, server.URL, "clusters", "create", "production", "--ha", "--nodes", "3", "--node-type", "cx33", "--location", "nbg1",
		"--k3s-version", "v1.35.6+k3s1", "--no-protect", "--no-etcd-backup", "--field", "nat_gateway_enabled:=true", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err, out)
	}
	pools, _ := body["worker_node_pools"].([]any)
	pool, _ := pools[0].(map[string]any)
	if body["master_ha"] != true || body["location"] != "nbg1" || body["k3s_version"] != "v1.35.6+k3s1" || body["protected"] != false ||
		body["etcd_backup"] != nil || body["nat_gateway_enabled"] != true || pool["instance_type"] != "cx33" || pool["instance_count"] != float64(3) {
		t.Fatal(body)
	}
	if api.count("POST /api/clusters") != 0 {
		t.Fatal("--dry-run created a cluster")
	}

	// A token for the create stays out of the price preview, which uses the organization's.
	out, _, err = execute(t, server.URL, "clusters", "create", "tokened", "--dry-run", "--data", `{"hetzner_token":"project-token","temporary_hetzner_token":"one-off"}`)
	if err != nil || !strings.Contains(out, `"hetzner_token": "project-token"`) {
		t.Fatal(out, err)
	}
	if preview := api.bodies["POST /api/clusters/pricing-preview"]; strings.Contains(preview, "project-token") || strings.Contains(preview, "one-off") || !strings.Contains(preview, `"use_global_token":true`) {
		t.Fatal(preview)
	}

	// --data replaces whole top-level fields, here the pools.
	out, errOut, err := execute(t, server.URL, "clusters", "create", "batch", "--dry-run", "--data", `{"worker_node_pools":[{"name":"spot","instance_type":"cx43","autoscaling_enabled":true,"autoscaling_min_instances":0,"autoscaling_max_instances":4}]}`)
	if err != nil || !strings.Contains(out, `"name": "spot"`) || strings.Contains(out, `"name": "default"`) || !strings.Contains(errOut, "Node pool spot: 0–4 × cx43") {
		t.Fatal(out, errOut, err)
	}
}

func TestClusterCreateRejectsBadInputBeforeCreating(t *testing.T) {
	server, api := newStepAPI(t, createSteps())
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(strings.Replace(minikubeKubeconfig, "- name: minikube\n  context:", "- name: edka-staging\n  context:", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"Bad_Name"}, "lowercase letters"},
		{[]string{"ab"}, "3 to 32"},
		{[]string{"staging", "--location", "mars1"}, `unknown location "mars1"; choose one of nbg1, fsn1`},
		{[]string{"staging", "--k3s-version", "v1.20.0+k3s1"}, "Edka doesn't install k3s v1.20.0+k3s1"},
		{[]string{"staging", "--nodes", "0"}, "--nodes must be at least 1"},
		{[]string{"staging", "--merge-kubeconfig"}, "--merge-kubeconfig needs --wait"},
		{[]string{"staging", "--use"}, "--use needs --merge-kubeconfig"},
		{[]string{"staging", "--wait", "--merge-kubeconfig", "--yes"}, "wasn't written by edka"},
	} {
		_, _, err := execute(t, server.URL, append([]string{"clusters", "create"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	if api.count("POST /api/clusters") != 0 {
		t.Fatal("created a cluster from bad input:", api.requests)
	}
}

func TestClusterCreateNeedsAVersionWithoutTheVersionsRoute(t *testing.T) {
	steps := createSteps()
	delete(steps, "GET /api/clusters/k3s-versions")
	server, _ := newStepAPI(t, steps)

	if _, _, err := execute(t, server.URL, "clusters", "create", "staging", "--yes"); err == nil || !strings.Contains(err.Error(), "pass --k3s-version") {
		t.Fatal(err)
	}
	if _, _, err := execute(t, server.URL, "clusters", "create", "staging", "--yes", "--k3s-version", "v1.36.4+k3s1"); err != nil {
		t.Fatal(err)
	}
}

func TestClusterCreateExplainsLimitsAndMissingTokens(t *testing.T) {
	for _, tc := range []struct {
		response, want string
		retryHint      bool
	}{
		// Edka's plan limits, with their own explanation.
		{`429 {"error":"Cluster limit reached","message":"You have reached the maximum number of active clusters allowed (3). You can purchase additional clusters or upgrade your plan.","max":3,"current":3}`, "maximum number of active clusters allowed (3)", false},
		{`429 {"error":"vCPU limit reached","message":"Free-plan clusters are limited to 8 vCPUs (this configuration needs 12). Upgrade to the Standard or Pro plan for more.","code":"quota_exceeded"}`, "limited to 8 vCPUs", false},
		// Any other 429 is rate limiting and keeps the advice to wait.
		{`429 {"error":"Too many requests"}`, "Too many requests", true},
		{`400 {"error":"Hetzner API token is required"}`, "Save the organization's Hetzner token", false},
	} {
		steps := createSteps()
		steps["POST /api/clusters"] = []string{tc.response}
		server, _ := newStepAPI(t, steps)
		_, _, err := execute(t, server.URL, "clusters", "create", "staging", "--yes")
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "Wait before trying again") != tc.retryHint {
			t.Errorf("%s: %v", tc.response, err)
		}
	}
}

const stagingKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: staging
  cluster:
    server: https://10.0.0.9:6443
contexts:
- name: staging-user
  context:
    cluster: staging
    user: edka-user-0f8fad5b
current-context: staging-user
users:
- name: edka-user-0f8fad5b
  user:
    token: new-cluster-token
`

func TestClusterCreateWaitsAndMergesTheKubeconfig(t *testing.T) {
	steps := createSteps()
	steps["GET /api/clusters/c9"] = []string{
		`{"data":[{"id":"c9","name":"staging","status":"creating","progress":10}]}`,
		`{"data":[{"id":"c9","name":"staging","status":"installing_addons","progress":95}]}`,
		`{"data":[{"id":"c9","name":"staging","status":"active","progress":100,"location":"fsn1"}]}`,
	}
	steps["GET /api/clusters/c9/events"] = []string{
		`{"data":[{"id":1,"progress":10,"message":"Creating network"}]}`,
		`{"data":[{"id":3,"progress":95,"message":"Installing add-ons"},{"id":2,"progress":60,"message":"Joining nodes"},{"id":1,"progress":10,"message":"Creating network"}]}`,
		`{"data":[{"id":4,"progress":100,"message":"Cluster ready"},{"id":3,"progress":95,"message":"Installing add-ons"},{"id":2,"progress":60,"message":"Joining nodes"},{"id":1,"progress":10,"message":"Creating network"}]}`,
	}
	steps["GET /api/clusters/c9/user-kubeconfig/download"] = []string{stagingKubeconfig}
	server, api := newStepAPI(t, steps)
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(minikubeKubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)

	out, errOut, err := execute(t, server.URL, "clusters", "create", "staging", "--yes", "--wait", "--merge-kubeconfig", "--use")
	if err != nil {
		t.Fatal(err, errOut)
	}
	progress := regexp.MustCompile(`(?m)^ *\d+% .*$`).FindAllString(errOut, -1)
	if strings.Join(progress, "|") != " 10% Creating network| 60% Joining nodes| 95% Installing add-ons|100% Cluster ready" {
		t.Fatalf("%q\n%s", progress, errOut)
	}
	for _, want := range []string{"Creating cluster staging…", "✓ Cluster staging is active", "as context edka-staging", "kubectl now uses edka-staging"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("missing %q in\n%s", want, errOut)
		}
	}
	if !strings.Contains(out, "staging") {
		t.Fatal("no cluster on stdout:", out)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "current-context: edka-staging") || !strings.Contains(string(data), "token: new-cluster-token") {
		t.Fatal(string(data))
	}
	if api.count("GET /api/clusters/c9/user-kubeconfig/download") != 1 {
		t.Fatal(api.requests)
	}
}

func TestClusterCreateWaitSurvivesFailedReads(t *testing.T) {
	steps := createSteps()
	steps["GET /api/clusters/c9"] = []string{
		`{"data":[{"id":"c9","name":"staging","status":"creating"}]}`,
		`503 {"message":"Service unavailable"}`,
		`502 <html>Bad Gateway</html>`,
		`{"data":[{"id":"c9","name":"staging","status":"active","location":"fsn1"}]}`,
	}
	steps["GET /api/clusters/c9/events"] = []string{`{"data":[]}`}
	server, _ := newStepAPI(t, steps)

	out, errOut, err := execute(t, server.URL, "clusters", "create", "staging", "--yes", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut,
		"Creating servers and installing k3s…\n",
		"Could not read from Edka, trying again: Service unavailable (HTTP 503)\n",
		"Could not read from Edka, trying again: Bad Gateway (HTTP 502)\n",
		"✓ Cluster staging is active\n",
	)
	if !strings.Contains(out, "staging") {
		t.Fatal("no cluster on stdout:", out)
	}
}

func TestClusterCreateReportsAFailedCluster(t *testing.T) {
	steps := createSteps()
	steps["GET /api/clusters/c9"] = []string{
		`{"data":[{"id":"c9","name":"staging","status":"creating"}]}`,
		`{"data":[{"id":"c9","name":"staging","status":"failed"}]}`,
	}
	steps["GET /api/clusters/c9/events"] = []string{`{"data":[{"id":7,"progress":40,"message":"Server limit exceeded in fsn1"}]}`}
	server, _ := newStepAPI(t, steps)

	_, _, err := execute(t, server.URL, "clusters", "create", "staging", "--yes", "--wait")
	if err == nil || err.Error() != "cluster staging failed: Server limit exceeded in fsn1\nFind the cause with `edka clusters diagnose staging`" {
		t.Fatal(err)
	}
}

func TestClusterCreateWaitTimeoutKeepsTheClusterGoing(t *testing.T) {
	steps := createSteps()
	steps["GET /api/clusters/c9"] = []string{`{"data":[{"id":"c9","name":"staging","status":"creating"}]}`}
	steps["GET /api/clusters/c9/events"] = []string{`{"data":[]}`}
	server, _ := newStepAPI(t, steps)

	_, errOut, err := execute(t, server.URL, "clusters", "create", "staging", "--yes", "--wait", "--wait-timeout", "50ms")
	if err == nil || !strings.Contains(err.Error(), "it keeps provisioning, follow it with `edka clusters get staging`") {
		t.Fatal(err)
	}
	if strings.Count(errOut, "Creating servers and installing k3s…") != 1 {
		t.Fatal("the status line should print once:", errOut)
	}
}
