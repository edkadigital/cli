package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeAPI answers GET routes from fixtures and records every request.
func fakeAPI(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	requests := []string{}
	// Lists of several clusters arrive together.
	var recording sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			request += "?" + r.URL.RawQuery
		}
		recording.Lock()
		requests = append(requests, request)
		recording.Unlock()
		if body, ok := routes[r.Method+" "+r.URL.Path]; ok {
			// A fixture such as "403 {...}" answers with that status.
			var code int
			if n, _ := fmt.Sscanf(body, "%d ", &code); n == 1 && code >= 400 {
				w.WriteHeader(code)
				body = body[4:]
			}
			fmt.Fprint(w, body)
			return
		}
		if r.Method == "GET" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
			return
		}
		fmt.Fprint(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

var appFixtures = map[string]string{
	"GET /api/clusters":                   `{"data":[{"id":"c1","name":"sinaia"},{"id":"c2","name":"staging"}]}`,
	"GET /api/clusters/c1/apps/instances": `{"success":true,"data":[{"id":"a1","cluster_id":"c1","app_name":"strapi","instance_name":"blog","version":"5.46.0","status":"installed","namespace":"strapi"}]}`,
	"GET /api/clusters/c2/apps/instances": `{"success":true,"data":[{"id":"a2","cluster_id":"c2","app_name":"excalidraw","display_name":"Whiteboard","instance_name":"default","version":"0.18.1","status":"installed","namespace":"excalidraw"}]}`,
	"GET /api/clusters/c1/apps/a1":        `{"success":true,"data":{"id":"a1","app_name":"strapi","instance_name":"blog","version":"5.46.0","status":"installed","namespace":"strapi","release_name":"blog"}}`,
	"GET /api/clusters/c1/apps/a1/logs":   `{"logs":"started\n","podName":"blog-0"}`,
	"GET /api/apps/catalog":               `{"success":true,"data":[{"name":"Strapi","slug":"strapi","category":"cms","version":"5.46.0","description":"Headless CMS"}]}`,
	"GET /api/deployments":                `{"data":[{"id":"d1","name":"api","status":"deployed","image_repository":"ghcr.io/acme/api","image_tag":"v2","replicas":2,"cluster_name":"sinaia","hostnames":["api.acme.dev"]},{"id":"d2","name":"worker","status":"deploying","image_repository":"ghcr.io/acme/worker","autoscale_enabled":true,"min_replicas":2,"max_replicas":5,"cluster_name":"staging"}]}`,
	"GET /api/clusters/c1/deployments":    `{"data":[{"id":"d1","name":"api","status":"deployed","image_repository":"ghcr.io/acme/api","image_tag":"v2","replicas":2}]}`,
	"GET /api/deployments/d1/status":      `{"data":{"name":"api","status":"deployed","running_image":"ghcr.io/acme/api:v1","configured_image":"ghcr.io/acme/api:v2","version_mismatch":true,"replicas":{"desired":2,"ready":1,"available":1,"updated":1},"pods":[{"name":"api-7d9","status":"running","ready":true,"restartCount":0},{"name":"api-x2k","status":"failed","ready":false,"restartCount":4,"reason":"CrashLoopBackOff"}]}}`,
	"GET /api/deployments/d1/revisions":   `{"data":[{"generation":5,"status":"healthy","image_repository":"ghcr.io/acme/api","image_tag":"v2","source":"manual","actor_name":"Camil","created_at":"2026-09-30T08:00:00Z"}],"pagination":{"limit":20,"offset":0,"total":1,"hasMore":false}}`,
}

func TestAppsListShowsNamesAcrossClusters(t *testing.T) {
	server, requests := fakeAPI(t, appFixtures)
	out, _, err := execute(t, server.URL, "apps", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "NAME APP VERSION STATUS CLUSTER NAMESPACE" {
		t.Fatal(out)
	}
	if !strings.HasPrefix(lines[1], "blog ") || !strings.Contains(lines[1], "sinaia") || !strings.HasPrefix(lines[2], "Whiteboard ") || !strings.Contains(lines[2], "staging") {
		t.Fatal(out)
	}
	*requests = nil
	out, _, err = execute(t, server.URL, "apps", "list", "--cluster", "sinaia", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(out), &body) != nil || len(body.Data) != 1 || body.Data[0]["cluster_name"] != "sinaia" {
		t.Fatal(out)
	}
	if got := strings.Join(*requests, ","); got != "GET /api/clusters,GET /api/clusters/c1/apps/instances" {
		t.Fatal(got)
	}
}

func TestAppsResolveByNameAndUninstall(t *testing.T) {
	server, requests := fakeAPI(t, appFixtures)
	out, _, err := execute(t, server.URL, "apps", "get", "blog")
	if err != nil || !strings.Contains(out, "strapi") || !strings.Contains(out, "sinaia") {
		t.Fatal(out, err)
	}
	if _, _, err := execute(t, server.URL, "apps", "uninstall"); err == nil {
		t.Fatal("uninstall ran without naming an app")
	}
	*requests = nil
	_, _, err = execute(t, server.URL, "apps", "uninstall", "Whiteboard")
	if err == nil || !strings.Contains(err.Error(), "--yes") || strings.Contains(strings.Join(*requests, ","), "DELETE") {
		t.Fatal(*requests, err)
	}
	if _, _, err := execute(t, server.URL, "apps", "uninstall", "Whiteboard", "--yes"); err != nil {
		t.Fatal(err)
	}
	if last := (*requests)[len(*requests)-1]; last != "DELETE /api/clusters/c2/apps/a2" {
		t.Fatal(last)
	}
	if _, _, err := execute(t, server.URL, "apps", "get", "strapi"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("app type selected an instance: %v", err)
	}
	if _, _, err := execute(t, server.URL, "apps", "get", "Whiteboard", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "not found in cluster sinaia; choose another with --cluster") {
		t.Fatal(err)
	}
}

func TestAmbiguousNamesListMatches(t *testing.T) {
	fixtures := map[string]string{}
	for k, v := range appFixtures {
		fixtures[k] = v
	}
	fixtures["GET /api/clusters/c2/apps/instances"] = `{"data":[{"id":"a3","cluster_id":"c2","app_name":"strapi","instance_name":"blog"}]}`
	server, _ := fakeAPI(t, fixtures)
	_, _, err := execute(t, server.URL, "apps", "logs", "blog")
	if err == nil || !strings.Contains(err.Error(), "2 apps match") || !strings.Contains(err.Error(), "a1") || !strings.Contains(err.Error(), "a3") {
		t.Fatal(err)
	}
}

func TestAppLogsPassPodSelection(t *testing.T) {
	server, requests := fakeAPI(t, appFixtures)
	out, _, err := execute(t, server.URL, "apps", "logs", "blog", "--tail", "50", "--pod", "blog-0", "--container", "web", "--previous")
	if err != nil || out != "started\n" {
		t.Fatal(out, err)
	}
	if last := (*requests)[len(*requests)-1]; last != "GET /api/clusters/c1/apps/a1/logs?container=web&podName=blog-0&previous=true&tailLines=50" {
		t.Fatal(last)
	}
	if _, _, err := execute(t, server.URL, "logs", "api", "--follow", "--previous"); err == nil {
		t.Fatal("followed previous container logs")
	}
}

func TestDeploymentsListAndStatus(t *testing.T) {
	server, requests := fakeAPI(t, appFixtures)
	out, _, err := execute(t, server.URL, "deployments", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "IMAGE", "REPLICAS", "ghcr.io/acme/api:v2", "api.acme.dev", "2–5 auto", "ghcr.io/acme/worker:latest"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	*requests = nil
	out, _, err = execute(t, server.URL, "deployments", "list", "--cluster", "sinaia")
	if err != nil || !strings.Contains(out, "sinaia") || strings.Join(*requests, ",") != "GET /api/clusters,GET /api/clusters/c1/deployments" {
		t.Fatal(out, *requests, err)
	}
	out, _, err = execute(t, server.URL, "deployments", "status", "api")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1/2 ready", "ghcr.io/acme/api:v1 (configured ghcr.io/acme/api:v2)", "POD", "api-x2k", "CrashLoopBackOff"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	out, _, err = execute(t, server.URL, "deployments", "revisions", "api")
	if err != nil || !strings.Contains(out, "GENERATION") || !strings.Contains(out, "Camil") || (*requests)[len(*requests)-1] != "GET /api/deployments/d1/revisions?limit=20" {
		t.Fatal(out, err)
	}
}

func TestBuildPickerOffersOnlyGitDeployments(t *testing.T) {
	rows := []map[string]any{
		{"id": "d1", "name": "api", "status": "deployed", "cluster_name": "sinaia", "github_deployment_id": "g1", "github_repository_full_name": "acme/api", "github_ref": "refs/heads/main", "image_repository": "registry.edka.dev/api", "image_tag": "abc1234"},
		{"id": "d2", "name": "nginx", "status": "deployed", "cluster_name": "sinaia", "github_deployment_id": nil, "image_repository": "nginx", "image_tag": "1.27"},
		// Rows from the organization-wide list don't say whether they have a repository.
		{"id": "d3", "name": "worker", "status": "failed", "cluster_name": "sinaia", "image_repository": "ghcr.io/acme/worker"},
	}
	shown := func(candidates []candidate) string {
		lines := []string{}
		for _, c := range candidates {
			lines = append(lines, c.Name+": "+c.Detail)
		}
		return strings.Join(lines, "\n")
	}
	if got := shown(deploymentCandidates(rows, true)); got != "api: deployed · sinaia · acme/api @ main\nworker: failed · sinaia · ghcr.io/acme/worker:latest" {
		t.Fatal(got)
	}
	if got := shown(deploymentCandidates(rows, false)); !strings.Contains(got, "nginx: deployed · sinaia · nginx:1.27") {
		t.Fatal(got)
	}
}

func TestDeploymentDeleteNeedsNameAndConfirmation(t *testing.T) {
	server, requests := fakeAPI(t, appFixtures)
	t.Setenv("EDKA_DEPLOYMENT", "api")
	if _, _, err := execute(t, server.URL, "deployments", "delete"); err == nil {
		t.Fatal("deleted the context deployment without naming it")
	}
	_, _, err := execute(t, server.URL, "deployments", "delete", "api")
	if err == nil || !strings.Contains(err.Error(), "Delete deployment api in cluster sinaia") {
		t.Fatal(err)
	}
	if _, _, err := execute(t, server.URL, "deployments", "delete", "api", "--yes"); err != nil {
		t.Fatal(err)
	}
	if last := (*requests)[len(*requests)-1]; last != "DELETE /api/deployments/d1" {
		t.Fatal(last)
	}
}

func TestLifecycleConfirmationsNameTheDeployment(t *testing.T) {
	server, requests := fakeAPI(t, appFixtures)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"scale", "api", "--replicas", "0"}, "Scale deployment api in cluster sinaia to zero replicas"},
		{[]string{"deployments", "rollback", "api", "--generation", "4"}, "Roll back deployment api in cluster sinaia to generation 4"},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	for _, request := range *requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("changed a deployment without confirmation: %s", request)
		}
	}
}

func TestEndpointCommandsLiveUnderAPI(t *testing.T) {
	server, requests := fakeAPI(t, appFixtures)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"inventory", "resources", "list"}, "edka api inventory resources list"},
		{[]string{"clusters", "nodepools", "list", "--cluster", "production"}, "edka api clusters nodepools list --cluster production"},
		{[]string{"clusters", "apps", "list", "--cluster", "sinaia"}, "Apps have their own commands: edka apps list --cluster sinaia\nAPI endpoint commands are under `edka api`: edka api clusters apps list --cluster sinaia"},
		{[]string{"clusters", "databases", "backups", "list"}, "Databases have their own commands: edka databases --help"},
		{[]string{"cluster", "deployments", "list"}, "Deployments have their own commands: edka deployments list"},
		{[]string{"clusters", "apps", "get", "strapi"}, "Apps have their own commands: edka apps get strapi"},
		{[]string{"clusters", "addons", "list", "--cluster", "sinaia"}, "Addons have their own commands: edka addons list --cluster sinaia\nAPI endpoint commands are under `edka api`: edka api clusters addons list --cluster sinaia"},
		{[]string{"addons", "categories", "list"}, "edka api addons categories list"},
		{[]string{"cluster", "nodepools", "update", "--field", "name=a b"}, "edka api clusters nodepools update --field 'name=a b'"},
		{[]string{"apps", "instances", "list"}, "edka api apps instances list"},
		{[]string{"apps", "lsit"}, "Did you mean this?\n\tlist"},
		{[]string{"api", "clusters", "bogus"}, `unknown command "bogus" for "edka api clusters"`},
		{[]string{"profile", "bogus"}, `unknown command "bogus"`},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	if len(*requests) != 0 {
		t.Fatalf("hints reached the API: %v", *requests)
	}
	var help bytes.Buffer
	root := New("test", strings.NewReader(""), &help, &bytes.Buffer{})
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^\s+inventory\s`).MatchString(help.String()) {
		t.Fatal(help.String())
	}
	for _, group := range []string{"addons", "apps", "clusters", "cronjobs", "databases", "deployments"} {
		if !strings.Contains(help.String(), "\n  "+group+" ") {
			t.Errorf("help does not list %s", group)
		}
	}
	for _, command := range []string{"restart", "scale", "rollback", "build", "logs", "deployments restart", "deployments build", "deployments logs"} {
		if cmd, args, err := root.Find(strings.Fields(command)); err != nil || len(args) != 0 || cmd.RunE == nil {
			t.Errorf("%s is not a command", command)
		}
	}
}

func withFixtures(extra map[string]string) map[string]string {
	fixtures := map[string]string{}
	for k, v := range appFixtures {
		fixtures[k] = v
	}
	for k, v := range extra {
		fixtures[k] = v
	}
	return fixtures
}

var cronjobFixtures = withFixtures(map[string]string{
	"GET /api/clusters/c1/cronjobs": `{"data":[{"id":"j1","name":"nightly-report","schedule":"0 3 * * *","status":"deployed","suspend":false,"image_repository":"ghcr.io/acme/report","image_tag":"v4","lastScheduleTime":"2026-09-30T03:00:00Z","lastJobStatus":"Succeeded"}]}`,
	"GET /api/clusters/c2/cronjobs": `{"data":[{"id":"j2","name":"cleanup","schedule":"*/15 * * * *","status":"deployed","suspend":true,"image_repository":"ghcr.io/acme/cleanup"}]}`,
	"GET /api/cronjobs/j1":          `{"data":{"id":"j1","name":"nightly-report","schedule":"0 3 * * *","status":"deployed","suspend":false,"image_repository":"ghcr.io/acme/report","image_tag":"v4","run_command":"report --all","namespace":"reports"}}`,
	"GET /api/cronjobs/j1/status":   `{"data":{"lastScheduleTime":"2026-09-30T03:00:00Z","activeJobs":0,"lastSuccessfulTime":"2026-09-30T03:04:10Z"}}`,
	"GET /api/cronjobs/j1/jobs":     `{"data":[{"name":"nightly-report-29311","status":"Succeeded","startTime":"2026-09-30T03:00:02Z","completionTime":"2026-09-30T03:04:10Z"},{"name":"nightly-report-manual-7","status":"Running","startTime":"2026-09-30T09:00:00Z","completionTime":null}]}`,
	"GET /api/cronjobs/j1/logs":     `{"logs":"report sent\n","podName":"nightly-report-29311-x","jobName":"nightly-report-29311"}`,
})

func TestCronjobsListRunsAndLogs(t *testing.T) {
	server, requests := fakeAPI(t, cronjobFixtures)
	out, _, err := execute(t, server.URL, "cronjobs", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SCHEDULE", "LAST RUN", "nightly-report", "0 3 * * *", "(succeeded)", "ghcr.io/acme/report:v4", "cleanup", "suspended", "staging"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	out, _, err = execute(t, server.URL, "cronjobs", "get", "nightly-report")
	if err != nil || !strings.Contains(out, "report --all") || !strings.Contains(out, "Last success") {
		t.Fatal(out, err)
	}
	out, _, err = execute(t, server.URL, "cronjobs", "runs", "nightly-report")
	if err != nil || !strings.Contains(out, "4m8s") || !strings.Contains(out, "nightly-report-manual-7") || (*requests)[len(*requests)-1] != "GET /api/cronjobs/j1/jobs?limit=20" {
		t.Fatal(out, err)
	}
	if _, _, err := execute(t, server.URL, "cronjobs", "runs", "nightly-report", "--limit", "101"); err == nil {
		t.Fatal("accepted a limit Edka rejects")
	}
	out, _, err = execute(t, server.URL, "cronjobs", "logs", "nightly-report", "--run", "nightly-report-29311")
	if err != nil || out != "report sent\n" || (*requests)[len(*requests)-1] != "GET /api/cronjobs/j1/logs?jobName=nightly-report-29311&tailLines=100" {
		t.Fatal(out, *requests, err)
	}
}

func TestCronjobActionsNeedNames(t *testing.T) {
	server, requests := fakeAPI(t, cronjobFixtures)
	for _, action := range []string{"trigger", "suspend", "resume", "delete"} {
		if _, _, err := execute(t, server.URL, "cronjobs", action); err == nil {
			t.Errorf("%s ran without naming a cronjob", action)
		}
	}
	*requests = nil
	_, errOut, err := execute(t, server.URL, "cronjobs", "trigger", "nightly-report")
	if err != nil || !strings.Contains(errOut, "Queued a run of nightly-report") || (*requests)[len(*requests)-1] != "POST /api/cronjobs/j1/trigger" {
		t.Fatal(errOut, *requests, err)
	}
	for action, want := range map[string]string{"suspend": `{"suspend":true}`, "resume": `{"suspend":false}`} {
		var body string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "PATCH" {
				data, _ := io.ReadAll(r.Body)
				body = r.URL.Path + " " + string(data)
			}
			if fixture, ok := cronjobFixtures[r.Method+" "+r.URL.Path]; ok {
				fmt.Fprint(w, fixture)
				return
			}
			fmt.Fprint(w, `{"data":{}}`)
		}))
		if _, _, err := execute(t, server.URL, "cronjobs", action, "nightly-report"); err != nil || body != "/api/cronjobs/j1/suspend "+want {
			t.Errorf("%s: %q %v", action, body, err)
		}
		server.Close()
	}
	_, _, err = execute(t, server.URL, "cronjobs", "delete", "cleanup")
	if err == nil || !strings.Contains(err.Error(), "Delete cronjob cleanup in cluster staging") {
		t.Fatal(err)
	}
	if _, _, err := execute(t, server.URL, "cronjobs", "delete", "cleanup", "--yes"); err != nil || (*requests)[len(*requests)-1] != "DELETE /api/cronjobs/j2" {
		t.Fatal(*requests, err)
	}
}

var clusterFixtures = withFixtures(map[string]string{
	"GET /api/clusters":                             `{"data":[{"id":"c1","name":"sinaia","status":"active","provider":"hetzner","location":"fsn1","k3s_version":"v1.33.1+k3s1","keel_webhook_token":"secret-token","worker_node_pools":[{"name":"workers","instance_type":"cx32","instance_count":3,"actual_count":3},{"name":"gpu","instance_type":"gx11","instance_count":1,"actual_count":0}]},{"id":"c2","name":"staging","status":"active","provider":"byo"}]}`,
	"GET /api/clusters/c1":                          `{"data":[{"id":"c1","name":"sinaia","status":"active","provider":"hetzner","location":"fsn1","k3s_version":"v1.33.1+k3s1","apiEndpoint":"https://10.0.0.1:6443","connectivity_status":"connected","master_instance_type":"cx22","master_instance_count":3,"master_ha":true,"protected":true,"monthly_price":"84.20","pricing_currency":"EUR","date_created":"2026-01-10T12:00:00Z","keel_webhook_token":"secret-token","worker_node_pools":[{"name":"workers","instance_type":"cx32","instance_count":3,"actual_count":3,"location":"fsn1","autoscaling_enabled":true,"autoscaling_min_instances":2,"autoscaling_max_instances":6}]}]}`,
	"GET /api/clusters/c1/user-kubeconfig/download": "apiVersion: v1\nkind: Config\n",
	"GET /api/clusters/c2/user-kubeconfig/download": `403 {"error":"Forbidden","message":"Your kubeconfig has already been downloaded. Use the rotate feature to generate a new one."}`,
	"DELETE /api/clusters/c1":                       `403 {"error":"Cluster is protected","message":"Cannot delete protected cluster: sinaia. Please disable protection first."}`,
	"GET /api/clusters/c1/databases":                `{"success":true,"databases":[{"id":"db1","cluster_id":"c1","engine":"postgresql","database_name":"orders","version":"17","status":"ready","configuration":{"instances_count":"3","storage_size":20,"namespace":"postgres","backup_enabled":true}}]}`,
	"GET /api/clusters/c2/databases":                `{"success":true,"databases":[{"id":"db2","cluster_id":"c2","engine":"valkey","database_name":"cache","version":"8","status":"provisioning","configuration":{"instances_count":"1","storage_size":5}}]}`,
	"GET /api/clusters/c1/databases/db1":            `{"success":true,"database":{"id":"db1","engine":"postgresql","database_name":"orders","version":"17","status":"ready","configuration":{"instances_count":"3","storage_size":20,"namespace":"postgres","backup_enabled":true},"created_at":"2026-02-01T10:00:00Z"}}`,
	"GET /api/clusters/c1/databases/db1/status":     `{"success":true,"status":{"available":true,"instances":3,"readyInstances":2,"currentPrimary":"orders-1","lastSuccessfulBackup":"2026-09-30T02:00:00Z","recoveryWindow":{"earliest":"2026-09-01T00:00:00Z","latest":"2026-09-30T08:00:00Z"},"backups":[{"name":"orders-20260929","phase":"completed","method":"barmanObjectStore","startedAt":"2026-09-29T02:00:00Z","stoppedAt":"2026-09-29T02:03:30Z"},{"name":"orders-20260930","phase":"completed","method":"barmanObjectStore","startedAt":"2026-09-30T02:00:00Z","stoppedAt":"2026-09-30T02:02:00Z"}]}}`,
	"GET /api/clusters/c1/databases/db1/logs":       `{"logs":"checkpoint complete\n","podName":"orders-1"}`,
})

func TestClustersListAndGetHideWebhookToken(t *testing.T) {
	server, _ := fakeAPI(t, clusterFixtures)
	out, _, err := execute(t, server.URL, "clusters", "list")
	if err != nil || !strings.Contains(out, "WORKERS") || !strings.Contains(out, "3/4") || !strings.Contains(out, "v1.33.1+k3s1") {
		t.Fatal(out, err)
	}
	for _, args := range [][]string{{"clusters", "list", "--json"}, {"clusters", "get", "sinaia", "--json"}} {
		out, _, err := execute(t, server.URL, args...)
		if err != nil || strings.Contains(out, "secret-token") || !json.Valid([]byte(out)) {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	for _, args := range [][]string{{"clusters", "get", "sinaia"}, {"status", "--cluster", "sinaia"}} {
		out, _, err := execute(t, server.URL, args...)
		for _, want := range []string{"3 × cx22 (HA)", "https://10.0.0.1:6443", "84.20 EUR per month", "Protected", "POOL", "2–6"} {
			if err != nil || !strings.Contains(out, want) {
				t.Fatalf("%v: missing %q in\n%s %v", args, want, out, err)
			}
		}
	}
}

func TestKubeconfigIsSavedOnceToANewPrivateFile(t *testing.T) {
	server, requests := fakeAPI(t, clusterFixtures)
	dir := t.TempDir()
	if _, _, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia"); err == nil || !strings.Contains(err.Error(), "--output-file") {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "existing.yaml")
	if err := os.WriteFile(existing, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	*requests = nil
	if _, _, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia", "--output-file", existing); err == nil || !strings.Contains(err.Error(), "already exists") || len(*requests) != 0 {
		t.Fatal(*requests, err)
	}
	path := filepath.Join(dir, "kube", "sinaia.yaml")
	_, errOut, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia", "--output-file", path)
	data, _ := os.ReadFile(path)
	if err != nil || string(data) != "apiVersion: v1\nkind: Config\n" || !strings.Contains(errOut, "Edka issues it once") {
		t.Fatal(string(data), errOut, err)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	_, _, err = execute(t, server.URL, "clusters", "kubeconfig", "staging", "--output-file", filepath.Join(dir, "staging.yaml"))
	if err == nil || !strings.Contains(err.Error(), "edka clusters kubeconfig staging --rotate --output-file") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "staging.yaml")); !os.IsNotExist(err) {
		t.Fatal("wrote a file for a refused download")
	}
}

const sinaiaKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: sinaia
  cluster:
    server: https://10.0.0.1:6443
contexts:
- name: sinaia-user
  context:
    cluster: sinaia
    user: edka-user-0f8fad5b
current-context: sinaia-user
users:
- name: edka-user-0f8fad5b
  user:
    token: secret-token
`

const minikubeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: minikube
  cluster:
    server: https://127.0.0.1:8443
contexts:
- name: minikube
  context:
    cluster: minikube
    user: minikube
current-context: minikube
users:
- name: minikube
  user:
    token: local
`

// mergeFixtures serves a kubeconfig shaped like Edka's for sinaia.
func mergeFixtures() map[string]string {
	fixtures := map[string]string{"GET /api/clusters/c1/user-kubeconfig/download": sinaiaKubeconfig}
	for route, body := range clusterFixtures {
		if _, ok := fixtures[route]; !ok {
			fixtures[route] = body
		}
	}
	return fixtures
}

func countRequests(requests []string, request string) int {
	n := 0
	for _, r := range requests {
		if r == request {
			n++
		}
	}
	return n
}

func TestKubeconfigMergesIntoKubectlConfig(t *testing.T) {
	server, requests := fakeAPI(t, mergeFixtures())
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(minikubeKubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)

	for _, args := range [][]string{{"--merge", "--output-file", path}, {"--use"}} {
		if _, _, err := execute(t, server.URL, append([]string{"clusters", "kubeconfig", "sinaia"}, args...)...); err == nil || len(*requests) != 0 {
			t.Fatal(args, *requests, err)
		}
	}

	_, errOut, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia", "--merge", "--use")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"name: minikube", "token: local", "name: edka-sinaia", "token: secret-token", "current-context: edka-sinaia", "cluster-id: c1"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q in\n%s", want, data)
		}
	}
	for _, want := range []string{"✓ Merged your kubeconfig for sinaia into ", " as context edka-sinaia", "kubectl now uses edka-sinaia", ".edka-backup"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("missing %q in %q", want, errOut)
		}
	}
	if countRequests(*requests, "POST /api/clusters/c1/user-credentials/rotate") != 0 {
		t.Fatal("rotated without --rotate:", *requests)
	}
	if backup, _ := os.ReadFile(path + ".edka-backup"); string(backup) != minikubeKubeconfig {
		t.Fatal(string(backup))
	}
}

func TestKubeconfigMergeChecksTheFileBeforeTheDownload(t *testing.T) {
	server, requests := fakeAPI(t, mergeFixtures())
	path := filepath.Join(t.TempDir(), "config")
	foreign := strings.Replace(minikubeKubeconfig, "- name: minikube\n  context:", "- name: edka-sinaia\n  context:", 1)
	if err := os.WriteFile(path, []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)

	_, _, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia", "--merge")
	if err == nil || !strings.Contains(err.Error(), "wasn't written by edka") {
		t.Fatal(err)
	}
	if countRequests(*requests, "GET /api/clusters/c1/user-kubeconfig/download") != 0 {
		t.Fatal("spent the one-time kubeconfig on a file it can't merge into:", *requests)
	}
}

func TestKubeconfigRotateNeedsConfirmation(t *testing.T) {
	server, requests := fakeAPI(t, mergeFixtures())
	path := filepath.Join(t.TempDir(), "config")
	t.Setenv("KUBECONFIG", path)

	_, _, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia", "--rotate", "--merge")
	if err == nil || !strings.Contains(err.Error(), "--yes") || countRequests(*requests, "POST /api/clusters/c1/user-credentials/rotate") != 0 {
		t.Fatal(*requests, err)
	}

	*requests = nil
	_, errOut, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia", "--rotate", "--merge", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, r := range *requests {
		if strings.Contains(r, "user-credentials/rotate") || strings.Contains(r, "user-kubeconfig") {
			calls = append(calls, r)
		}
	}
	if strings.Join(calls, ", ") != "POST /api/clusters/c1/user-credentials/rotate, GET /api/clusters/c1/user-kubeconfig/download" {
		t.Fatal(calls)
	}
	if !strings.Contains(errOut, "✓ Rotated your credentials for sinaia") || !strings.Contains(errOut, "Next: kubectl --context edka-sinaia get nodes") {
		t.Fatal(errOut)
	}
}

func TestKubeconfigIsKeptWhenTheMergeFails(t *testing.T) {
	// clusterFixtures serves a kubeconfig without a context, which can't be merged.
	server, _ := fakeAPI(t, clusterFixtures)
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(minikubeKubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)

	_, _, err := execute(t, server.URL, "clusters", "kubeconfig", "sinaia", "--merge")
	if err == nil || !strings.Contains(err.Error(), "could not be merged") || !strings.Contains(err.Error(), "It was saved to ") {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(filepath.Join(dir, "edka-sinaia-*.yaml"))
	if len(kept) != 1 {
		t.Fatal(kept)
	}
	if data, _ := os.ReadFile(kept[0]); string(data) != "apiVersion: v1\nkind: Config\n" {
		t.Fatal(string(data))
	}
	if data, _ := os.ReadFile(path); string(data) != minikubeKubeconfig {
		t.Fatal("changed the kubeconfig on a failed merge")
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("left the lock behind")
	}
}

func TestClusterProtectionAndDelete(t *testing.T) {
	server, requests := fakeAPI(t, clusterFixtures)
	if _, _, err := execute(t, server.URL, "clusters", "protect", "staging"); err != nil || (*requests)[len(*requests)-1] != "POST /api/clusters/c2/protect" {
		t.Fatal(*requests, err)
	}
	*requests = nil
	if _, _, err := execute(t, server.URL, "clusters", "unprotect", "staging"); err == nil || strings.Contains(strings.Join(*requests, ","), "POST") {
		t.Fatal("removed protection without confirmation")
	}
	for _, args := range [][]string{{"clusters", "delete"}, {"clusters", "delete", "staging"}} {
		*requests = nil
		if _, _, err := execute(t, server.URL, args...); err == nil || strings.Contains(strings.Join(*requests, ","), "DELETE") {
			t.Fatalf("%v deleted without a name and confirmation", args)
		}
	}
	if _, _, err := execute(t, server.URL, "clusters", "delete", "staging", "--yes"); err != nil || (*requests)[len(*requests)-1] != "DELETE /api/clusters/c2" {
		t.Fatal(*requests, err)
	}
	if _, _, err := execute(t, server.URL, "clusters", "delete", "sinaia", "--yes"); err == nil || !strings.Contains(err.Error(), "edka clusters unprotect sinaia") {
		t.Fatal(err)
	}
}

func TestDatabasesAcrossClusters(t *testing.T) {
	server, requests := fakeAPI(t, clusterFixtures)
	out, _, err := execute(t, server.URL, "databases", "list")
	for _, want := range []string{"ENGINE", "orders", "postgresql 17", "20 GiB", "cache", "valkey 8", "staging"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s %v", want, out, err)
		}
	}
	out, _, err = execute(t, server.URL, "db", "get", "orders")
	for _, want := range []string{"2/3", "orders-1", "enabled", "Recovery window"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s %v", want, out, err)
		}
	}
	out, _, err = execute(t, server.URL, "databases", "backups", "orders")
	if err != nil || strings.Index(out, "orders-20260930") > strings.Index(out, "orders-20260929") || !strings.Contains(out, "3m30s") {
		t.Fatal(out, err)
	}
	if _, _, err := execute(t, server.URL, "databases", "backup", "orders"); err != nil || (*requests)[len(*requests)-1] != "POST /api/clusters/c1/databases/db1/backups" {
		t.Fatal(*requests, err)
	}
	out, _, err = execute(t, server.URL, "databases", "logs", "orders", "--tail", "5")
	if err != nil || out != "checkpoint complete\n" {
		t.Fatal(out, err)
	}
	*requests = nil
	if _, _, err := execute(t, server.URL, "databases", "delete", "orders"); err == nil || !strings.Contains(err.Error(), "Delete postgresql database orders in cluster sinaia") || strings.Contains(strings.Join(*requests, ","), "DELETE") {
		t.Fatal(*requests, err)
	}
	if _, _, err := execute(t, server.URL, "databases", "delete", "orders", "--force", "--yes"); err != nil || (*requests)[len(*requests)-1] != "DELETE /api/clusters/c1/databases/db1?force=true" {
		t.Fatal(*requests, err)
	}
}

func TestLinkShowsNamesAndOnlyKeepsDeploymentOnSameCluster(t *testing.T) {
	server, _ := fakeAPI(t, withFixtures(map[string]string{
		"GET /api/cli/whoami":              `{"data":{"email":"you@example.com","organization":{"id":"o1","name":"Acme"}}}`,
		"GET /api/clusters/c2/deployments": `{"data":[{"id":"d9","name":"web"}]}`,
	}))
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, ".edka.json")
	readLink := func() map[string]any {
		var link map[string]any
		// A link that is missing reads as nil.
		data, _ := os.ReadFile(path)
		_ = json.Unmarshal(data, &link)
		return link
	}
	_, errOut, err := execute(t, server.URL, "link", "--cluster", "sinaia")
	if err != nil || !strings.Contains(errOut, "Cluster: sinaia\n") || strings.Contains(errOut, "Deployment:") || readLink()["cluster"] != "c1" || readLink()["deployment"] != nil {
		t.Fatal(errOut, readLink(), err)
	}
	_, errOut, err = execute(t, server.URL, "link", "--cluster", "sinaia", "--deployment", "api")
	if err != nil || !strings.Contains(errOut, "Deployment: api\n") || readLink()["deployment"] != "d1" {
		t.Fatal(errOut, readLink(), err)
	}
	_, errOut, err = execute(t, server.URL, "link")
	if err != nil || !strings.Contains(errOut, "Cluster: sinaia\n  Deployment: api\n") || readLink()["deployment"] != "d1" {
		t.Fatalf("relinking the same cluster dropped its deployment: %s %v", errOut, err)
	}
	_, errOut, err = execute(t, server.URL, "link", "--cluster", "staging")
	if err != nil || !strings.Contains(errOut, "Cluster: staging\n") || readLink()["cluster"] != "c2" || readLink()["deployment"] != nil {
		t.Fatalf("switching clusters kept the old deployment: %s %v %v", errOut, readLink(), err)
	}
}

// TestRunHelperProcess stands in for the command `edka run` starts, on every
// platform: it prints KUBECONFIG, EDKA_CLUSTER and the kubeconfig's contents.
func TestRunHelperProcess(t *testing.T) {
	if os.Getenv("EDKA_RUN_HELPER") != "1" {
		return
	}
	path := os.Getenv("KUBECONFIG")
	data, err := os.ReadFile(path)
	fmt.Printf("%s\n%s\n%s", path, os.Getenv("EDKA_CLUSTER"), data)
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestRunPassesTemporaryKubeconfig(t *testing.T) {
	server, requests := fakeAPI(t, withFixtures(map[string]string{
		"POST /api/clusters/c1/user-kubeconfig/temporary": `{"data":{"kubeconfig":"apiVersion: v1\nkind: Config\n","expires_at":"2026-09-30T13:00:00.000Z"}}`,
	}))
	t.Setenv("EDKA_RUN_HELPER", "1")
	out, errOut, err := execute(t, server.URL, "run", "--cluster", "sinaia", "--kubeconfig", "--", os.Args[0], "-test.run=^TestRunHelperProcess$")
	if err != nil {
		t.Fatal(err, errOut)
	}
	lines := strings.SplitN(out, "\n", 3)
	if len(lines) != 3 || lines[1] != "c1" || lines[2] != "apiVersion: v1\nkind: Config\n" {
		t.Fatalf("the command saw %q", out)
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("%s outlived the command: %v", lines[0], err)
	}
	if !strings.Contains(errOut, "✓ Temporary kubeconfig for sinaia, valid until ") || strings.Contains(out, "Temporary") {
		t.Fatalf("stdout %q, stderr %q", out, errOut)
	}
	if last := (*requests)[len(*requests)-1]; last != "POST /api/clusters/c1/user-kubeconfig/temporary" {
		t.Fatal(*requests)
	}
}

func TestRunKubeconfigSuggestsRotationForOutdatedCredentials(t *testing.T) {
	server, _ := fakeAPI(t, withFixtures(map[string]string{
		"POST /api/clusters/c1/user-kubeconfig/temporary": `409 {"error":"Credentials out of date","message":"Your cluster credentials were issued for a different access level. Rotate them to get a kubeconfig."}`,
	}))
	t.Setenv("EDKA_RUN_HELPER", "1")
	out, _, err := execute(t, server.URL, "run", "--cluster", "sinaia", "--kubeconfig", "--", os.Args[0], "-test.run=^TestRunHelperProcess$")
	if err == nil || !strings.Contains(err.Error(), "edka api clusters user-credentials rotate-own --cluster sinaia") || out != "" {
		t.Fatalf("out %q, err %v", out, err)
	}
}
