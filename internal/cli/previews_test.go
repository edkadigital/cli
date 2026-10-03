package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

var previewFixtures = map[string]string{
	"GET /api/deployments":                       `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia","github_deployment_id":"g1"},{"id":"d2","name":"worker","cluster_name":"sinaia","github_deployment_id":null},{"id":"d3","name":"cron","cluster_name":"sinaia"}]}`,
	"GET /api/deployments/d1/previews":           `{"data":[{"id":"p1","pr_number":12,"pr_title":"Fix login","pr_author":"ana","head_ref":"fix/login","base_ref":"main","head_sha":"abc1234def","hostname":"pr-12.api.acme.dev","preview_url":"https://pr-12.api.acme.dev","namespace":"preview","status":"active","expires_at":"2026-10-09T10:00:00Z"},{"id":"p2","pr_number":15,"pr_title":"Add export","head_ref":"export","hostname":"","preview_url":null,"status":"failed","error_message":"Build failed: Dockerfile not found"},{"id":"p3","pr_number":16,"pr_title":"Odd address","preview_url":"file:///etc/passwd","status":"active"}]}`,
	"GET /api/deployments/d1/previews/12/status": `{"data":{"pr_number":12,"name":"api-pr-12","running_image":"registry.edka.dev/api:abc1234","replicas":{"desired":1,"ready":1,"available":1,"updated":1},"pods":[{"name":"api-pr-12-7d9","ready":true,"restartCount":0,"status":"Running"}]}}`,
	"GET /api/deployments/d1/previews/15/status": `404 {"error":"Preview deployment not found in cluster"}`,
	"GET /api/deployments/d1/previews/12/logs":   `{"logs":"ready on :3000\n","podName":"api-pr-12-7d9"}`,
	"DELETE /api/deployments/d1/previews/12":     `{"message":"Preview deletion queued"}`,
	"GET /api/deployments/d3/previews":           `404 {"error":"GitHub deployment not found"}`,
}

func TestPreviewsList(t *testing.T) {
	server, requests := fakeAPI(t, previewFixtures)
	out, _, err := execute(t, server.URL, "previews", "list", "api")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PR", "#12", "active", "Fix login", "fix/login", "ana", "https://pr-12.api.acme.dev", "#15", "failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	out, _, err = execute(t, server.URL, "preview", "list", "--deployment", "api", "--deleted", "--json")
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err != nil || json.Unmarshal([]byte(out), &list) != nil || len(list.Data) != 3 || (*requests)[len(*requests)-1] != "GET /api/deployments/d1/previews?includeDeleted=true" {
		t.Fatal(out, *requests, err)
	}
	// An image deployment has no previews, and the command says why: from the
	// list of deployments when it names the source, else from Edka's answer.
	for _, name := range []string{"worker", "cron"} {
		_, _, err = execute(t, server.URL, "previews", "list", name)
		if err == nil || !strings.Contains(err.Error(), name+" is an image deployment") {
			t.Fatal(name, err)
		}
	}
	if _, _, err = execute(t, server.URL, "previews", "status", "12", "--deployment", "cron"); err == nil || !strings.Contains(err.Error(), "cron is an image deployment") {
		t.Fatal(err)
	}
}

func TestPreviewsStatusShowsTheRecordAndWhatRuns(t *testing.T) {
	server, _ := fakeAPI(t, previewFixtures)
	for _, pr := range []string{"12", "#12"} {
		out, errOut, err := execute(t, server.URL, "previews", "status", pr, "--deployment", "api")
		if err != nil || errOut != "" {
			t.Fatal(out, errOut, err)
		}
		inOrder(t, out, "Preview", "#12 Fix login", "Status", "active", "URL", "https://pr-12.api.acme.dev", "Branch", "fix/login → main", "Commit", "abc1234\n", "Image", "registry.edka.dev/api:abc1234", "Replicas", "1/1 ready", "POD", "api-pr-12-7d9")
	}
	// A preview that failed to build runs nothing; its record says why.
	out, errOut, err := execute(t, server.URL, "previews", "status", "15", "--deployment", "api")
	if err != nil || !strings.Contains(errOut, "Could not read what runs for preview #15: Preview deployment not found in cluster") {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, out, "Preview", "#15 Add export", "Status", "failed: Build failed: Dockerfile not found")
	if strings.Contains(out, "Replicas") || strings.Contains(out, "POD") {
		t.Fatalf("a preview that runs nothing shows replicas or pods:\n%s", out)
	}
	out, _, err = execute(t, server.URL, "previews", "status", "15", "--deployment", "api", "--json")
	var status struct {
		Data map[string]any `json:"data"`
	}
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || status.Data["error_message"] != "Build failed: Dockerfile not found" || status.Data["runtime"] != nil {
		t.Fatal(out, err)
	}
	if _, set := status.Data["runtime"]; !set {
		t.Fatalf("the JSON has no runtime field: %s", out)
	}
}

func TestPreviewsLogsOpenAndDelete(t *testing.T) {
	server, requests := fakeAPI(t, previewFixtures)
	out, _, err := execute(t, server.URL, "previews", "logs", "12", "--deployment", "api", "--tail", "50", "--previous")
	if err != nil || out != "ready on :3000\n" || (*requests)[len(*requests)-1] != "GET /api/deployments/d1/previews/12/logs?previous=true&tailLines=50" {
		t.Fatal(out, *requests, err)
	}
	// Without a terminal the URL is printed and no browser opens.
	out, errOut, err := execute(t, server.URL, "previews", "open", "12", "--deployment", "api")
	if err != nil || !strings.Contains(out, `"url": "https://pr-12.api.acme.dev"`) || errOut != "https://pr-12.api.acme.dev\n" {
		t.Fatal(out, errOut, err)
	}
	// A preview without a web address opens nothing: none yet, or one that is no web address.
	for pr, status := range map[string]string{"15": "failed", "16": "active"} {
		_, _, err = execute(t, server.URL, "previews", "open", pr, "--deployment", "api")
		if err == nil || err.Error() != "preview #"+pr+" has no web address; its status is "+status {
			t.Fatal(pr, err)
		}
	}
	sent := len(*requests)
	_, _, err = execute(t, server.URL, "previews", "delete", "12", "--deployment", "api")
	if err == nil || !strings.Contains(err.Error(), "Delete the preview of pull request #12 of deployment api requires confirmation") {
		t.Fatal(err)
	}
	for _, request := range (*requests)[sent:] {
		if strings.HasPrefix(request, "DELETE ") {
			t.Fatalf("deleted without confirmation: %s", request)
		}
	}
	_, errOut, err = execute(t, server.URL, "previews", "delete", "12", "--deployment", "api", "--yes")
	if err != nil || (*requests)[len(*requests)-1] != "DELETE /api/deployments/d1/previews/12" || !strings.Contains(errOut, "✓ Deleting the preview of pull request #12\n") {
		t.Fatal(errOut, *requests, err)
	}
	if _, _, err := execute(t, server.URL, "previews", "delete", "--deployment", "api", "--yes"); err == nil {
		t.Fatal("deleted a preview without its pull request number")
	}
}

func TestPreviewsAreNamedByPullRequestNumber(t *testing.T) {
	server, _ := fakeAPI(t, previewFixtures)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"previews", "status", "fix-login", "--deployment", "api"}, "a preview is named by its pull request number, such as 12"},
		{[]string{"previews", "status", "99", "--deployment", "api"}, "api has no preview of pull request #99; list them with `edka previews list`"},
		{[]string{"previews", "status", "--deployment", "api"}, "api has 3 previews; name a pull request from `edka previews list`"},
		{[]string{"previews", "logs", "12"}, "choose a deployment with --deployment"},
		{[]string{"previews", "remove", "12"}, `unknown command "remove" for "edka previews"`},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
}
