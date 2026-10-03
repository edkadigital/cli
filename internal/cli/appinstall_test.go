package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const whiteboardCatalog = `{"data":[{"id":"cat-wb","name":"Whiteboard","slug":"whiteboard","category":"web","configuration_tabs":["general","database","access"],"inputs_schema":{
	"access":[
		{"name":"access_enabled","config":{"type":"boolean","label":"Expose","default":false}},
		{"name":"ingress_class","config":{"type":"dynamic-select","label":"Traffic Class","data_source":"cluster-ingress-classes","required":true,"show_if":"access_enabled"}},
		{"name":"gateway_name","config":{"type":"hidden","label":"Gateway","default":""}},
		{"name":"webhook_class","config":{"type":"dynamic-select","label":"Webhook class","data_source":"cluster-gateway-classes"}}],
	"placement":[
		{"name":"namespace","config":{"type":"dynamic-select","label":"Namespace","data_source":"cluster-namespaces","default":"whiteboard"}},
		{"name":"node_pool_name","config":{"type":"dynamic-select","label":"Node pool","data_source":"cluster-node-pools"}}],
	"general":[
		{"name":"replicas","config":{"type":"number","label":"Replicas","default":1,"description":"Pods to run"}},
		{"name":"size","config":{"type":"select","label":"Size","default":"small","options":[{"label":"Small","value":"small"},{"label":"Large","value":"large"}]}},
		{"name":"app_secret","config":{"type":"password","label":"App secret","generate":true,"required":true}},
		{"name":"api_key","config":{"type":"password","label":"API key","required":true}}],
	"database":[
		{"name":"postgres_instance","config":{"type":"dynamic-select","label":"PostgreSQL","data_source":"cluster-postgresql-instances","required":true}},
		{"name":"postgres_database","config":{"type":"dynamic-select","label":"Database","data_source":"cluster-postgresql-databases","depends_on":"postgres_instance","required":true}}]
}}]}`

// installAPI serves one cluster, the catalog above, its option lists and an
// install route that answers with installs in turn. It records every request,
// and each install body as JSON with sorted keys.
func installAPI(t *testing.T, installs []string, apps []string) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	requests, bodies := []string{}, []string{}
	installCalls, appCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			request += "?" + r.URL.RawQuery
		}
		requests = append(requests, request)
		switch request {
		case "GET /api/clusters":
			fmt.Fprint(w, `{"data":[{"id":"c1","name":"sinaia"}]}`)
		case "GET /api/clusters/c1/apps/catalog":
			fmt.Fprint(w, whiteboardCatalog)
		case "GET /api/clusters/c1/apps/data-sources/cluster-postgresql-instances":
			fmt.Fprint(w, `{"success":true,"data":[{"value":"pg-1","label":"orders (orders.postgres)"},{"value":"pg-2","label":"analytics (analytics.postgres)"}]}`)
		case "GET /api/clusters/c1/apps/data-sources/cluster-postgresql-databases?postgres_instance=pg-1":
			fmt.Fprint(w, `{"success":true,"data":[{"value":"app","label":"app (initial database)"}]}`)
		case "GET /api/clusters/c1/ingress-classes":
			fmt.Fprint(w, `{"success":true,"data":[{"name":"eg","isDefault":true,"controller_type":"gateway-api"},{"name":"eg-ts","isDefault":false,"controller_type":"gateway-api"},{"name":"tailscale","controller_type":"ingress"}]}`)
		case "GET /api/clusters/c1/namespaces":
			fmt.Fprint(w, `{"namespaces":[{"name":"default","status":"Active"},{"name":"whiteboard","status":"Active"}]}`)
		case "GET /api/clusters/c1/nodepools":
			fmt.Fprint(w, `{"data":[{"name":"workers"},{"name":"metal"}]}`)
		case "POST /api/clusters/c1/apps/cat-wb/instances":
			data, _ := io.ReadAll(r.Body)
			var body any
			if err := json.Unmarshal(data, &body); err != nil {
				t.Errorf("install body is not JSON: %s", data)
			}
			sorted, _ := json.Marshal(body)
			bodies = append(bodies, string(sorted))
			answer := installs[min(installCalls, len(installs)-1)]
			installCalls++
			var code int
			if n, _ := fmt.Sscanf(answer, "%d ", &code); n == 1 {
				w.WriteHeader(code)
				answer = answer[4:]
			}
			fmt.Fprint(w, answer)
		case "GET /api/clusters/c1/apps/app-1":
			answer := apps[min(appCalls, len(apps)-1)]
			appCalls++
			var code int
			if n, _ := fmt.Sscanf(answer, "%d ", &code); n == 1 {
				w.WriteHeader(code)
				answer = answer[4:]
			}
			fmt.Fprint(w, answer)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests, &bodies
}

const installStarted = `202 {"success":true,"message":"App installation initiated","app_id":"app-1","instance_slug":"whiteboard","job_id":"app-install-app-1"}`

func TestAppsCatalogListsAnAppsSettings(t *testing.T) {
	server, _, _ := installAPI(t, []string{installStarted}, nil)
	out, _, err := execute(t, server.URL, "apps", "catalog", "whiteboard", "--cluster", "sinaia")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, out, "SETTING", "DEFAULT", "REQUIRED", "replicas", "number", "1", "Pods to run", "app_secret", "generated", "yes", "postgres_instance", "yes", "access_enabled", "false", "ingress_class", "if access_enabled")
	if strings.Contains(out, "gateway_name") {
		t.Fatalf("hidden setting listed:\n%s", out)
	}
	out, _, err = execute(t, server.URL, "apps", "catalog", "whiteboard", "--cluster", "sinaia", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(out), &list) != nil || len(list.Data) != 11 || list.Data[1]["options"] == nil {
		t.Fatal(out)
	}
}

func TestAppsInstallSendsOnlyTheChosenSettings(t *testing.T) {
	server, _, bodies := installAPI(t, []string{installStarted}, nil)
	out, errOut, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia")
	if err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 1 || (*bodies)[0] != `{"configuration":{}}` {
		t.Fatal(*bodies)
	}
	if out != "" || errOut != "✓ Installing Whiteboard in cluster sinaia as whiteboard.\n  Next: edka apps get whiteboard\n" {
		t.Fatalf("%q %q", out, errOut)
	}
	out, _, err = execute(t, server.URL, "apps", "install", "Whiteboard", "--cluster", "sinaia", "--name", "Team board", "--json")
	if err != nil || !strings.Contains(out, `"app_id": "app-1"`) {
		t.Fatal(out, err)
	}
	if last := (*bodies)[len(*bodies)-1]; last != `{"configuration":{},"displayName":"Team board"}` {
		t.Fatal(last)
	}
}

func TestAppsInstallTypesSettingsAndResolvesOptionNames(t *testing.T) {
	server, requests, bodies := installAPI(t, []string{installStarted}, nil)
	_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia",
		"--set", "replicas=3", "--set", "access_enabled=true", "--set", "ingress_class=eg-ts",
		"--set", "postgres_database=app", "--set", "postgres_instance=orders", "--set", "size=Large",
		"--set", `labels:={"team":"web"}`)
	if err == nil || !strings.Contains(err.Error(), `no setting "labels"`) || !strings.Contains(err.Error(), "edka apps catalog whiteboard") {
		t.Fatal(err)
	}
	_, _, err = execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia",
		"--set", "replicas=3", "--set", "access_enabled=true", "--set", "ingress_class=eg-ts",
		"--set", "postgres_database=app", "--set", "postgres_instance=orders", "--set", "size=Large",
		"--set", `gateway_name:="eg"`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"configuration":{"access_enabled":true,"gateway_name":"eg","ingress_class":"eg-ts","postgres_database":"app","postgres_instance":"pg-1","replicas":3,"size":"large"}}`
	if len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
	// The database list depends on the instance, so it is read after the instance resolves.
	inOrder(t, strings.Join(*requests, "\n"), "data-sources/cluster-postgresql-instances", "data-sources/cluster-postgresql-databases?postgres_instance=pg-1")
}

// The last value for a setting wins, and a plain value may contain ":=".
func TestAppsInstallReadsEachSetInTurn(t *testing.T) {
	server, _, bodies := installAPI(t, []string{installStarted}, nil)
	if _, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--set", "size=Small", "--set", `size:="large"`, "--set", "gateway_name=eg:=1"); err != nil {
		t.Fatal(err)
	}
	if want := `{"configuration":{"gateway_name":"eg:=1","size":"large"}}`; len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
}

func TestAppsInstallStopsWhenOptionsCannotBeListed(t *testing.T) {
	server, _, bodies := installAPI(t, []string{installStarted}, nil)
	// The fake API has no databases for the analytics instance.
	_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--set", "postgres_instance=analytics", "--set", "postgres_database=app")
	if err == nil || !strings.Contains(err.Error(), "cannot list the options for postgres_database") || len(*bodies) != 0 {
		t.Fatal(err, *bodies)
	}
}

func TestAppsOptionsUsesTheDefaultOfTheSettingItDependsOn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			request += "?" + r.URL.RawQuery
		}
		switch request {
		case "GET /api/clusters":
			fmt.Fprint(w, `{"data":[{"id":"c1","name":"sinaia"}]}`)
		case "GET /api/clusters/c1/apps/catalog":
			fmt.Fprint(w, `{"data":[{"id":"cat-mb","name":"Metabase","slug":"metabase","inputs_schema":{"general":[
				{"name":"postgres_instance","config":{"type":"dynamic-select","data_source":"cluster-postgresql-instances","default":"pg-1"}},
				{"name":"postgres_database","config":{"type":"dynamic-select","data_source":"cluster-postgresql-databases","depends_on":"postgres_instance"}}]}}]}`)
		case "GET /api/clusters/c1/apps/data-sources/cluster-postgresql-databases?postgres_instance=pg-1":
			fmt.Fprint(w, `{"data":[{"value":"app","label":"app (initial database)"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	out, _, err := execute(t, server.URL, "apps", "options", "metabase", "postgres_database", "--cluster", "sinaia")
	if err != nil || !strings.Contains(out, "app (initial database)") {
		t.Fatal(out, err)
	}
}

func TestAppsInstallNamesSettingsTheAppDoesNotHave(t *testing.T) {
	rejected := `400 {"error":"Invalid configuration","message":"Invalid app configuration: …","fields":[{"field":"labels","message":"Unknown field"},{"field":"replicas","message":"Number must be greater than or equal to 1"}]}`
	server, _, _ := installAPI(t, []string{rejected}, nil)
	_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--data", `{"labels":{},"replicas":0}`)
	want := "Whiteboard cannot install with these settings:\n  labels: Unknown field\n  replicas: Number must be greater than or equal to 1. Pods to run\n" +
		"Remove labels from --data; list the app's settings with `edka apps catalog whiteboard`.\nSet the others with --set key=value."
	if err == nil || err.Error() != want {
		t.Fatalf("%q", err)
	}
}

func TestAppsInstallRejectsBadSettingsBeforeInstalling(t *testing.T) {
	for _, tc := range []struct {
		set, want string
	}{
		{"api_key=sk-live-123", "api_key is a secret"},
		{"replicas=many", `replicas takes a number, not "many"`},
		{"replicas=NaN", `replicas takes a number, not "NaN"`},
		{"replicas=Inf", `replicas takes a number, not "Inf"`},
		{"replicas=-Infinity", `replicas takes a number, not "-Infinity"`},
		{"replicas=1e400", `replicas takes a number, not "1e400"`},
		{"access_enabled=maybe", "access_enabled takes true or false"},
		{"postgres_instance=billing", `postgres_instance has no option "billing"; choose one of: orders (orders.postgres), analytics (analytics.postgres)`},
		{"size=huge", `size has no option "huge"; choose one of: Small, Large`},
		{"replicas", "set a setting as key=value"},
	} {
		server, _, bodies := installAPI(t, []string{installStarted}, nil)
		_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--set", tc.set)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "sk-live-123") || len(*bodies) != 0 {
			t.Errorf("%s: %v, %d installs", tc.set, err, len(*bodies))
		}
	}
}

func TestAppsInstallNamesMissingSettingsInScripts(t *testing.T) {
	missing := `400 {"error":"Invalid configuration","message":"Invalid app configuration: …","fields":[{"field":"postgres_instance","message":"Required"},{"field":"api_key","message":"Required"}]}`
	server, _, bodies := installAPI(t, []string{missing}, nil)
	_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia")
	if err == nil {
		t.Fatal("install succeeded")
	}
	want := "Whiteboard needs these settings:\n  postgres_instance: Required. PostgreSQL. Options: orders (orders.postgres), analytics (analytics.postgres)\n  api_key: Required. API key\nSet them with --set key=value, and secrets in a file for --data @file."
	if err.Error() != want || len(*bodies) != 1 {
		t.Fatalf("%q after %d installs", err, len(*bodies))
	}
}

func TestAppsInstallReadsSettingsFromAFile(t *testing.T) {
	server, _, bodies := installAPI(t, []string{installStarted}, nil)
	file := filepath.Join(t.TempDir(), "whiteboard.json")
	if err := os.WriteFile(file, []byte(`{"api_key":"from-file","replicas":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--data", "@"+file, "--set", "replicas=4"); err != nil {
		t.Fatal(err)
	}
	if want := `{"configuration":{"api_key":"from-file","replicas":4}}`; len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
}

func TestAppsInstallWaitFollowsTheInstallation(t *testing.T) {
	fastPolls(t)
	server, _, _ := installAPI(t, []string{installStarted}, []string{
		`{"data":{"id":"app-1","status":"installing_dependencies","progress":{"message":"Installing addon: cert-manager"}}}`,
		`{"data":{"id":"app-1","status":"configuring","progress":{"message":"Preparing app configuration"}}}`,
		`{"data":{"id":"app-1","status":"deploying","progress":{"message":"Preparing app configuration"}}}`,
		`{"data":{"id":"app-1","app_name":"Whiteboard","instance_name":"whiteboard","status":"installed","version":"1.0.0","namespace":"whiteboard","release_name":"whiteboard"}}`,
	})
	out, errOut, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut,
		"Installing Whiteboard in cluster sinaia as whiteboard…\n",
		"Installing dependencies…\n",
		"Installing addon: cert-manager\n",
		"Preparing the configuration…\n",
		"Preparing app configuration\n",
		"Deploying…\n",
		"✓ whiteboard is installed\n",
	)
	if strings.Count(errOut, "Preparing app configuration") != 1 {
		t.Fatalf("repeated lines:\n%s", errOut)
	}
	inOrder(t, out, "Status", "installed", "Cluster", "sinaia", "Namespace", "whiteboard", "ID", "app-1")
}

func TestAppsInstallWaitReadsAgainAfterAReadThatFailed(t *testing.T) {
	fastPolls(t)
	server, _, _ := installAPI(t, []string{installStarted}, []string{
		`{"data":{"id":"app-1","status":"deploying","progress":{"message":"Applying manifests"}}}`,
		`502 {"error":"Bad Gateway"}`,
		`{"data":{"id":"app-1","app_name":"Whiteboard","instance_name":"whiteboard","status":"installed","version":"1.0.0","namespace":"whiteboard","release_name":"whiteboard"}}`,
	})
	_, errOut, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut, "Deploying…\n", "Could not read from Edka, trying again", "✓ whiteboard is installed\n")
}

func TestAppsInstallWaitEndsDuringARequestWithTheHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/clusters":
			fmt.Fprint(w, `{"data":[{"id":"c1","name":"sinaia"}]}`)
		case "GET /api/clusters/c1/apps/catalog":
			fmt.Fprint(w, whiteboardCatalog)
		case "POST /api/clusters/c1/apps/cat-wb/instances":
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, strings.TrimPrefix(installStarted, "202 "))
		case "GET /api/clusters/c1/apps/app-1":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
	}))
	t.Cleanup(server.Close)
	_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--wait", "--wait-timeout", "100ms")
	if err == nil || !strings.Contains(err.Error(), "whiteboard has not finished") || !strings.Contains(err.Error(), "follow it with `edka apps get whiteboard`") {
		t.Fatal(err)
	}
}

func TestAppsInstallWaitReportsAFailure(t *testing.T) {
	fastPolls(t)
	server, _, _ := installAPI(t, []string{installStarted}, []string{
		`{"data":{"id":"app-1","status":"failed","progress":{"message":"Helm release failed: timed out waiting for the condition"}}}`,
	})
	_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--wait")
	want := "whiteboard failed: Helm release failed: timed out waiting for the condition\nFind the cause with `edka apps diagnose whiteboard`"
	if err == nil || err.Error() != want {
		t.Fatal(err)
	}
}

func TestAppsInstallNamesAnUnknownApp(t *testing.T) {
	server, _, bodies := installAPI(t, []string{installStarted}, nil)
	_, _, err := execute(t, server.URL, "apps", "install", "blackboard", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), `catalog app "blackboard" was not found`) || !strings.Contains(err.Error(), "edka apps catalog") || len(*bodies) != 0 {
		t.Fatal(err)
	}
}

func TestAppsInstallErrorsListOptionsForScripts(t *testing.T) {
	missing := `400 {"error":"Invalid configuration","message":"Invalid app configuration: …","fields":[{"field":"postgres_instance","message":"Required"},{"field":"postgres_database","message":"Required"},{"field":"api_key","message":"Required"}]}`
	server, _, _ := installAPI(t, []string{missing}, nil)
	_, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--json")
	var settings *FieldsError
	if !errors.As(err, &settings) || len(settings.Fields) != 3 {
		t.Fatal(err)
	}
	got, _ := json.Marshal(settings.Fields)
	want := `[{"field":"postgres_instance","message":"Required","label":"PostgreSQL","type":"dynamic-select","options":[{"value":"pg-1","label":"orders (orders.postgres)"},{"value":"pg-2","label":"analytics (analytics.postgres)"}]},` +
		`{"field":"postgres_database","message":"Required","label":"Database","type":"dynamic-select","depends_on":"postgres_instance"},` +
		`{"field":"api_key","message":"Required","label":"API key","type":"password","secret":true}]`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if !strings.Contains(err.Error(), "postgres_database: Required. Database. Choose postgres_instance first") {
		t.Fatal(err)
	}
}

func TestAppsOptionsListsTheChoicesForASetting(t *testing.T) {
	server, requests, bodies := installAPI(t, []string{installStarted}, nil)
	options := func(args ...string) (string, string, error) {
		return execute(t, server.URL, append([]string{"apps", "options", "whiteboard"}, append(args, "--cluster", "sinaia")...)...)
	}
	out, _, err := options("postgres_instance")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, out, "VALUE", "LABEL", "pg-1", "orders (orders.postgres)", "pg-2", "analytics (analytics.postgres)")
	out, _, err = options("postgres_database", "--set", "postgres_instance=orders", "--json")
	if err != nil || strings.Join(strings.Fields(out), "") != `{"data":[{"label":"app(initialdatabase)","value":"app"}],"open":false}` {
		t.Fatal(out, err)
	}
	if !strings.Contains(strings.Join(*requests, "\n"), "cluster-postgresql-databases?postgres_instance=pg-1") {
		t.Fatal(*requests)
	}
	if _, _, err := options("postgres_database"); err == nil || err.Error() != "postgres_database depends on postgres_instance; pass --set postgres_instance=<name>" {
		t.Fatal(err)
	}
	if _, _, err := options("replicas"); err == nil || err.Error() != "replicas takes a number value, not a choice from a list" {
		t.Fatal(err)
	}
	if out, _, err = options("size"); err != nil || !strings.Contains(out, "large") {
		t.Fatal(out, err)
	}
	if out, _, err = options("webhook_class"); err != nil || !strings.Contains(out, "eg (default)") || strings.Contains(out, "tailscale") {
		t.Fatalf("gateway classes include an ingress class: %s %v", out, err)
	}
	if out, _, err = options("node_pool_name"); err != nil {
		t.Fatal(err)
	}
	inOrder(t, out, "Default placement", "workers", "metal")
	out, errOut, err := options("namespace", "--json")
	if err != nil || !strings.Contains(out, `"open": true`) || errOut != "" {
		t.Fatal(out, errOut, err)
	}
	if _, errOut, _ = options("namespace"); !strings.Contains(errOut, "a new name works too") {
		t.Fatal(errOut)
	}
	if len(*bodies) != 0 {
		t.Fatal(*bodies)
	}
}

func TestAppsInstallKeepsANewNamespace(t *testing.T) {
	server, _, bodies := installAPI(t, []string{installStarted}, nil)
	if _, _, err := execute(t, server.URL, "apps", "install", "whiteboard", "--cluster", "sinaia", "--set", "namespace=boards", "--set", "node_pool_name=metal"); err != nil {
		t.Fatal(err)
	}
	if want := `{"configuration":{"namespace":"boards","node_pool_name":"metal"}}`; len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
}

func TestEnvSet(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "false": false, "FALSE": false, "1": true, "true": true, "yes": true} {
		t.Setenv("EDKA_TEST_FLAG", value)
		if got := envSet("EDKA_TEST_FLAG"); got != want {
			t.Errorf("%q: got %v", value, got)
		}
	}
}
