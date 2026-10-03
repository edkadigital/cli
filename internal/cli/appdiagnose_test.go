package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const appPodEvents = "GET /api/clusters/c1/explorer/resources/pods/blog-x2k/events"

// appDiagnoseFixtures is the app blog while it runs, with the routes its
// diagnosis reads. A test replaces the routes of its failure.
func appDiagnoseFixtures(extra map[string]string) map[string]string {
	routes := withFixtures(map[string]string{
		// The pod of the Job that installed the app is not ready, and nothing is wrong with it.
		"GET /api/clusters/c1/apps/a1/pods": `{"success":true,"data":{"pods":[{"name":"blog-7d9","namespace":"strapi","role":"runtime","status":"running","ready":true,"restartCount":0},{"name":"helm-install-blog-abcde","namespace":"strapi","role":"helm-install-blog","status":"succeeded","ready":false,"restartCount":0}]}}`,
		appPodEvents:                        `{"events":[]}`,
	})
	for route, body := range extra {
		routes[route] = body
	}
	return routes
}

// appPods is the answer of the pods route with one pod that is not ready
// beside the one that runs.
func appPods(pod string) string {
	return `{"success":true,"data":{"pods":[{"name":"blog-7d9","namespace":"strapi","status":"running","ready":true,"restartCount":0},` + pod + `]}}`
}

func TestAppDiagnoseFindsNothingInAnAppThatRuns(t *testing.T) {
	server, requests := fakeAPI(t, appDiagnoseFixtures(nil))
	out, errOut, err := execute(t, server.URL, "apps", "diagnose", "blog")
	if err != nil || errOut != "" {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, out, "• No problem found. 1 of 1 pods are ready.\n", "Name", "blog", "App", "strapi", "Version", "5.46.0", "Cluster", "sinaia", "Status", "installed", "POD", "blog-7d9", "running", "helm-install-blog-abcde", "succeeded")
	// The events and the log of a pod explain nothing while every pod is ready.
	for _, request := range *requests {
		if !strings.HasPrefix(request, "GET ") || strings.Contains(request, "/logs") || strings.Contains(request, "/explorer/") {
			t.Fatalf("a diagnosis of an app that runs sent %s", request)
		}
	}
	out, _, err = execute(t, server.URL, "app", "diagnose", "blog", "--json")
	var report struct {
		Healthy  bool
		Findings []finding
		App      map[string]any
		Pods     []map[string]any
		Events   []map[string]any
		Logs     *podLogs
		Unread   []unread
	}
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || !report.Healthy || len(report.Findings) != 1 || report.App["release_name"] != "blog" || len(report.Pods) != 2 || report.Events == nil || report.Logs != nil || len(report.Unread) != 0 {
		t.Fatal(out, err)
	}
}

func TestAppDiagnoseExplainsACrashLoopWithThePreviousLog(t *testing.T) {
	server, requests := fakeAPI(t, appDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/apps/a1/pods": appPods(`{"name":"blog-x2k","namespace":"strapi","status":"failed","ready":false,"restartCount":5,"container":"migrate","reason":"CrashLoopBackOff","message":"back-off 2m40s restarting failed container"}`),
		appPodEvents:                        `{"events":[{"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container migrate","count":14,"lastSeen":"2026-10-02T10:00:00Z","object":{"kind":"Pod","name":"blog-x2k","namespace":"strapi"}},{"type":"Warning","reason":"Unhealthy","message":"Readiness probe failed: connection refused","count":3,"object":{"kind":"Pod","name":"blog-x2k","namespace":"strapi"}}]}`,
		"GET /api/clusters/c1/apps/a1/logs": `{"success":true,"data":{"logs":"running migrations\nerror: relation \"strapi_migrations\" does not exist\n","podName":"blog-x2k","containerName":"migrate","parameters":{"previous":true}}}`,
	}))
	out, _, err := execute(t, server.URL, "apps", "diagnose", "blog", "--tail", "50")
	if err == nil || err.Error() != "blog has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Pod blog-x2k starts and exits, 5 times so far.\n",
		"  back-off 2m40s restarting failed container\n",
		"  Next: Read what it printed last, under Logs.\n",
		"  Next: edka apps logs blog --pod blog-x2k --container migrate --previous --tail 200\n",
		"blog-x2k", "failed", "CrashLoopBackOff",
		"WARNING", "BackOff", "Pod blog-x2k",
		"Logs of blog-x2k, from the container before its last restart\n",
		"error: relation \"strapi_migrations\" does not exist\n")
	// A crash loop fails its probes too, and an app has no revision to roll back to.
	if strings.Contains(out, "fails its health check") || strings.Contains(out, "rollback") {
		t.Fatalf("the report names a second cause, or a rollback:\n%s", out)
	}
	sent := strings.Join(*requests, "\n")
	// The log is the one of the container Edka blames, read in the pod's namespace.
	for _, want := range []string{appPodEvents + "?namespace=strapi", "GET /api/clusters/c1/apps/a1/logs?container=migrate&podName=blog-x2k&previous=true&tailLines=50"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("%s was not read: %v", want, *requests)
		}
	}
}

func TestAppDiagnoseExplainsPodsThatNeverStart(t *testing.T) {
	for _, tc := range []struct {
		name, pod, events string
		want              []string
		unwanted          string
	}{
		// The scheduler's reason is on the pod and in its event, and is one finding.
		{"no node", `{"name":"blog-x2k","namespace":"strapi","status":"pending","ready":false,"restartCount":0,"reason":"Unschedulable","message":"0/3 nodes are available: 3 Insufficient memory."}`,
			`{"events":[{"type":"Warning","reason":"FailedScheduling","message":"0/3 nodes are available: 3 Insufficient memory.","count":4,"object":{"kind":"Pod","name":"blog-x2k","namespace":"strapi"}}]}`,
			[]string{"✗ No node can run Pod blog-x2k.\n", "  0/3 nodes are available: 3 Insufficient memory.\n", "  Next: edka nodepools list\n", "  Next: Add servers with `edka nodepools scale <pool>`.\n", "Unschedulable"}, "✗ No node can run Pod blog-x2k.\n  0/3 nodes are available: 3 Insufficient memory.\n  Next: edka nodepools list\n  Next: Add servers with `edka nodepools scale <pool>`.\n✗"},
		{"image pull", `{"name":"blog-x2k","namespace":"strapi","status":"failed","ready":false,"restartCount":0,"container":"strapi","reason":"ImagePullBackOff","message":"manifest unknown"}`, `{"events":[]}`,
			[]string{"✗ Pod blog-x2k can't pull its image.\n", "  manifest unknown\n", "  Next: edka registries list\n"}, "image ."},
		{"volume", `{"name":"blog-x2k","namespace":"strapi","status":"pending","ready":false,"restartCount":0,"container":"strapi","reason":"ContainerCreating"}`,
			`{"events":[{"type":"Warning","reason":"FailedMount","message":"MountVolume.SetUp failed for volume \"data\": persistentvolumeclaim \"blog-data\" not found","count":6,"object":{"kind":"Pod","name":"blog-x2k","namespace":"strapi"}}]}`,
			[]string{"✗ Pod blog-x2k can't mount a volume.\n", "  MountVolume.SetUp failed for volume \"data\": persistentvolumeclaim \"blog-data\" not found\n", "ContainerCreating"}, "Next:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := fakeAPI(t, appDiagnoseFixtures(map[string]string{
				"GET /api/clusters/c1/apps/a1/pods": appPods(tc.pod),
				appPodEvents:                        tc.events,
			}))
			out, _, err := execute(t, server.URL, "apps", "diagnose", "blog")
			if err == nil || err.Error() != "blog has a problem; see the finding above" {
				t.Fatal(out, err)
			}
			inOrder(t, out, tc.want...)
			if strings.Contains(out, tc.unwanted) {
				t.Fatalf("the report has %q:\n%s", tc.unwanted, out)
			}
			// A container that never started has no log to read.
			if strings.Contains(strings.Join(*requests, "\n"), "/logs") {
				t.Fatalf("a log was read for %s: %v", tc.name, *requests)
			}
		})
	}
}

// While Edka still installs an app, a pod that is not ready yet is a note.
func TestAppDiagnoseNotesAnAppThatIsStillInstalled(t *testing.T) {
	record := func(status, message string) string {
		return `{"success":true,"data":{"id":"a1","app_name":"strapi","display_name":"Team Blog","instance_name":"blog","version":"5.46.0","status":"` + status + `","progress":{"message":"` + message + `"},"namespace":"strapi","release_name":"blog"}}`
	}
	server, _ := fakeAPI(t, appDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/apps/a1":      record("deploying", "Waiting for the pods to become ready"),
		"GET /api/clusters/c1/apps/a1/pods": `{"success":true,"data":{"pods":[{"name":"blog-x2k","namespace":"strapi","status":"pending","ready":false,"restartCount":0,"container":"strapi","reason":"ContainerCreating"}]}}`,
	}))
	out, _, err := execute(t, server.URL, "apps", "diagnose", "blog")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• Edka is still installing or updating the app.\n", "  Waiting for the pods to become ready\n", "• 0 of 1 pods are ready.\n", "  Next: edka apps logs blog\n", "Name", "Team Blog", "Status", "deploying: Waiting for the pods to become ready")
	if strings.Contains(out, "✗") || strings.Contains(out, "No problem found") {
		t.Fatalf("an app that is being installed reads as failed or as healthy:\n%s", out)
	}

	// An install that failed says why in its progress, and may run no pod.
	server, _ = fakeAPI(t, appDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/apps/a1":      record("failed", "Helm install failed: timed out waiting for the condition"),
		"GET /api/clusters/c1/apps/a1/pods": `{"success":true,"data":{"pods":[]}}`,
	}))
	out, _, err = execute(t, server.URL, "apps", "diagnose", "blog")
	if err == nil || err.Error() != "blog has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out, "✗ Edka's last install or update of the app failed.\n", "  Helm install failed: timed out waiting for the condition\n", "  Next: edka apps logs blog\n")
}

func TestAppDiagnoseReportsAnInstalledAppWithNoPods(t *testing.T) {
	server, _ := fakeAPI(t, appDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/apps/a1/pods": `{"success":true,"data":{"pods":[]}}`,
	}))
	out, _, err := execute(t, server.URL, "apps", "diagnose", "blog")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• Edka found no pods of the app in the cluster.\n", "  Next: edka apps get blog\n")
	if strings.Contains(out, "No problem found") {
		t.Fatalf("an app with no pods reads as healthy:\n%s", out)
	}
}

// Edka answers an error for a cluster it can't read, where an empty list
// would read as an app with no pods.
func TestAppDiagnoseReportsWhatItCouldNotRead(t *testing.T) {
	server, _ := fakeAPI(t, appDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/apps/a1/pods": `503 {"code":"CLUSTER_UNREACHABLE","message":"Cluster 'sinaia' is unreachable"}`,
	}))
	out, errOut, err := execute(t, server.URL, "apps", "diagnose", "blog")
	if err == nil {
		t.Fatal("an unreachable cluster exits successfully", out)
	}
	inOrder(t, out, "✗ Edka could not read the app's pods from its cluster.\n", "  Cluster 'sinaia' is unreachable (HTTP 503)\n", "  Next: edka clusters diagnose sinaia\n", "Name", "blog")
	if !strings.Contains(errOut, "Could not read the pods: Cluster 'sinaia' is unreachable (HTTP 503)\n") {
		t.Fatal(errOut)
	}
	if strings.Contains(out, "no pods") {
		t.Fatalf("pods that were not read are reported as none:\n%s", out)
	}
	out, _, _ = execute(t, server.URL, "apps", "diagnose", "blog", "--json")
	var report struct {
		Healthy bool
		Pods    []map[string]any
		Unread  []unread
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Healthy || report.Pods == nil || len(report.Unread) != 1 || report.Unread[0].Source != "pods" {
		t.Fatal(out, err)
	}
}
