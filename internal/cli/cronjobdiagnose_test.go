package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const cronjobEvents = "GET /api/clusters/c1/explorer/resources/cronjobs/nightly-report/events"

// cronjobDiagnoseFixtures is a cronjob whose last run succeeded, with the
// routes its diagnosis reads. A test replaces the routes of its failure.
func cronjobDiagnoseFixtures(extra map[string]string) map[string]string {
	routes := map[string]string{
		"GET /api/clusters":             `{"data":[{"id":"c1","name":"sinaia"}]}`,
		"GET /api/clusters/c1/cronjobs": `{"data":[{"id":"j1","name":"nightly-report","schedule":"0 3 * * *","status":"deployed"}]}`,
		"GET /api/cronjobs/j1":          `{"data":{"id":"j1","name":"nightly-report","cluster_id":"c1","namespace":"reports","schedule":"0 3 * * *","status":"deployed","suspend":false,"image_repository":"ghcr.io/acme/report","image_tag":"v4"}}`,
		"GET /api/cronjobs/j1/status":   `{"data":{"lastScheduleTime":"2026-10-02T03:00:00Z","activeJobs":0,"lastSuccessfulTime":"2026-10-02T03:04:10Z"}}`,
		"GET /api/cronjobs/j1/jobs":     `{"data":[{"name":"nightly-report-29312","status":"Succeeded","startTime":"2026-10-02T03:00:02Z","completionTime":"2026-10-02T03:04:10Z"},{"name":"nightly-report-29311","status":"Failed","startTime":"2026-10-01T03:00:02Z","completionTime":"2026-10-01T03:01:00Z"}]}`,
		cronjobEvents:                   `{"events":[{"type":"Normal","reason":"SuccessfulCreate","message":"Created job nightly-report-29312","count":1,"object":{"kind":"CronJob","name":"nightly-report","namespace":"reports"}}]}`,
	}
	for route, body := range extra {
		routes[route] = body
	}
	return routes
}

func TestCronjobDiagnoseFindsNothingWhenTheLastRunSucceeded(t *testing.T) {
	server, requests := fakeAPI(t, cronjobDiagnoseFixtures(nil))
	out, errOut, err := execute(t, server.URL, "cronjobs", "diagnose", "nightly-report")
	if err != nil || errOut != "" {
		t.Fatal(out, errOut, err)
	}
	// A run that failed before the last one is in the table and is no finding.
	inOrder(t, out, "• No problem found. The last run, nightly-report-29312, succeeded.\n", "Cronjob", "nightly-report", "Cluster", "sinaia", "Namespace", "reports", "Schedule", "0 3 * * *", "RUN", "nightly-report-29312", "Succeeded", "4m8s", "nightly-report-29311", "Failed")
	sent := strings.Join(*requests, "\n")
	if strings.Contains(sent, "/logs") || !strings.Contains(sent, "GET /api/cronjobs/j1/jobs?limit=5") || !strings.Contains(sent, cronjobEvents+"?namespace=reports") {
		t.Fatalf("a diagnosis of a cronjob that works sent %v", *requests)
	}
	out, _, err = execute(t, server.URL, "cronjobs", "diagnose", "nightly-report", "--json")
	var report struct {
		Healthy  bool
		Findings []finding
		Cronjob  map[string]any
		Runtime  map[string]any
		Runs     []map[string]any
		Logs     *podLogs
		Unread   []unread
	}
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || !report.Healthy || len(report.Findings) != 1 || report.Cronjob["schedule"] != "0 3 * * *" || report.Runtime == nil || len(report.Runs) != 2 || report.Logs != nil || len(report.Unread) != 0 {
		t.Fatal(out, err)
	}
}

func TestCronjobDiagnoseExplainsARunThatFailed(t *testing.T) {
	server, requests := fakeAPI(t, cronjobDiagnoseFixtures(map[string]string{
		// A run that still runs is not the last one that ended.
		"GET /api/cronjobs/j1/jobs": `{"data":[{"name":"nightly-report-manual-7","status":"Running","startTime":"2026-10-02T09:00:00Z","completionTime":null},{"name":"nightly-report-29312","status":"Failed","startTime":"2026-10-02T03:00:02Z","completionTime":"2026-10-02T03:01:00Z"},{"name":"nightly-report-29311","status":"Succeeded","startTime":"2026-10-01T03:00:02Z","completionTime":"2026-10-01T03:04:10Z"}]}`,
		cronjobEvents:               `{"events":[{"type":"Warning","reason":"BackoffLimitExceeded","message":"Job has reached the specified backoff limit","count":1,"lastSeen":"2026-10-02T03:01:00Z","object":{"kind":"Job","name":"nightly-report-29312","namespace":"reports"}},{"type":"Warning","reason":"BackOff","message":"Back-off restarting failed container","count":3,"object":{"kind":"Pod","name":"nightly-report-29311-abc","namespace":"reports"}}]}`,
		"GET /api/cronjobs/j1/logs": `{"logs":"connecting to the database\nerror: password authentication failed\n","podName":"nightly-report-29312-x7k","jobName":"nightly-report-29312"}`,
	}))
	out, _, err := execute(t, server.URL, "cronjobs", "diagnose", "nightly-report", "--tail", "60")
	if err == nil || err.Error() != "nightly-report has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ The last run, nightly-report-29312, failed.\n",
		"  BackoffLimitExceeded: Job has reached the specified backoff limit\n",
		"  Next: Read what it printed last, under Logs.\n",
		"  Next: edka cronjobs logs nightly-report --run nightly-report-29312\n",
		"RUN", "nightly-report-manual-7", "Running",
		"WARNING", "BackoffLimitExceeded", "Job nightly-report-29312",
		"Logs of nightly-report-29312-x7k\n",
		"error: password authentication failed\n")
	if !strings.Contains(strings.Join(*requests, "\n"), "GET /api/cronjobs/j1/logs?jobName=nightly-report-29312&tailLines=60") {
		t.Fatalf("the log of the failed run was not read: %v", *requests)
	}
}

// A Job that can't create its pod has neither ended nor started: Edka reports
// it as Unknown, and the run before it may have succeeded.
func TestCronjobDiagnoseExplainsARunThatCannotCreateItsPod(t *testing.T) {
	jobs := func(status string) string {
		return `{"data":[{"name":"nightly-report-29313","status":"` + status + `","startTime":"2026-10-03T03:00:02Z","completionTime":null},{"name":"nightly-report-29312","status":"Succeeded","startTime":"2026-10-02T03:00:02Z","completionTime":"2026-10-02T03:04:10Z"}]}`
	}
	events := `{"events":[{"type":"Warning","reason":"FailedCreate","message":"Error creating: pods \"nightly-report-29313-\" is forbidden: exceeded quota: pods, requested: pods=1, used: pods=10, limited: pods=10","count":7,"object":{"kind":"Job","name":"nightly-report-29313","namespace":"reports"}}]}`
	server, requests := fakeAPI(t, cronjobDiagnoseFixtures(map[string]string{"GET /api/cronjobs/j1/jobs": jobs("Unknown"), cronjobEvents: events}))
	out, _, err := execute(t, server.URL, "cronjobs", "diagnose", "nightly-report")
	if err == nil || err.Error() != "nightly-report has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Kubernetes can't create the pod of nightly-report-29313.\n",
		"  Error creating: pods \"nightly-report-29313-\" is forbidden: exceeded quota: pods, requested: pods=1, used: pods=10, limited: pods=10\n",
		"RUN", "nightly-report-29313", "Unknown",
		"WARNING", "FailedCreate", "Job nightly-report-29313")
	// A run that has no pod has no log.
	if strings.Contains(out, "No problem found") || strings.Contains(strings.Join(*requests, "\n"), "/logs") {
		t.Fatalf("the report reads as healthy, or a log was read:\n%s\n%v", out, *requests)
	}

	// The warning is of the past once the run has its pod.
	server, _ = fakeAPI(t, cronjobDiagnoseFixtures(map[string]string{"GET /api/cronjobs/j1/jobs": jobs("Running"), cronjobEvents: events}))
	out, _, err = execute(t, server.URL, "cronjobs", "diagnose", "nightly-report")
	if err != nil || !strings.Contains(out, "• No problem found. The last run, nightly-report-29312, succeeded.\n") {
		t.Fatal(out, err)
	}
}

func TestCronjobDiagnoseReportsAScheduleThatStartsNoRun(t *testing.T) {
	record := func(status string, suspend bool) string {
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "j1", "name": "nightly-report", "cluster_id": "c1", "namespace": "reports", "schedule": "0 3 * * *", "status": status, "suspend": suspend}})
		return string(body)
	}
	none := `{"data":[]}`
	for _, tc := range []struct {
		name, record, events string
		problem              bool
		want                 []string
	}{
		{"suspended", record("deployed", true), `{"events":[]}`, false,
			[]string{"• The schedule is suspended, so no run starts.\n", "  Next: edka cronjobs resume nightly-report\n", "Status", "suspended"}},
		{"never ran", record("deployed", false), `{"events":[]}`, false,
			[]string{"• No run has started yet.\n", "  Next: edka cronjobs trigger nightly-report\n"}},
		{"can't start", record("deployed", false), `{"events":[{"type":"Warning","reason":"FailedCreate","message":"Error creating job: exceeded quota","count":2,"object":{"kind":"CronJob","name":"nightly-report","namespace":"reports"}}]}`, true,
			[]string{"✗ Kubernetes could not start a run.\n", "  FailedCreate: Error creating job: exceeded quota\n"}},
		{"missed runs", record("deployed", false), `{"events":[{"type":"Warning","reason":"TooManyMissedTimes","message":"too many missed start times","count":1,"object":{"kind":"CronJob","name":"nightly-report","namespace":"reports"}}]}`, false,
			[]string{"• Kubernetes warns about the cronjob's schedule.\n", "  TooManyMissedTimes: too many missed start times\n"}},
		{"failed change", record("failed", false), `{"events":[]}`, true,
			[]string{"✗ Edka's last change to the cronjob failed.\n", "  Next: edka cronjobs get nightly-report\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := fakeAPI(t, cronjobDiagnoseFixtures(map[string]string{
				"GET /api/cronjobs/j1":        tc.record,
				"GET /api/cronjobs/j1/status": `{"data":{"lastScheduleTime":null,"activeJobs":0,"lastSuccessfulTime":null}}`,
				"GET /api/cronjobs/j1/jobs":   none,
				cronjobEvents:                 tc.events,
			}))
			out, _, err := execute(t, server.URL, "cronjobs", "diagnose", "nightly-report")
			if (err != nil) != tc.problem {
				t.Fatal(out, err)
			}
			inOrder(t, out, tc.want...)
			if strings.Contains(out, "No problem found") {
				t.Fatalf("the report reads as healthy:\n%s", out)
			}
		})
	}
}

// Edka answers the status of a cronjob with no times when it can't read the
// cluster, so only runs that were read show that none started.
func TestCronjobDiagnoseReportsWhatItCouldNotRead(t *testing.T) {
	unreachable := `503 {"error":"Cluster unreachable","message":"The cluster did not answer"}`
	server, _ := fakeAPI(t, cronjobDiagnoseFixtures(map[string]string{
		"GET /api/cronjobs/j1/status": `{"data":{"lastScheduleTime":null,"activeJobs":0,"lastSuccessfulTime":null}}`,
		"GET /api/cronjobs/j1/jobs":   unreachable,
		cronjobEvents:                 unreachable,
	}))
	out, errOut, err := execute(t, server.URL, "cronjobs", "diagnose", "nightly-report")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• No problem found in what could be read.\n")
	inOrder(t, errOut, "Could not read the runs: The cluster did not answer (HTTP 503)\n", "Could not read the events: ")
	if strings.Contains(out, "No run has started yet") {
		t.Fatalf("runs that were not read are reported as none:\n%s", out)
	}
	out, _, _ = execute(t, server.URL, "cronjobs", "diagnose", "nightly-report", "--json")
	var report struct {
		Runs   []map[string]any
		Unread []unread
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Runs == nil || len(report.Unread) != 2 || report.Unread[0].Source != "runs" {
		t.Fatal(out, err)
	}
}
