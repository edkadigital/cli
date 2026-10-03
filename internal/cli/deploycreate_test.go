package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// silentAPI fails the test when the CLI sends a request.
func silentAPI(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("sent %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDeploymentsCreateDescribesItsBodyWithoutARequest(t *testing.T) {
	server := silentAPI(t)
	var example map[string]any
	out, _, err := execute(t, server.URL, "deployments", "create", "--example")
	if err != nil || json.Unmarshal([]byte(out), &example) != nil || example["image_repository"] == nil || example["config"] == nil {
		t.Fatal(out, err)
	}
	// The fields keep the order Edka declares them in, with the name first.
	if !strings.HasPrefix(out, "{\n  \"name\": ") {
		t.Fatalf("the example does not start with the name:\n%s", out)
	}
	out, _, err = execute(t, server.URL, "deployments", "create", "--git", "--example")
	if err != nil || json.Unmarshal([]byte(out), &example) != nil || example["github_repository_id"] == nil {
		t.Fatal(out, err)
	}
	var schema struct {
		Properties map[string]struct {
			Properties map[string]any `json:"properties"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	out, _, err = execute(t, server.URL, "deployments", "create", "--schema")
	if err != nil || json.Unmarshal([]byte(out), &schema) != nil {
		t.Fatal(out, err)
	}
	// The schema describes what goes in config, and no field the route sets itself.
	if schema.Properties["config"].Properties["env_variables"] == nil || strings.Join(schema.Required, ",") != "name,namespace" {
		t.Fatalf("config or the required fields are not described:\n%s", out)
	}
	if _, set := schema.Properties["cluster_id"]; set {
		t.Fatal("the schema asks for the cluster the path names")
	}
	if _, _, err = execute(t, server.URL, "deployments", "create", "--schema", "--example"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatal(err)
	}
	_, _, err = execute(t, server.URL, "deployments", "create", "--git")
	if err == nil || !strings.Contains(err.Error(), "`edka deployments create --git --example`") {
		t.Fatal(err)
	}
	if _, _, err = execute(t, server.URL, "deployments", "create", "--deployment", "api", "--field", "name=web"); err == nil || !strings.Contains(err.Error(), "the body names the new one") {
		t.Fatal(err)
	}
}

func TestEndpointSchemaPrintsTheBodyWithoutARequest(t *testing.T) {
	server := silentAPI(t)
	for _, command := range [][]string{{"api", "clusters", "deployments", "create"}, {"api", "clusters", "deployments", "git", "create"}, {"api", "deployments", "settings", "update"}} {
		var schema map[string]any
		out, _, err := execute(t, server.URL, append(command, "--schema")...)
		if err != nil || json.Unmarshal([]byte(out), &schema) != nil || schema["properties"] == nil || schema["examples"] == nil {
			t.Fatal(command, out, err)
		}
		help, _, err := execute(t, server.URL, append(command, "--help")...)
		if err != nil || !strings.Contains(help, "--schema prints the JSON Schema of the request body") {
			t.Fatal(command, help, err)
		}
	}
	// A route Edka has not described takes no --schema.
	if _, _, err := execute(t, server.URL, "api", "clusters", "create", "--schema"); err == nil || !strings.Contains(err.Error(), "unknown flag: --schema") {
		t.Fatal(err)
	}
}

func TestDeploymentsCreateDryRunPrintsTheRequest(t *testing.T) {
	server, requests := fakeAPI(t, map[string]string{"GET /api/clusters": `{"data":[{"id":"c1","name":"sinaia"}]}`})
	for _, tc := range []struct {
		git  bool
		path string
	}{{false, "/api/clusters/c1/deployments"}, {true, "/api/clusters/c1/deployments/git"}} {
		args := []string{"deployments", "create", "--cluster", "sinaia", "--data", `{"name":"web","namespace":"default"}`, "--field", "replicas:=2", "--dry-run"}
		if tc.git {
			args = append(args, "--git")
		}
		out, _, err := execute(t, server.URL, args...)
		var request struct {
			Method, Path string
			Body         map[string]any
		}
		if err != nil || json.Unmarshal([]byte(out), &request) != nil || request.Method != "POST" || request.Path != tc.path || request.Body["name"] != "web" || request.Body["replicas"] != float64(2) {
			t.Fatal(out, err)
		}
	}
	for _, request := range *requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("a dry run sent %s", request)
		}
	}
}

func TestDeploymentsCreateLinksAndWaits(t *testing.T) {
	fastPolls(t)
	sent := ""
	routes := map[string][]string{
		"GET /api/clusters":   {`{"data":[{"id":"c1","name":"sinaia"}]}`},
		"GET /api/cli/whoami": {`{"data":{"email":"you@example.com","organization":{"id":"o1","name":"Acme"}}}`},
		"GET /api/deployments/d1": {
			`{"data":{"id":"d1","spec_generation":1,"applied_generation":0,"healthy_generation":0,"status":"deploying"}}`,
			`{"data":{"id":"d1","spec_generation":1,"applied_generation":1,"healthy_generation":1,"status":"deployed"}}`,
		},
		"GET /api/deployments/d1/status": {`{"data":{"name":"web","status":"deployed","replicas":{"desired":1,"updated":1,"ready":1}}}`},
	}
	replies, requests := sequenceAPI(t, routes)
	// The create route is answered here, so the body it receives is kept.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/clusters/c1/deployments" {
			body, _ := io.ReadAll(r.Body)
			sent = string(body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"data":{"id":"d1","name":"web","spec_generation":1,"status":"deploying"}}`)
			return
		}
		replies.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	t.Chdir(dir)
	out, errOut, err := execute(t, server.URL, "deployments", "create", "--cluster", "sinaia", "--data", `{"name":"web","namespace":"default","image_repository":"nginx"}`, "--field", "image_tag=1.29", "--link", "--wait", "--json")
	if err != nil {
		t.Fatal(err, errOut)
	}
	var body map[string]any
	if json.Unmarshal([]byte(sent), &body) != nil || body["name"] != "web" || body["image_tag"] != "1.29" {
		t.Fatalf("sent %q", sent)
	}
	inOrder(t, errOut,
		"✓ Created deployment web in cluster sinaia\n",
		"✓ Linked this directory\n  Cluster: sinaia\n  Deployment: web\n",
		"Waiting for generation 1…\n",
		"✓ web is running generation 1\n",
	)
	var link map[string]any
	data, _ := os.ReadFile(filepath.Join(dir, ".edka.json"))
	if json.Unmarshal(data, &link) != nil || link["cluster"] != "c1" || link["deployment"] != "d1" || link["organization"] != "o1" {
		t.Fatalf("link: %s", data)
	}
	var status map[string]any
	if json.Unmarshal([]byte(out), &status) != nil || status["data"].(map[string]any)["status"] != "deployed" {
		t.Fatalf("stdout is not the runtime status: %q", out)
	}
	// The link is written before the wait reads the deployment.
	inOrder(t, strings.Join(*requests, "\n"), "GET /api/cli/whoami", "GET /api/deployments/d1")
}

// `edka up --data` changes the linked deployment. `create` makes a new one in
// the linked cluster.
func TestDeploymentsCreateLeavesTheLinkedDeploymentAlone(t *testing.T) {
	server, requests := fakeAPI(t, map[string]string{
		"GET /api/clusters":                 `{"data":[{"id":"c1","name":"sinaia"}]}`,
		"POST /api/clusters/c1/deployments": `{"data":{"id":"d2","name":"web","spec_generation":1}}`,
	})
	dir := t.TempDir()
	t.Chdir(dir)
	link := fmt.Sprintf(`{"version":1,"profile":"default","api_url":%q,"organization":"o1","cluster":"c1","deployment":"d1"}`, server.URL)
	if err := os.WriteFile(filepath.Join(dir, ".edka.json"), []byte(link), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := execute(t, server.URL, "deployments", "create", "--field", "name=web", "--field", "namespace=default", "--json")
	if err != nil || !strings.Contains(out, `"d2"`) || !strings.Contains(errOut, "✓ Created deployment web in cluster sinaia\n  Next: edka deployments status web\n") {
		t.Fatal(out, errOut, err)
	}
	if got := strings.Join(*requests, ","); got != "GET /api/clusters,POST /api/clusters/c1/deployments" {
		t.Fatal(got)
	}
	// Without --link the directory keeps its link.
	if data, _ := os.ReadFile(filepath.Join(dir, ".edka.json")); string(data) != link {
		t.Fatalf("the link changed: %s", data)
	}
}

func TestDeploymentsCreateGitFollowsTheFirstBuild(t *testing.T) {
	fastPolls(t)
	routes := buildRoutes(true)
	routes["GET /api/clusters"] = []string{`{"data":[{"id":"c1","name":"sinaia"}]}`}
	routes["POST /api/clusters/c1/deployments/git"] = []string{`{"data":{"id":"d1","name":"api","spec_generation":4,"initial_build":{"id":"b1","status":"queued"}}}`}
	server, requests := sequenceAPI(t, routes)
	_, errOut, err := execute(t, server.URL, "deployments", "create", "--git", "--cluster", "sinaia", "--data", `{"name":"api","github_repository_id":"42","image_repository_name":"acme/api"}`, "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut,
		"✓ Created deployment api in cluster sinaia\n",
		"Waiting for its first build…\n",
		"✓ Built abc1234 in 2m14s\n",
		"Auto-deploy started generation 5…\n",
		"✓ api is running generation 5\n",
	)
	for _, request := range *requests {
		if strings.HasPrefix(request, "POST ") && request != "POST /api/clusters/c1/deployments/git" {
			t.Fatalf("started a second build or rollout: %s", request)
		}
	}
	// Edka creates the deployment even when its first build does not start.
	routes["POST /api/clusters/c1/deployments/git"] = []string{`{"data":{"id":"d1","name":"api","spec_generation":4,"initial_build":null}}`}
	server, _ = sequenceAPI(t, routes)
	_, _, err = execute(t, server.URL, "deployments", "create", "--git", "--cluster", "sinaia", "--field", "name=api", "--wait")
	if err == nil || !strings.Contains(err.Error(), "its first build did not start; start one with `edka build api --wait`") {
		t.Fatal(err)
	}
}
