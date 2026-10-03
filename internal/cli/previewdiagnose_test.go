package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const previewEvents = "GET /api/clusters/c1/explorer/resources/deployments/api-pr-12/events"

// previewDiagnoseFixtures is a preview that runs, with the routes its
// diagnosis reads. A test replaces the routes of its failure.
func previewDiagnoseFixtures(extra map[string]string) map[string]string {
	routes := map[string]string{
		"GET /api/deployments":                       `{"data":[{"id":"d1","name":"api","cluster_id":"c1","cluster_name":"sinaia","github_deployment_id":"g1"}]}`,
		"GET /api/deployments/d1/previews":           `{"data":[{"id":"p1","pr_number":12,"pr_title":"Fix login","head_ref":"fix/login","base_ref":"main","head_sha":"abc1234def","preview_url":"https://pr-12.api.acme.dev","namespace":"preview","resource_name":"api-pr-12","status":"active"},{"id":"p2","pr_number":15,"pr_title":"Add export","head_ref":"export","namespace":"preview","resource_name":"api-pr-15","status":"failed","error_message":"Build failed: Dockerfile not found"}]}`,
		"GET /api/deployments/d1/previews/12/status": `{"data":{"pr_number":12,"name":"api-pr-12","namespace":"preview","running_image":"registry.edka.dev/api:abc1234","replicas":{"desired":1,"ready":1,"available":1,"updated":1},"pods":[{"name":"api-pr-12-7d9","ready":true,"restartCount":0,"status":"Running"}]}}`,
		"GET /api/deployments/d1/previews/15/status": `404 {"error":"Preview deployment not found in cluster"}`,
		previewEvents: `{"events":[{"type":"Normal","reason":"ScalingReplicaSet","message":"Scaled up replica set api-pr-12-7d9 to 1","count":1,"object":{"kind":"Deployment","name":"api-pr-12","namespace":"preview"}}]}`,
	}
	for route, body := range extra {
		routes[route] = body
	}
	return routes
}

// waitingPreview is the status of preview 12 while its only pod is not ready.
func waitingPreview(phase string, restarts string) string {
	return `{"data":{"pr_number":12,"name":"api-pr-12","namespace":"preview","running_image":"registry.edka.dev/api:abc1234","replicas":{"desired":1,"ready":0,"available":0,"updated":1},"pods":[{"name":"api-pr-12-x2k","ready":false,"restartCount":` + restarts + `,"status":"` + phase + `"}]}}`
}

func TestPreviewDiagnoseFindsNothingInAPreviewThatRuns(t *testing.T) {
	server, requests := fakeAPI(t, previewDiagnoseFixtures(nil))
	out, errOut, err := execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api")
	if err != nil || errOut != "" {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, out, "• No problem found. 1 of 1 pods are ready.\n", "Preview", "#12 Fix login", "Deployment", "api", "Cluster", "sinaia", "Status", "active", "Rollout", "deployed", "Replicas", "1/1 ready", "api-pr-12-7d9")
	sent := strings.Join(*requests, "\n")
	// The cluster's failing pods and a log explain nothing while every pod is ready.
	if strings.Contains(sent, "/pods/problematic") || strings.Contains(sent, "/logs") || !strings.Contains(sent, previewEvents+"?namespace=preview") {
		t.Fatalf("a diagnosis of a preview that runs sent %v", *requests)
	}
	out, _, err = execute(t, server.URL, "previews", "diagnose", "#12", "--deployment", "api", "--json")
	var report struct {
		Healthy  bool
		Findings []finding
		Preview  map[string]any
		Runtime  map[string]any
		Pods     []map[string]any
		Logs     *podLogs
		Unread   []unread
	}
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || !report.Healthy || len(report.Findings) != 1 || report.Preview["pr_title"] != "Fix login" || report.Runtime == nil || report.Pods == nil || report.Logs != nil || len(report.Unread) != 0 {
		t.Fatal(out, err)
	}
}

// The status of a preview has no reason for a pod that is not ready, so the
// reason is the one the cluster reports for that pod.
func TestPreviewDiagnoseExplainsACrashLoopFromTheClustersFailingPods(t *testing.T) {
	server, requests := fakeAPI(t, previewDiagnoseFixtures(map[string]string{
		"GET /api/deployments/d1/previews/12/status": waitingPreview("Running", "3"),
		"GET /api/clusters/c1/pods/problematic":      `{"count":2,"pods":[{"name":"api-pr-12-x2k","namespace":"preview","problemType":"crashing","message":"app: CrashLoopBackOff - back-off 40s restarting failed container","restartCount":4,"containers":[{"name":"app","ready":false,"restartCount":4,"reason":"CrashLoopBackOff","message":"back-off 40s restarting failed container"}]},{"name":"api-pr-9-aaa","namespace":"preview","problemType":"error","message":"app: ImagePullBackOff","restartCount":0,"containers":[{"name":"app","reason":"ImagePullBackOff"}]}]}`,
		previewEvents:                                `{"events":[{"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container app","count":9,"lastSeen":"2026-10-02T10:00:00Z","object":{"kind":"Pod","name":"api-pr-12-x2k","namespace":"preview"}},{"type":"Warning","reason":"Unhealthy","message":"Readiness probe failed: connection refused","count":3,"object":{"kind":"Pod","name":"api-pr-12-x2k","namespace":"preview"}}]}`,
		"GET /api/deployments/d1/previews/12/logs":   `{"logs":"listening on :3000\npanic: DATABASE_URL is not set\n","podName":"api-pr-12-x2k","parameters":{"previous":true}}`,
	}))
	out, _, err := execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api", "--tail", "50")
	if err == nil || err.Error() != "preview #12 has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Pod api-pr-12-x2k starts and exits, 4 times so far.\n",
		"  back-off 40s restarting failed container\n",
		"  Next: Read what it printed last, under Logs.\n",
		"  Next: edka previews logs 12 --deployment api --pod api-pr-12-x2k --previous --tail 200\n",
		"Rollout", "failed",
		"api-pr-12-x2k", "failed", "CrashLoopBackOff",
		"WARNING", "BackOff", "Pod api-pr-12-x2k",
		"Logs of api-pr-12-x2k, from the container before its last restart\n",
		"panic: DATABASE_URL is not set\n")
	// A crash loop fails its probes too, and a preview has no revision to roll back to.
	if strings.Contains(out, "fails its health check") || strings.Contains(out, "rollback") {
		t.Fatalf("the report names a second cause, or a rollback:\n%s", out)
	}
	sent := strings.Join(*requests, "\n")
	for _, want := range []string{"GET /api/clusters/c1/pods/problematic?namespace=preview", "GET /api/deployments/d1/previews/12/logs?podName=api-pr-12-x2k&previous=true&tailLines=50"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("%s was not read: %v", want, *requests)
		}
	}
	// The failing pod of another preview in the namespace is not this preview's.
	out, _, _ = execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api", "--json")
	var report struct {
		Healthy bool
		Pods    []map[string]any
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Healthy || len(report.Pods) != 1 || report.Pods[0]["name"] != "api-pr-12-x2k" {
		t.Fatal(out, err)
	}
}

func TestPreviewDiagnoseExplainsAPodFromItsEvents(t *testing.T) {
	server, _ := fakeAPI(t, previewDiagnoseFixtures(map[string]string{
		"GET /api/deployments/d1/previews/12/status": waitingPreview("Pending", "0"),
		"GET /api/clusters/c1/pods/problematic":      `{"count":1,"pods":[{"name":"api-pr-12-x2k","namespace":"preview","problemType":"pending","message":"0/3 nodes are available: 3 Insufficient memory.","restartCount":0,"containers":[]}]}`,
		previewEvents:                                `{"events":[{"type":"Warning","reason":"FailedScheduling","message":"0/3 nodes are available: 3 Insufficient memory.","count":4,"object":{"kind":"Pod","name":"api-pr-12-x2k","namespace":"preview"}}]}`,
		"GET /api/deployments/d1/previews/12/logs":   `400 {"error":"Pod is not running"}`,
	}))
	out, errOut, err := execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api")
	if err == nil {
		t.Fatal("a preview that no node can run exits successfully", out)
	}
	inOrder(t, out, "✗ No node can run Pod api-pr-12-x2k.\n", "  0/3 nodes are available: 3 Insufficient memory.\n", "  Next: edka nodepools list\n", "  Next: Add servers with `edka nodepools scale <pool>`.\n", "Rollout", "pending")
	if !strings.Contains(errOut, "Could not read the logs of api-pr-12-x2k: ") {
		t.Fatalf("the log that could not be read is not named: %s", errOut)
	}

	// A health check that fails while the rollout still runs is a note.
	server, _ = fakeAPI(t, previewDiagnoseFixtures(map[string]string{
		"GET /api/deployments/d1/previews/12/status": waitingPreview("Running", "0"),
		"GET /api/clusters/c1/pods/problematic":      `{"count":0,"pods":[]}`,
		previewEvents:                                `{"events":[{"type":"Warning","reason":"Unhealthy","message":"Readiness probe failed: connection refused","count":2,"object":{"kind":"Pod","name":"api-pr-12-x2k","namespace":"preview"}}]}`,
		"GET /api/deployments/d1/previews/12/logs":   `{"logs":"starting\n","podName":"api-pr-12-x2k"}`,
	}))
	out, _, err = execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• Pod api-pr-12-x2k fails its health check.\n", "  Next: edka previews logs 12 --deployment api\n", "• The rollout is still running.\n", "  Next: edka previews status 12 --deployment api\n", "Logs of api-pr-12-x2k\n", "starting\n")
	if strings.Contains(out, "✗") {
		t.Fatalf("a rollout that still runs is reported as a problem:\n%s", out)
	}
}

// The pods of the change before stay ready while the ReplicaSet of the last
// change can't create its own, so ready replicas alone are no rollout.
func TestPreviewDiagnoseReportsARolloutThatCannotCreateItsPods(t *testing.T) {
	stuck := `{"data":{"pr_number":12,"name":"api-pr-12","namespace":"preview","running_image":"registry.edka.dev/api:abc1234","replicas":{"desired":1,"ready":1,"available":1,"updated":0},"conditions":[{"type":"Available","status":"True"},{"type":"ReplicaFailure","status":"True","reason":"FailedCreate","message":"pods \"api-pr-12-5c8-\" is forbidden: exceeded quota: pods"}],"pods":[{"name":"api-pr-12-7d9","ready":true,"restartCount":0,"status":"Running"}]}}`
	server, _ := fakeAPI(t, previewDiagnoseFixtures(map[string]string{
		"GET /api/deployments/d1/previews/12/status": stuck,
		previewEvents: `{"events":[{"type":"Warning","reason":"FailedCreate","message":"Error creating: pods \"api-pr-12-5c8-\" is forbidden: exceeded quota: pods","count":12,"object":{"kind":"ReplicaSet","name":"api-pr-12-5c8","namespace":"preview"}}]}`,
	}))
	out, _, err := execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api")
	if err == nil || err.Error() != "preview #12 has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out, "✗ ReplicaSet api-pr-12-5c8 can't create its pods.\n", "  Error creating: pods \"api-pr-12-5c8-\" is forbidden: exceeded quota: pods\n", "Rollout", "pending", "Replicas", "1/1 ready, 0 updated")
	out, _, _ = execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api", "--json")
	var report struct{ Healthy bool }
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Healthy {
		t.Fatal(out, err)
	}

	// The condition says why when the events were not read.
	server, _ = fakeAPI(t, previewDiagnoseFixtures(map[string]string{
		"GET /api/deployments/d1/previews/12/status": stuck,
		previewEvents: `503 {"error":"Cluster unreachable","message":"The cluster did not answer"}`,
	}))
	out, _, err = execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api")
	if err == nil {
		t.Fatal("a rollout that can't create its pods exits successfully", out)
	}
	inOrder(t, out, "✗ The rollout can't create its pods.\n", "  FailedCreate: pods \"api-pr-12-5c8-\" is forbidden: exceeded quota: pods\n")

	// A rollout that has not replaced the pods of the change before still runs.
	server, _ = fakeAPI(t, previewDiagnoseFixtures(map[string]string{
		"GET /api/deployments/d1/previews/12/status": `{"data":{"pr_number":12,"name":"api-pr-12","namespace":"preview","replicas":{"desired":1,"ready":1,"available":1,"updated":0},"pods":[{"name":"api-pr-12-7d9","ready":true,"restartCount":0,"status":"Running"}]}}`,
	}))
	out, _, err = execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api")
	if err != nil || !strings.Contains(out, "• The rollout is still running.\n") || strings.Contains(out, "No problem found") {
		t.Fatal(out, err)
	}
}

// A preview whose build failed runs nothing: its record says why, and the
// cluster is not asked for events or pods.
func TestPreviewDiagnoseReportsAPreviewThatFailed(t *testing.T) {
	server, requests := fakeAPI(t, previewDiagnoseFixtures(nil))
	out, errOut, err := execute(t, server.URL, "previews", "diagnose", "15", "--deployment", "api")
	if err == nil || err.Error() != "preview #15 has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out, "✗ Preview #15 failed.\n", "  Build failed: Dockerfile not found\n", "Preview", "#15 Add export", "Status", "failed: Build failed: Dockerfile not found")
	if strings.Contains(out, "could not read the preview") || strings.Contains(out, "Rollout") {
		t.Fatalf("a preview that runs nothing has a second finding, or a rollout:\n%s", out)
	}
	if !strings.Contains(errOut, "Could not read the runtime status: Preview deployment not found in cluster (HTTP 404)\n") {
		t.Fatal(errOut)
	}
	if sent := strings.Join(*requests, "\n"); strings.Contains(sent, "/explorer/") || strings.Contains(sent, "/pods/problematic") {
		t.Fatalf("a preview that runs nothing was read from the cluster: %v", *requests)
	}
}

func TestPreviewDiagnoseReportsAClusterItCouldNotRead(t *testing.T) {
	server, _ := fakeAPI(t, previewDiagnoseFixtures(map[string]string{
		"GET /api/deployments/d1/previews/12/status": `503 {"error":"Cluster unreachable","message":"The cluster did not answer"}`,
	}))
	out, _, err := execute(t, server.URL, "previews", "diagnose", "12", "--deployment", "api")
	if err == nil {
		t.Fatal("an unreachable cluster exits successfully", out)
	}
	inOrder(t, out, "✗ Edka could not read the preview from its cluster.\n", "  The cluster did not answer (HTTP 503)\n", "  Next: edka clusters diagnose sinaia\n", "Preview", "#12 Fix login")
}
