package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/config"
)

// fastPolls makes waits poll without pausing.
func fastPolls(t *testing.T) {
	t.Helper()
	interval, start := pollInterval, rolloutStartWait
	pollInterval, rolloutStartWait = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { pollInterval, rolloutStartWait = interval, start })
}

// sequenceAPI answers each route with its responses in turn, repeating the
// last one, and records every request. A response such as "502 {...}" answers
// with that status.
func sequenceAPI(t *testing.T, routes map[string][]string) (*httptest.Server, *[]string) {
	t.Helper()
	requests := []string{}
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		requests = append(requests, route)
		responses, ok := routes[route]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
			return
		}
		response := responses[min(calls[route], len(responses)-1)]
		calls[route]++
		var code int
		if n, _ := fmt.Sscanf(response, "%d ", &code); n == 1 && code >= 400 {
			w.WriteHeader(code)
			response = response[4:]
		}
		fmt.Fprint(w, response)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// inOrder fails unless each part appears in s after the one before it.
func inOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	rest := s
	for _, part := range parts {
		i := strings.Index(rest, part)
		if i < 0 {
			t.Fatalf("missing %q in order in:\n%s", part, s)
		}
		rest = rest[i+len(part):]
	}
}

func TestLogStreamPrintsWholeLinesOnce(t *testing.T) {
	var out bytes.Buffer
	var s logStream
	for _, log := range []string{"Waiting for logs...", "step 1\nstep 2\nhal", "step 1\nstep 2\nhalf a line\nstep 3\n"} {
		s.write(&out, log, false)
	}
	s.write(&out, "step 1\nstep 2\nhalf a line\nstep 3\ndone", true)
	if got := out.String(); got != "step 1\nstep 2\nhalf a line\nstep 3\ndone\n" {
		t.Fatalf("%q", got)
	}
	out.Reset()
	var none logStream
	none.write(&out, "Waiting for logs...", true)
	if out.Len() != 0 {
		t.Fatalf("printed Edka's note as a log: %q", out.String())
	}
}

// buildRoutes answers a Git deployment d1 named api whose build b1 succeeds.
func buildRoutes(autoDeploy bool) map[string][]string {
	return map[string][]string{
		"GET /api/deployments": {`{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`},
		"GET /api/deployments/d1": {
			`{"data":{"id":"d1","github_deployment_id":"g1","spec_generation":4,"applied_generation":4,"healthy_generation":4,"status":"deployed"}}`,
			`{"data":{"id":"d1","spec_generation":5,"applied_generation":4,"healthy_generation":4,"status":"updating"}}`,
			`{"data":{"id":"d1","spec_generation":5,"applied_generation":5,"healthy_generation":4,"status":"updating"}}`,
			`{"data":{"id":"d1","spec_generation":5,"applied_generation":5,"healthy_generation":5,"status":"deployed"}}`,
		},
		"POST /api/deployments/d1/builds": {`{"data":{"id":"b1","status":"queued"}}`},
		"GET /api/deployments/d1/builds/b1/logs": {
			`{"data":{"logs":"Waiting for logs...","status":"queued","completed":false}}`,
			`{"data":{"logs":"#1 load\n#2 build\nhal","status":"building","completed":false}}`,
			`{"data":{"logs":"#1 load\n#2 build\nhalf\n#3 push\n","status":"pushing","completed":false}}`,
			`{"data":{"logs":"#1 load\n#2 build\nhalf\n#3 push\n#4 done","status":"success","completed":true}}`,
		},
		"GET /api/deployments/d1/builds/b1": {`{"data":{"id":"b1","status":"success","commit_sha":"abc1234def","commit_message":"Fix login\n\nDetails","image_tag":"abc1234","image_uri":"registry.edka.dev/api:abc1234","duration_seconds":134,"build_logs":"#1 load"}}`},
		"GET /api/deployments/d1/github":    {fmt.Sprintf(`{"data":{"auto_deploy_enabled":%t}}`, autoDeploy)},
		"GET /api/deployments/d1/revisions": {
			`{"data":[{"generation":4,"source":"deploy","image_tag":"abc1234"}]}`,
			`{"data":[{"generation":5,"source":"build","image_tag":"abc1234"},{"generation":4,"source":"deploy","image_tag":"abc1234"}]}`,
		},
		"GET /api/deployments/d1/status": {
			`{"data":{"status":"progressing","replicas":{"desired":2,"updated":1,"ready":1}}}`,
			`{"data":{"status":"deployed","replicas":{"desired":2,"updated":2,"ready":2}}}`,
		},
	}
}

func TestBuildWaitStreamsLogsAndFollowsAutoDeploy(t *testing.T) {
	fastPolls(t)
	server, requests := sequenceAPI(t, buildRoutes(true))
	out, errOut, err := execute(t, server.URL, "build", "api", "--wait", "--json")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut,
		"Build submitted. Waiting for it to finish…\n",
		"Waiting for the build to start…\n",
		"Building the image…\n#1 load\n#2 build\n",
		"Pushing the image…\nhalf\n#3 push\n#4 done\n",
		"✓ Built abc1234 in 2m14s\n",
		"Auto-deploy started generation 5…\n",
		"Applying generation 5…\n",
		"Rolling out: 1/2 updated, 1/2 ready\n",
		"Rolling out: 2/2 updated, 2/2 ready\n",
		"✓ api is running generation 5\n",
	)
	if strings.Contains(errOut, "Waiting for logs") || strings.Count(errOut, "#1 load") != 1 || strings.Count(errOut, "Applying") != 1 {
		t.Fatalf("repeated or placeholder lines:\n%s", errOut)
	}
	var build map[string]any
	if err := json.Unmarshal([]byte(out), &build); err != nil || build["data"].(map[string]any)["id"] != "b1" {
		t.Fatalf("stdout is not the build: %q %v", out, err)
	}
	for _, request := range *requests {
		if strings.HasPrefix(request, "POST ") && request != "POST /api/deployments/d1/builds" {
			t.Fatalf("deployed the build a second time: %s", request)
		}
	}
}

func TestBuildWaitWithAutoDeployOffSuggestsUp(t *testing.T) {
	fastPolls(t)
	server, requests := sequenceAPI(t, buildRoutes(false))
	out, errOut, err := execute(t, server.URL, "build", "api", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	if !strings.Contains(errOut, "Auto-deploy is off. Next: edka up api --wait") {
		t.Fatal(errOut)
	}
	inOrder(t, out, "Deployment", "api", "Build", "b1", "Commit", "abc1234 Fix login\n", "Image", "registry.edka.dev/api:abc1234", "Duration", "2m14s")
	if strings.Contains(out, "Deployed") || strings.Contains(out, "#1 load") {
		t.Fatalf("summary shows a rollout or the log:\n%s", out)
	}
	for _, request := range *requests {
		if strings.Contains(request, "/revisions") {
			t.Fatalf("looked for a rollout with auto-deploy off: %s", request)
		}
	}
}

func TestBuildWaitReportsFailures(t *testing.T) {
	fastPolls(t)
	failed := buildRoutes(true)
	failed["GET /api/deployments/d1/builds/b1/logs"] = []string{`{"data":{"logs":"#1 load\nerror: no start command\n","status":"failed","completed":true}}`}
	failed["GET /api/deployments/d1/builds/b1"] = []string{`{"data":{"id":"b1","status":"failed","error_message":"Railpack could not find a start command"}}`}
	server, _ := sequenceAPI(t, failed)
	_, errOut, err := execute(t, server.URL, "build", "api", "--wait")
	if err == nil || err.Error() != "build failed: Railpack could not find a start command" || !strings.Contains(errOut, "error: no start command\n") {
		t.Fatalf("%v\n%s", err, errOut)
	}

	// Auto-deploy is on, but no rollout of the build starts.
	stalled := buildRoutes(true)
	stalled["GET /api/deployments/d1/revisions"] = []string{`{"data":[{"generation":4,"source":"deploy","image_tag":"abc1234"}]}`}
	server, _ = sequenceAPI(t, stalled)
	_, _, err = execute(t, server.URL, "build", "api", "--wait")
	if err == nil || !strings.Contains(err.Error(), "no rollout of it started") || !strings.Contains(err.Error(), "edka up api --wait") {
		t.Fatal(err)
	}
}

func TestBuildNamesImageDeployments(t *testing.T) {
	server, requests := sequenceAPI(t, map[string][]string{
		"GET /api/deployments":    {`{"data":[{"id":"d1","name":"nginx","cluster_name":"sinaia"}]}`},
		"GET /api/deployments/d1": {`{"data":{"id":"d1","github_deployment_id":null,"spec_generation":3}}`},
	})
	_, _, err := execute(t, server.URL, "build", "nginx", "--wait")
	if err == nil || err.Error() != "nginx is an image deployment with no GitHub repository to build; deploy image changes with `edka up nginx --wait`" {
		t.Fatal(err)
	}
	for _, request := range *requests {
		if strings.HasPrefix(request, "POST ") {
			t.Fatalf("requested a build: %s", request)
		}
	}
}

func TestUpWaitShowsRolloutProgress(t *testing.T) {
	fastPolls(t)
	server, _ := sequenceAPI(t, map[string][]string{
		"GET /api/deployments": {`{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`},
		"GET /api/deployments/d1": {
			`{"data":{"id":"d1","github_deployment_id":null}}`,
			`{"data":{"spec_generation":2,"applied_generation":1,"healthy_generation":1,"status":"updating"}}`,
			`{"data":{"spec_generation":2,"applied_generation":1,"healthy_generation":1,"status":"updating","status_message":"Could not reach the cluster: timeout. Checking again in 30 seconds."}}`,
			`{"data":{"spec_generation":2,"applied_generation":2,"healthy_generation":1,"status":"updating"}}`,
			`{"data":{"spec_generation":2,"applied_generation":2,"healthy_generation":1,"status":"updating"}}`,
			`{"data":{"spec_generation":2,"applied_generation":2,"healthy_generation":2,"status":"deployed"}}`,
		},
		"POST /api/deployments/d1/restart": {`{"data":{"id":"d1","generation":2}}`},
		"GET /api/deployments/d1/status": {
			`{"data":{"status":"progressing","replicas":{"desired":2,"updated":1,"ready":1},"pods":[{"name":"api-new","status":"failed","ready":false,"restartCount":1,"reason":"CrashLoopBackOff"}]}}`,
			`{"data":{"status":"progressing","replicas":{"desired":2,"updated":1,"ready":1},"pods":[{"name":"api-new","status":"failed","ready":false,"restartCount":2,"reason":"CrashLoopBackOff"}]}}`,
			`{"data":{"name":"api","status":"deployed","running_image":"nginx:1.31","replicas":{"desired":2,"updated":2,"ready":2,"available":2},"pods":[{"name":"api-new","status":"running","ready":true,"restartCount":2}]}}`,
		},
	})
	out, errOut, err := execute(t, server.URL, "up", "api", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut,
		"Deployment submitted. Waiting for generation 2…\n",
		"Applying generation 2…\n",
		"Could not reach the cluster: timeout. Checking again in 30 seconds.\n",
		"Rolling out: 1/2 updated, 1/2 ready\n",
		"Pod api-new: CrashLoopBackOff, 1 restart\n",
		"Pod api-new: CrashLoopBackOff, 2 restarts\n",
		"Rolling out: 2/2 updated, 2/2 ready\n",
		"✓ api is running generation 2\n",
	)
	if strings.Count(errOut, "Applying") != 1 || strings.Count(errOut, "Could not reach") != 1 {
		t.Fatalf("repeated lines:\n%s", errOut)
	}
	inOrder(t, out, "Status", "deployed", "Image", "nginx:1.31", "Replicas", "2/2 ready")
}

func TestWaitExplainsFailedRollouts(t *testing.T) {
	fastPolls(t)
	for _, tc := range []struct {
		name   string
		routes map[string][]string
		want   string
	}{
		{"failed", map[string][]string{
			"GET /api/deployments/d1": {`{"data":{"spec_generation":2,"applied_generation":2,"healthy_generation":1,"status":"failed","status_message":"Readiness probe failed"}}`},
		}, "generation 2 failed: Readiness probe failed\nFind the cause with `edka diagnose api`"},
		{"rolled back", map[string][]string{
			"GET /api/deployments/d1":             {`{"data":{"spec_generation":3,"applied_generation":2,"healthy_generation":1,"status":"updating"}}`},
			"GET /api/deployments/d1/revisions/2": {`{"data":{"generation":2,"status":"failed","status_message":"OOMKilled"}}`},
		}, "generation 2 failed: OOMKilled\nGeneration 3 replaced it\nFind the cause with `edka diagnose api`"},
		{"replaced", map[string][]string{
			"GET /api/deployments/d1":             {`{"data":{"spec_generation":3,"applied_generation":3,"healthy_generation":3,"status":"deployed"}}`},
			"GET /api/deployments/d1/revisions/2": {`{"data":{"generation":2,"status":"applying"}}`},
		}, "generation 2 was replaced by generation 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EDKA_TOKEN", "test-token")
			server, _ := sequenceAPI(t, tc.routes)
			a := App{current: config.Profile{APIURL: server.URL}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, HTTP: server.Client()}
			_, err := a.waitDeployment(t.Context(), "d1", "api", 2)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestWaitSurvivesFailedReads(t *testing.T) {
	fastPolls(t)
	server, _ := sequenceAPI(t, map[string][]string{
		"GET /api/deployments": {`{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`},
		"GET /api/deployments/d1": {
			`{"data":{"id":"d1","github_deployment_id":null}}`,
			`{"data":{"spec_generation":2,"applied_generation":1,"healthy_generation":1,"status":"updating"}}`,
			`502 <html>Bad Gateway</html>`,
			`502 <html>Bad Gateway</html>`,
			`{"data":{"spec_generation":2,"applied_generation":2,"healthy_generation":2,"status":"deployed"}}`,
		},
		"POST /api/deployments/d1/restart": {`{"data":{"id":"d1","generation":2}}`},
		"GET /api/deployments/d1/status": {
			`503 {"message":"Service unavailable"}`,
			`{"data":{"name":"api","status":"deployed","running_image":"nginx:1.31","replicas":{"desired":2,"updated":2,"ready":2,"available":2}}}`,
		},
	})
	out, errOut, err := execute(t, server.URL, "up", "api", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut,
		"Applying generation 2…\n",
		"Could not read from Edka, trying again: Bad Gateway (HTTP 502)\n",
		"Could not read from Edka, trying again: Service unavailable (HTTP 503)\n",
		"Rolling out: 2/2 updated, 2/2 ready\n",
		"✓ api is running generation 2\n",
	)
	if strings.Count(errOut, "Bad Gateway") != 1 {
		t.Fatalf("repeated lines:\n%s", errOut)
	}
	inOrder(t, out, "Status", "deployed", "Image", "nginx:1.31")
}

func TestWaitStopsWhenReadsKeepFailing(t *testing.T) {
	fastPolls(t)
	retry := pollRetry
	pollRetry = 20 * time.Millisecond
	t.Cleanup(func() { pollRetry = retry })
	t.Setenv("EDKA_TOKEN", "test-token")
	for _, tc := range []struct {
		response, want string
		retried        bool
	}{
		{`503 {"message":"Service unavailable"}`, "Service unavailable (HTTP 503)", true},
		// Reading again can't change an answer such as a missing permission.
		{`403 {"message":"No access"}`, "No access (HTTP 403)", false},
	} {
		server, requests := sequenceAPI(t, map[string][]string{"GET /api/deployments/d1": {tc.response}})
		var errOut bytes.Buffer
		a := App{current: config.Profile{APIURL: server.URL}, Out: &bytes.Buffer{}, Err: &errOut, HTTP: server.Client()}
		_, err := a.waitDeployment(t.Context(), "d1", "api", 2)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatal(err)
		}
		if retried := len(*requests) > 1; retried != tc.retried || strings.Contains(errOut.String(), "trying again") != tc.retried {
			t.Fatalf("%s: %d requests, stderr %q", tc.want, len(*requests), errOut.String())
		}
	}
}

func TestWaitTimeoutDuringRetriesKeepsTheDeadline(t *testing.T) {
	fastPolls(t)
	t.Setenv("EDKA_TOKEN", "test-token")
	server, _ := sequenceAPI(t, map[string][]string{"GET /api/deployments/d1": {`503 {"message":"Service unavailable"}`}})
	a := App{current: config.Profile{APIURL: server.URL}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, HTTP: server.Client()}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := a.waitDeployment(ctx, "d1", "api", 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

// lateFailure answers a request with a 503 once the request's context has
// ended, as when Edka's answer arrives just as a wait times out.
type lateFailure struct{}

func (lateFailure) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"message":"Service unavailable"}`)), Request: r}, nil
}

func TestWaitTimeoutAsAReadFailsKeepsTheDeadline(t *testing.T) {
	fastPolls(t)
	t.Setenv("EDKA_TOKEN", "test-token")
	a := App{current: config.Profile{APIURL: "https://api.example"}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, HTTP: &http.Client{Transport: lateFailure{}}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := a.waitDeployment(ctx, "d1", "api", 2)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "the last read failed: Service unavailable (HTTP 503)") {
		t.Fatal(err)
	}
}
