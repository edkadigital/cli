package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// clusterDiagnoseFixtures is a cluster that works, with the routes its
// diagnosis reads. A test replaces the routes of its failure.
func clusterDiagnoseFixtures(extra map[string]string) map[string]string {
	routes := map[string]string{
		"GET /api/clusters":                       `{"data":[{"id":"c1","name":"sinaia","status":"active"}]}`,
		"GET /api/clusters/c1":                    `{"data":{"id":"c1","name":"sinaia","status":"active","provider":"hetzner","location":"fsn1","k3s_version":"v1.33.1+k3s1","connectivity_status":"connected","keel_webhook_token":"secret-token","worker_node_pools":[{"name":"workers","instance_count":3,"actual_count":3}]}}`,
		"GET /api/clusters/c1/connectivity":       `{"data":{"connectivity_status":"connected","connectivity_probe_error":null,"unreachable_since":null,"open_incident":null,"incidents":[]}}`,
		"GET /api/clusters/c1/drift":              `{"data":{"summary":{"status":"ok","unresolved":0},"records":[],"node_health":[]}}`,
		"GET /api/clusters/c1/deployments/status": `{"data":[{"deployment_id":"d1","name":"api","namespace":"prod","status":"deployed","replicas":{"desired":2,"ready":2}},{"deployment_id":"d2","name":"worker","namespace":"prod","status":"deployed","replicas":{"desired":1,"ready":1}}]}`,
		"GET /api/clusters/c1/pods/problematic":   `{"count":0,"pods":[],"pagination":{"limit":100,"hasMore":false}}`,
		"GET /api/clusters/c1/kubernetes-events":  `{"count":0,"events":[],"pagination":{"limit":50,"hasMore":false}}`,
		// The routes of the deployment api, for the commands that name it.
		"GET /api/clusters/c1/deployments": `{"data":[{"id":"d1","name":"api"}]}`,
		"GET /api/deployments/d1":          `{"data":{"id":"d1","name":"api","cluster_id":"c1","cluster_name":"sinaia","namespace":"prod","status":"deployed","spec_generation":5,"applied_generation":5,"healthy_generation":5}}`,
		"GET /api/deployments/d1/status":   `{"data":{"name":"api","status":"deployed","replicas":{"desired":2,"ready":2,"available":2,"updated":2},"pods":[]}}`,
	}
	for route, body := range extra {
		routes[route] = body
	}
	return routes
}

func TestClusterDiagnoseFindsNothingInAClusterThatWorks(t *testing.T) {
	server, requests := fakeAPI(t, clusterDiagnoseFixtures(nil))
	out, errOut, err := execute(t, server.URL, "clusters", "diagnose", "sinaia")
	if err != nil || errOut != "" {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, out, "• No problem found.\n", "Cluster", "sinaia", "Status", "active", "Connectivity", "connected", "Workers", "3/3", "Node pools", "ok", "Deployments", "2 deployed")
	for _, request := range *requests {
		// Edka's record of an active cluster explains nothing.
		if !strings.HasPrefix(request, "GET ") || request == "GET /api/clusters/c1/events?limit=5" {
			t.Fatalf("a diagnosis of a cluster that works sent %s", request)
		}
	}
	out, _, err = execute(t, server.URL, "clusters", "diagnose", "sinaia", "--json")
	var report struct {
		Healthy      bool
		Findings     []finding
		Cluster      map[string]any
		Connectivity map[string]any
		Deployments  []map[string]any
		Pods         []map[string]any
		Unread       []unread
	}
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || !report.Healthy || len(report.Findings) != 1 || report.Connectivity == nil || len(report.Deployments) != 2 || report.Pods == nil || len(report.Unread) != 0 {
		t.Fatal(out, err)
	}
	if strings.Contains(out, "secret-token") || report.Cluster["name"] != "sinaia" {
		t.Fatalf("the report holds the webhook token, or no cluster:\n%s", out)
	}
}

// `edka diagnose` diagnoses the deployment in context, and the cluster when
// there is none, as `edka status` shows one or the other.
func TestDiagnoseFallsBackToTheCluster(t *testing.T) {
	server, _ := fakeAPI(t, clusterDiagnoseFixtures(nil))
	out, _, err := execute(t, server.URL, "diagnose", "--cluster", "sinaia")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• No problem found.\n", "Cluster", "sinaia", "Connectivity", "connected")
	for _, args := range [][]string{{"diagnose", "--cluster", "sinaia", "--deployment", "api"}, {"diagnose", "api", "--cluster", "sinaia"}} {
		out, _, err = execute(t, server.URL, args...)
		if err != nil {
			t.Fatal(args, out, err)
		}
		inOrder(t, out, "• No problem found. 2 of 2 pods are ready on generation 5.\n", "Deployment", "api")
	}
	// The command of the deployments group diagnoses deployments only.
	if _, _, err = execute(t, server.URL, "deployments", "diagnose", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), "choose a deployment") {
		t.Fatal(err)
	}
	if _, _, err = execute(t, server.URL, "diagnose"); err == nil || !strings.Contains(err.Error(), "choose a cluster") {
		t.Fatal(err)
	}
}

func TestClusterDiagnoseQuotesEdkaForAClusterItCannotReach(t *testing.T) {
	unreachable := `503 {"error":"Cluster unreachable","message":"Cluster 'sinaia' is unreachable"}`
	server, _ := fakeAPI(t, clusterDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/connectivity":       `{"data":{"connectivity_status":"unreachable","connectivity_probe_error":"timeout","unreachable_since":"2026-10-02T09:00:00Z","open_incident":{"id":"i1","scenario":"firewall_blocked","investigation":{"finding":"The servers are still provisioned, but the Hetzner firewall no longer allows Edka management traffic.","suggested_action":"Allow 203.0.113.0/24 on TCP port 6443.","details":{}}},"incidents":[]}}`,
		"GET /api/clusters/c1/deployments/status": unreachable,
		"GET /api/clusters/c1/pods/problematic":   unreachable,
		"GET /api/clusters/c1/kubernetes-events":  unreachable,
	}))
	out, errOut, err := execute(t, server.URL, "clusters", "diagnose", "sinaia")
	if err == nil || err.Error() != "sinaia has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Edka can't reach the cluster's Kubernetes API.\n",
		"  The servers are still provisioned, but the Hetzner firewall no longer allows Edka management traffic.\n",
		"  Next: Allow 203.0.113.0/24 on TCP port 6443.\n",
		"Connectivity", "unreachable since ")
	inOrder(t, errOut, "Could not read the deployments: Cluster 'sinaia' is unreachable (HTTP 503)\n", "Could not read the failing pods: ", "Could not read the events: ")
	if strings.Contains(out, "No problem found") {
		t.Fatalf("an unreachable cluster reads as healthy:\n%s", out)
	}

	// Without an investigation, the error of the probe is the cause.
	server, _ = fakeAPI(t, clusterDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/connectivity": `{"data":{"connectivity_status":"disconnected","connectivity_probe_error":"credentials_rejected","open_incident":null}}`,
	}))
	out, _, err = execute(t, server.URL, "clusters", "diagnose", "sinaia")
	if err == nil {
		t.Fatal("a disconnected cluster exits successfully", out)
	}
	inOrder(t, out, "✗ Edka can't reach the cluster's Kubernetes API, and marked the cluster disconnected.\n", "  credentials_rejected\n")
}

func TestClusterDiagnoseReportsNodePoolsDeploymentsAndPods(t *testing.T) {
	pod := func(namespace, name, problem, message string, restarts int) string {
		body, _ := json.Marshal(map[string]any{"namespace": namespace, "name": name, "problemType": problem, "message": message, "restartCount": restarts})
		return string(body)
	}
	pods := []string{
		// The finding of the deployment api covers its pod.
		pod("prod", "api-7d9-x2k", "crashing", "app: CrashLoopBackOff - back-off 5m0s restarting failed container", 12),
		pod("prod", "worker-5f6-abc", "pending", "0/3 nodes are available: 3 Insufficient memory.", 0),
		pod("kube-system", "coredns-1", "error", "coredns: ImagePullBackOff", 0),
		pod("jobs", "import-1", "failed", "Evicted", 0),
		pod("jobs", "import-2", "crashing", "import: CrashLoopBackOff", 3),
		pod("jobs", "import-3", "crashing", "import: CrashLoopBackOff", 0),
		pod("jobs", "import-4", "failed", "Evicted", 0),
		pod("jobs", "import-5", "failed", "Evicted", 0),
	}
	server, _ := fakeAPI(t, clusterDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/drift":              `{"data":{"summary":{"status":"drifted","unresolved":2},"records":[{"drift_type":"node_not_ready","severity":"warning","pool_name":"workers","details":{"nodeName":"sinaia-pool-workers-worker2","poolName":"workers"},"resolved_at":null},{"drift_type":"quota_blocked_scale","severity":"critical","pool_name":"gpu","details":{"poolName":"gpu","desiredCount":2,"liveCount":1,"serverCount":1,"latestOperationError":"server limit exceeded","reason":"Observed fixed-size pool capacity is below desired count."},"resolved_at":null},{"drift_type":"node_not_ready","severity":"warning","details":{"nodeName":"sinaia-pool-workers-worker9"},"resolved_at":"2026-10-01T10:00:00Z"}]}}`,
		"GET /api/clusters/c1/deployments/status": `{"data":[{"deployment_id":"d1","name":"api","namespace":"prod","status":"failed","message":"CrashLoopBackOff: back-off 5m0s restarting failed container"},{"deployment_id":"d2","name":"worker","namespace":"prod","status":"pending"},{"deployment_id":"d3","name":"ghost","namespace":"prod","status":"not_found"}]}`,
		"GET /api/clusters/c1/pods/problematic":   `{"count":8,"pods":[` + strings.Join(pods, ",") + `]}`,
		"GET /api/clusters/c1/kubernetes-events":  `{"count":2,"events":[{"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container app","namespace":"prod","involvedObjectKind":"Pod","involvedObjectName":"api-7d9-x2k","occurrenceCount":40,"lastSeen":"2026-10-02T10:00:00Z"},{"type":"Normal","reason":"Pulled","message":"Pulled the image","namespace":"prod","involvedObjectKind":"Pod","involvedObjectName":"api-7d9-aaa","occurrenceCount":1}]}`,
	}))
	out, _, err := execute(t, server.URL, "clusters", "diagnose", "sinaia")
	if err == nil || err.Error() != "sinaia has 10 problems; see the findings above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Node sinaia-pool-workers-worker2 is not ready.\n",
		"  Next: edka nodepools list --cluster sinaia\n",
		"  Next: edka api clusters drift repair-plan get sinaia\n",
		"✗ Pool gpu has 1 of 2 nodes.\n", "  server limit exceeded\n",
		"✗ Deployment api is failing.\n", "  CrashLoopBackOff: back-off 5m0s restarting failed container\n", "  Next: edka diagnose api --cluster sinaia\n",
		"✗ Deployment ghost is not in the cluster.\n", "  Next: edka diagnose ghost --cluster sinaia\n",
		"✗ Pod prod/worker-5f6-abc is pending.\n", "  0/3 nodes are available: 3 Insufficient memory.\n", "  Next: edka diagnose worker --cluster sinaia\n",
		"✗ Pod kube-system/coredns-1 is failing.\n", "  coredns: ImagePullBackOff\n",
		"✗ Pod jobs/import-1 failed.\n",
		"✗ Pod jobs/import-2 starts and exits, 3 times so far.\n",
		"✗ Pod jobs/import-3 starts and exits.\n",
		"✗ 2 more pods fail; the table below lists them.\n",
		"Node pools", "drifted", "Deployments", "1 pending, 1 failed, 1 not found",
		"NAMESPACE", "prod", "api-7d9-x2k", "crashing", "import-5",
		"WARNING", "BackOff", "Pod api-7d9-x2k")
	// A difference Edka resolved, a pod its deployment explains and a normal event are no findings.
	for _, unwanted := range []string{"worker9", "Pod prod/api-7d9-x2k", "Pulled"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("the report has %q:\n%s", unwanted, out)
		}
	}
}

func TestClusterDiagnoseReadsEdkasRecordOfAClusterThatIsNotActive(t *testing.T) {
	record := func(status string) string {
		return `{"data":{"id":"c1","name":"sinaia","status":"` + status + `","provider":"hetzner","connectivity_status":"connected"}}`
	}
	activity := `{"data":[{"id":"e2","message":"Failed to create the load balancer: resource limit exceeded","progress":40},{"id":"e1","message":"Creating servers","progress":20}]}`
	server, requests := fakeAPI(t, clusterDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1":        record("failed"),
		"GET /api/clusters/c1/events": activity,
	}))
	out, _, err := execute(t, server.URL, "clusters", "diagnose", "sinaia")
	if err == nil {
		t.Fatal("a failed cluster exits successfully", out)
	}
	inOrder(t, out, "✗ The cluster failed.\n", "  Failed to create the load balancer: resource limit exceeded\n", "  Next: edka clusters get sinaia\n")
	if !strings.Contains(strings.Join(*requests, "\n"), "GET /api/clusters/c1/events?limit=5") {
		t.Fatalf("Edka's record of the cluster was not read: %v", *requests)
	}

	// A cluster that Edka still creates has no problem yet.
	server, _ = fakeAPI(t, clusterDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1":        record("creating"),
		"GET /api/clusters/c1/events": `{"data":[{"id":"e1","message":"Creating servers","progress":20}]}`,
	}))
	out, _, err = execute(t, server.URL, "clusters", "diagnose", "sinaia")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• Edka is still creating the cluster.\n", "  Creating servers\n")
	if strings.Contains(out, "✗") || strings.Contains(out, "No problem found") {
		t.Fatalf("a cluster that is being created reads as failed or as healthy:\n%s", out)
	}
}

// A source that was not read may hold a problem, so the report does not say
// the cluster has none.
func TestClusterDiagnoseQualifiesAReportWithUnreadSources(t *testing.T) {
	server, _ := fakeAPI(t, clusterDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/drift": `500 {"error":"Internal Server Error","message":"Failed to get cluster drift"}`,
	}))
	out, errOut, err := execute(t, server.URL, "clusters", "diagnose", "sinaia")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• No problem found in what could be read.\n")
	inOrder(t, errOut, "Could not read the check of the node pools: Failed to get cluster drift (HTTP 500)\n")
	out, _, _ = execute(t, server.URL, "clusters", "diagnose", "sinaia", "--json")
	var report struct {
		Healthy bool
		Drift   map[string]any
		Unread  []unread
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || !report.Healthy || report.Drift != nil || len(report.Unread) != 1 || report.Unread[0].Source != "check of the node pools" {
		t.Fatal(out, err)
	}
}
