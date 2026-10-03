package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const diagnoseEvents = "GET /api/clusters/c1/explorer/resources/deployments/api/events"

// diagnoseFixtures is a deployment that runs as configured, with the routes a
// diagnosis reads. A test replaces the routes of its failure.
func diagnoseFixtures(extra map[string]string) map[string]string {
	routes := map[string]string{
		"GET /api/deployments":              `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`,
		"GET /api/deployments/d1":           `{"data":{"id":"d1","name":"api","cluster_id":"c1","namespace":"prod","status":"deployed","image_repository":"ghcr.io/acme/api","image_tag":"v3","spec_generation":5,"applied_generation":5,"healthy_generation":5,"config":{"memory_limit":"256Mi"}}}`,
		"GET /api/deployments/d1/status":    `{"data":{"name":"api","status":"deployed","running_image":"ghcr.io/acme/api:v3","configured_image":"ghcr.io/acme/api:v3","replicas":{"desired":2,"ready":2,"available":2,"updated":2},"pods":[{"name":"api-7d9","status":"running","ready":true,"restartCount":0},{"name":"api-8c1","status":"running","ready":true,"restartCount":0}]}}`,
		"GET /api/deployments/d1/revisions": `{"data":[{"generation":5,"status":"applied","source":"settings"},{"generation":4,"status":"superseded","source":"image"}]}`,
		diagnoseEvents:                      `{"generatedAt":"2026-10-02T10:00:00Z","events":[{"type":"Normal","reason":"ScalingReplicaSet","message":"Scaled up replica set api-7d9 to 2","count":1,"lastSeen":"2026-10-02T09:00:00Z","object":{"kind":"Deployment","name":"api","namespace":"prod"}}]}`,
	}
	for route, body := range extra {
		routes[route] = body
	}
	return routes
}

// failingRecord is the deployment while generation 5 does not become healthy.
const failingRecord = `{"data":{"id":"d1","name":"api","cluster_id":"c1","namespace":"prod","status":"deploying","image_repository":"ghcr.io/acme/api","image_tag":"v3","spec_generation":5,"applied_generation":5,"healthy_generation":4,"config":{"memory_limit":"256Mi"}}}`

func failingStatus(message, pod string) string {
	return `{"data":{"name":"api","status":"failed","message":"` + message + `","replicas":{"desired":2,"ready":1,"available":1,"updated":1},"pods":[{"name":"api-7d9","status":"running","ready":true,"restartCount":0},` + pod + `]}}`
}

func TestDiagnoseFindsNothingInAHealthyDeployment(t *testing.T) {
	server, requests := fakeAPI(t, diagnoseFixtures(nil))
	out, errOut, err := execute(t, server.URL, "diagnose", "api")
	if err != nil || errOut != "" {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, out, "• No problem found. 2 of 2 pods are ready on generation 5.\n", "Deployment", "api", "Cluster", "sinaia", "Replicas", "2/2 ready", "api-7d9")
	// The log of a pod that works explains nothing, and a normal event is no warning.
	if strings.Contains(out, "Logs of") || strings.Contains(out, "ScalingReplicaSet") {
		t.Fatalf("a healthy report shows logs or normal events:\n%s", out)
	}
	for _, request := range *requests {
		if !strings.HasPrefix(request, "GET ") || strings.Contains(request, "/logs") || strings.Contains(request, "/builds") {
			t.Fatalf("a diagnosis of a healthy image deployment sent %s", request)
		}
	}
	if !strings.Contains(strings.Join(*requests, "\n"), diagnoseEvents+"?namespace=prod") {
		t.Fatalf("the events were not read in the deployment's namespace: %v", *requests)
	}
	out, _, err = execute(t, server.URL, "deployments", "diagnose", "api", "--json")
	var report struct {
		Healthy  bool
		Findings []finding
		Runtime  map[string]any
		Logs     *podLogs
		Unread   []unread
	}
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || !report.Healthy || len(report.Findings) != 1 || report.Findings[0].Problem || report.Runtime == nil || report.Logs != nil || len(report.Unread) != 0 {
		t.Fatal(out, err)
	}
}

func TestDiagnoseExplainsACrashLoopWithThePreviousLog(t *testing.T) {
	server, requests := fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":        failingRecord,
		"GET /api/deployments/d1/status": failingStatus("CrashLoopBackOff: back-off 5m0s restarting failed container", `{"name":"api-x2k","status":"failed","ready":false,"restartCount":4,"reason":"CrashLoopBackOff","message":"back-off 5m0s restarting failed container"}`),
		diagnoseEvents:                   `{"events":[{"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container api","count":12,"lastSeen":"2026-10-02T10:00:00Z","object":{"kind":"Pod","name":"api-x2k","namespace":"prod"}},{"type":"Warning","reason":"Unhealthy","message":"Readiness probe failed: connection refused","count":3,"lastSeen":"2026-10-02T09:59:00Z","object":{"kind":"Pod","name":"api-x2k","namespace":"prod"}}]}`,
		"GET /api/deployments/d1/logs":   `{"logs":"listening on :8080\npanic: DATABASE_URL is not set\n","podName":"api-x2k","containerName":"api","parameters":{"previous":true}}`,
	}))
	out, _, err := execute(t, server.URL, "diagnose", "api", "--tail", "50")
	if err == nil || err.Error() != "api has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Pod api-x2k starts and exits, 4 times so far.\n",
		"  back-off 5m0s restarting failed container\n",
		"  Next: Read what it printed last, under Logs.\n",
		"  Next: edka logs api --pod api-x2k --previous --tail 200\n",
		"  Next: edka rollback api --generation 4\n",
		"Rollout", "failed: CrashLoopBackOff",
		"api-x2k", "CrashLoopBackOff",
		"WARNING", "BackOff", "Pod api-x2k",
		"Logs of api-x2k, from the container before its last restart\n",
		"panic: DATABASE_URL is not set\n",
	)
	// A crash loop fails its probes too; the report names one cause.
	if strings.Contains(out, "fails its health check") {
		t.Fatalf("the failed probe of a crash loop is a finding of its own:\n%s", out)
	}
	if !strings.Contains(strings.Join(*requests, "\n"), "GET /api/deployments/d1/logs?podName=api-x2k&previous=true&tailLines=50") {
		t.Fatalf("the previous log of the failing pod was not read: %v", *requests)
	}
}

func TestDiagnoseExplainsPodsThatNeverStart(t *testing.T) {
	for _, tc := range []struct {
		name, pod, events string
		want              []string
	}{
		{"image pull", `{"name":"api-x2k","status":"failed","ready":false,"restartCount":0,"reason":"ImagePullBackOff","message":"manifest unknown"}`, `{"events":[]}`,
			[]string{"✗ Pod api-x2k can't pull its image ghcr.io/acme/api:v3.\n", "  manifest unknown\n", "  Next: edka registries list\n", "  Next: edka registries apply <registry>\n"}},
		{"memory limit", `{"name":"api-x2k","status":"failed","ready":false,"restartCount":2,"reason":"OOMKilled"}`, `{"events":[]}`,
			[]string{"✗ Pod api-x2k was killed for using more memory than its limit of 256Mi.\n", "  Next: edka up api --field config.memory_limit=<limit> --wait\n", "  Next: edka rollback api --generation 4\n"}},
		{"missing secret", `{"name":"api-x2k","status":"failed","ready":false,"restartCount":0,"reason":"CreateContainerConfigError","message":"secret \"api-env\" not found"}`, `{"events":[]}`,
			[]string{"✗ Pod api-x2k can't start, because its configuration names something the cluster lacks.\n", "  secret \"api-env\" not found\n", "  Next: edka env --deployment api\n", "  Next: edka env set --secret <NAME> --deployment api\n"}},
		{"no node", `{"name":"api-x2k","status":"pending","ready":false,"restartCount":0}`, `{"events":[{"type":"Warning","reason":"FailedScheduling","message":"0/3 nodes are available: 3 Insufficient memory.","count":4,"lastSeen":"2026-10-02T10:00:00Z","object":{"kind":"Pod","name":"api-x2k","namespace":"prod"}}]}`,
			[]string{"✗ No node can run Pod api-x2k.\n", "  0/3 nodes are available: 3 Insufficient memory.\n", "  Next: edka nodepools list\n"}},
		{"failed probe", `{"name":"api-x2k","status":"pending","ready":false,"restartCount":0}`, `{"events":[{"type":"Warning","reason":"Unhealthy","message":"Readiness probe failed: HTTP probe failed with statuscode: 503","count":40,"lastSeen":"2026-10-02T10:00:00Z","object":{"kind":"Pod","name":"api-x2k","namespace":"prod"}}]}`,
			[]string{"✗ Pod api-x2k fails its health check.\n", "  Readiness probe failed: HTTP probe failed with statuscode: 503\n", "  Next: Check `config.health_checks` and `port`: edka deployments get api --json\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := fakeAPI(t, diagnoseFixtures(map[string]string{
				"GET /api/deployments/d1":        failingRecord,
				"GET /api/deployments/d1/status": failingStatus("the rollout failed", tc.pod),
				diagnoseEvents:                   tc.events,
				"GET /api/deployments/d1/logs":   `{"logs":"started\n","podName":"api-x2k"}`,
			}))
			out, _, err := execute(t, server.URL, "diagnose", "api")
			if err == nil {
				t.Fatal("a failing deployment exits successfully", out)
			}
			inOrder(t, out, tc.want...)
			// A container that never started has no log to read.
			read := strings.Contains(strings.Join(*requests, "\n"), "/logs")
			if started := tc.name == "memory limit" || tc.name == "failed probe" || tc.name == "no node"; read != started {
				t.Fatalf("log read %v for %s: %v", read, tc.name, *requests)
			}
		})
	}
}

// A health check that fails while the rollout still runs may pass once the
// container is up, so the report notes it and finds no problem.
func TestDiagnoseNotesARolloutThatStillRuns(t *testing.T) {
	server, _ := fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":        failingRecord,
		"GET /api/deployments/d1/status": `{"data":{"name":"api","status":"pending","message":"Waiting for deployment rollout to finish (1/2 updated replicas, 1/2 pods ready)","replicas":{"desired":2,"ready":1,"available":1,"updated":1},"pods":[{"name":"api-x2k","status":"pending","ready":false,"restartCount":0}]}}`,
		diagnoseEvents:                   `{"events":[{"type":"Warning","reason":"Unhealthy","message":"Readiness probe failed: connection refused","count":2,"lastSeen":"2026-10-02T10:00:00Z","object":{"kind":"Pod","name":"api-x2k","namespace":"prod"}}]}`,
		"GET /api/deployments/d1/logs":   `{"logs":"starting\n","podName":"api-x2k"}`,
	}))
	out, _, err := execute(t, server.URL, "diagnose", "api")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• Pod api-x2k fails its health check.\n", "• The rollout is still running.\n", "  Waiting for deployment rollout to finish", "Logs of api-x2k\n", "starting\n")
	if strings.Contains(out, "✗") {
		t.Fatalf("a running rollout is reported as a problem:\n%s", out)
	}
}

// The events of a deployment include those of the ReplicaSets it keeps from
// earlier rollouts and of pods that are ready by now.
func TestDiagnoseKeepsResolvedWarningsOutOfTheFindings(t *testing.T) {
	events := `{"events":[{"type":"Warning","reason":"FailedCreate","message":"Error creating: pods \"api-5f6-\" is forbidden: exceeded quota","count":9,"lastSeen":"2026-10-02T09:20:00Z","object":{"kind":"ReplicaSet","name":"api-5f6","namespace":"prod"}},{"type":"Warning","reason":"FailedScheduling","message":"0/3 nodes are available: 3 Insufficient memory.","count":2,"lastSeen":"2026-10-02T09:10:00Z","object":{"kind":"Pod","name":"api-7d9","namespace":"prod"}}]}`
	pods := `"pods":[{"name":"api-7d9","status":"running","ready":true,"restartCount":0},{"name":"api-x2k","status":"pending","ready":false,"restartCount":0}]`
	server, _ := fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":        failingRecord,
		"GET /api/deployments/d1/status": `{"data":{"name":"api","status":"pending","message":"Waiting for deployment rollout to finish (1/2 updated replicas, 1/2 pods ready)","replicas":{"desired":2,"ready":1,"available":1,"updated":1},"conditions":[{"type":"Progressing","status":"True","reason":"ReplicaSetUpdated"}],` + pods + `}}`,
		diagnoseEvents:                   events,
		"GET /api/deployments/d1/logs":   `{"logs":"starting\n","podName":"api-x2k"}`,
	}))
	out, _, err := execute(t, server.URL, "diagnose", "api")
	if err != nil {
		t.Fatal(out, err)
	}
	// The warnings are still listed under what the report read.
	inOrder(t, out, "• The rollout is still running.\n", "WARNING", "FailedCreate", "ReplicaSet api-5f6", "FailedScheduling", "Pod api-7d9")
	if strings.Contains(out, "✗") || strings.Contains(out, "can't create its pods") || strings.Contains(out, "No node can run") {
		t.Fatalf("a resolved warning is a finding:\n%s", out)
	}

	// Kubernetes keeps the condition on the Deployment while a ReplicaSet can't create its pods.
	server, _ = fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":        failingRecord,
		"GET /api/deployments/d1/status": `{"data":{"name":"api","status":"failed","message":"pods \"api-5f6-\" is forbidden: exceeded quota","replicas":{"desired":2,"ready":1,"available":1,"updated":0},"conditions":[{"type":"ReplicaFailure","status":"True","reason":"FailedCreate"}],"pods":[{"name":"api-7d9","status":"running","ready":true,"restartCount":0}]}}`,
		diagnoseEvents:                   events,
	}))
	out, _, err = execute(t, server.URL, "diagnose", "api")
	if err == nil || err.Error() != "api has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out, "✗ ReplicaSet api-5f6 can't create its pods.\n", "  Error creating: pods \"api-5f6-\" is forbidden: exceeded quota\n")
	if strings.Contains(out, "No node can run") {
		t.Fatalf("the warning of a pod that is ready is a finding:\n%s", out)
	}
}

func TestDiagnoseReportsAnAutomaticRollback(t *testing.T) {
	server, _ := fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":           `{"data":{"id":"d1","name":"api","cluster_id":"c1","namespace":"prod","status":"deployed","image_repository":"ghcr.io/acme/api","image_tag":"v2","spec_generation":6,"applied_generation":6,"healthy_generation":6}}`,
		"GET /api/deployments/d1/revisions": `{"data":[{"generation":6,"status":"applied","source":"auto-rollback","rollback_of_generation":4},{"generation":5,"status":"failed","source":"image","status_message":"ImagePullBackOff: manifest unknown"},{"generation":4,"status":"superseded","source":"image"}]}`,
	}))
	out, _, err := execute(t, server.URL, "diagnose", "api")
	if err == nil {
		t.Fatal("a rolled back change exits successfully", out)
	}
	inOrder(t, out, "✗ Generation 5 failed, and Edka rolled back to generation 4.\n", "  ImagePullBackOff: manifest unknown\n", "  Next: edka deployments revisions api\n")
	if strings.Contains(out, "No problem found") {
		t.Fatalf("a rolled back change reads as healthy:\n%s", out)
	}
}

// A rollback is a revision that rolls out, so it is not over until it is
// applied, and it can fail while the pods of an earlier generation still run.
func TestDiagnoseReportsARollbackThatDidNotFinish(t *testing.T) {
	record := func(status, message string) string {
		return `{"data":{"id":"d1","name":"api","cluster_id":"c1","namespace":"prod","status":"` + status + `","status_message":"` + message + `","image_repository":"ghcr.io/acme/api","image_tag":"v2","spec_generation":6,"applied_generation":6,"healthy_generation":4}}`
	}
	revisions := func(status, message string) string {
		return `{"data":[{"generation":6,"status":"` + status + `","source":"auto-rollback","rollback_of_generation":4,"status_message":"` + message + `"},{"generation":5,"status":"failed","source":"image","status_message":"ImagePullBackOff: manifest unknown"},{"generation":4,"status":"superseded","source":"image"}]}`
	}
	server, _ := fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":           record("failed", "exceeded quota: pods"),
		"GET /api/deployments/d1/revisions": revisions("failed", "exceeded quota: pods"),
	}))
	out, _, err := execute(t, server.URL, "diagnose", "api")
	if err == nil || err.Error() != "api has 2 problems; see the findings above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Generation 5 failed.\n", "  ImagePullBackOff: manifest unknown\n",
		"✗ Edka could not roll back to generation 4.\n", "  exceeded quota: pods\n", "  Next: edka deployments revisions api\n")
	// The rollback's failure is the row's too, and is reported once.
	if strings.Contains(out, "rolled back") || strings.Contains(out, "Generation 6 failed") {
		t.Fatalf("a failed rollback reads as done, or twice:\n%s", out)
	}

	server, _ = fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":           record("deploying", ""),
		"GET /api/deployments/d1/revisions": revisions("applying", ""),
	}))
	out, _, err = execute(t, server.URL, "diagnose", "api")
	if err == nil {
		t.Fatal("a change that is being rolled back exits successfully", out)
	}
	inOrder(t, out, "✗ Generation 5 failed, and Edka is rolling back to generation 4.\n", "  ImagePullBackOff: manifest unknown\n")
	if strings.Contains(out, "rolled back") {
		t.Fatalf("a rollback that still runs reads as done:\n%s", out)
	}
}

// A diagnosis is for a deployment that does not work, so a source that fails
// is named and the rest is still reported.
func TestDiagnoseReportsWhatItCouldNotRead(t *testing.T) {
	server, _ := fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":        failingRecord,
		"GET /api/deployments/d1/status": `503 {"error":"Cluster unreachable","message":"The cluster did not answer"}`,
		diagnoseEvents:                   `503 {"error":"Cluster unreachable","message":"The cluster did not answer"}`,
	}))
	out, errOut, err := execute(t, server.URL, "diagnose", "api")
	if err == nil {
		t.Fatal("an unreachable cluster exits successfully", out)
	}
	inOrder(t, out, "✗ Edka could not read the deployment from its cluster.\n", "  The cluster did not answer (HTTP 503)\n", "  Next: edka clusters diagnose sinaia\n", "Deployment", "Revision", "generation 5 (applied 5, healthy 4)")
	inOrder(t, errOut, "Could not read the runtime status: ", "Could not read the events: ")
	out, _, _ = execute(t, server.URL, "diagnose", "api", "--json")
	var report struct {
		Healthy bool
		Runtime map[string]any
		Unread  []unread
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Healthy || report.Runtime != nil || len(report.Unread) != 2 || report.Unread[0].Source != "runtime status" {
		t.Fatal(out, err)
	}
}

func TestDiagnoseReadsTheLastBuildOfAGitDeployment(t *testing.T) {
	server, requests := fakeAPI(t, diagnoseFixtures(map[string]string{
		"GET /api/deployments/d1":        `{"data":{"id":"d1","name":"api","cluster_id":"c1","namespace":"prod","status":"deployed","github_deployment_id":"g1","image_repository":"registry.edka.dev/api","image_tag":"abc1234","spec_generation":5,"applied_generation":5,"healthy_generation":5}}`,
		"GET /api/deployments/d1/builds": `{"data":[{"id":"b9","status":"failed","commit_sha":"def5678abc","error_message":"Dockerfile not found"}]}`,
	}))
	out, _, err := execute(t, server.URL, "diagnose", "api")
	if err == nil {
		t.Fatal("a failed build exits successfully", out)
	}
	inOrder(t, out, "✗ The last build failed.\n", "  Dockerfile not found\n", "  Next: edka build api --wait\n", "Last build", "failed def5678")
	if !strings.Contains(strings.Join(*requests, "\n"), "GET /api/deployments/d1/builds?limit=1") {
		t.Fatalf("the last build was not read: %v", *requests)
	}
}
