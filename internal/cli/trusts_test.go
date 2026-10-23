package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const testTrustID = "7d1e4c62-3c4b-4b8e-9b1f-0a4f2c1d9e01"

// actionsJob simulates a GitHub Actions job signing in with a trust: a runner
// that hands out OIDC tokens, and Edka's exchange in front of a step API.
type actionsJob struct {
	mu sync.Mutex
	// audiences are the audiences the runner issued tokens for.
	audiences []string
	// exchanged are the GitHub tokens Edka exchanged, in order.
	exchanged []string
	// bearers are the tokens each API request carried, by route.
	bearers []string
	runner  *httptest.Server
	edka    *httptest.Server
}

// newActionsJob answers exchanges with tokens that last expiresIn seconds and
// sends the other requests to the step API.
func newActionsJob(t *testing.T, steps map[string][]string, expiresIn int) (*actionsJob, *stepAPI) {
	t.Helper()
	backend, api := newStepAPI(t, steps)
	target, _ := url.Parse(backend.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	job := &actionsJob{}
	job.runner = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runner-request-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		job.mu.Lock()
		defer job.mu.Unlock()
		job.audiences = append(job.audiences, r.URL.Query().Get("audience"))
		fmt.Fprintf(w, `{"value":"github-token-%d"}`, len(job.audiences))
	}))
	job.edka = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job.mu.Lock()
		if r.URL.Path == "/api/cli/github-actions/token" {
			defer job.mu.Unlock()
			var body struct{ Trust, Token string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Trust != testTrustID {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"error":"access_denied","reason":"trust","message":"No trust with this ID accepts GitHub Actions tokens."}`)
				return
			}
			job.exchanged = append(job.exchanged, body.Token)
			fmt.Fprintf(w, `{"access_token":"edka_trust_%d","token_type":"Bearer","expires_in":%d,"organization":{"id":"org-1"}}`, len(job.exchanged), expiresIn)
			return
		}
		job.bearers = append(job.bearers, r.Header.Get("Authorization"))
		job.mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(job.runner.Close)
	t.Cleanup(job.edka.Close)
	return job, api
}

// execute runs the CLI in the job, signed in with the trust.
func (job *actionsJob) execute(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("EDKA_CONFIG_DIR", t.TempDir())
	t.Setenv("EDKA_TOKEN", "")
	t.Setenv("EDKA_TRUST", testTrustID)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", job.runner.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-request-token")
	t.Setenv("CI", "true")
	t.Setenv("EDKA_API_URL", job.edka.URL)
	for _, name := range []string{"EDKA_PROFILE", "EDKA_CLUSTER", "EDKA_DEPLOYMENT", "EDKA_ORGANIZATION"} {
		t.Setenv(name, "")
	}
	var out, errOut bytes.Buffer
	root := New("test", strings.NewReader(""), &out, &errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestATrustSignsInOnceForACommandItsTokenOutlasts(t *testing.T) {
	job, api := newActionsJob(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed"), packagedAppDetail("1.3.0", "1.3.0", "configuring"), packagedAppDetail("1.3.0", "1.3.0", "installed")), 900)

	_, errOut, err := job.execute(t, "apps", "update", "memos", "--cluster", "sinaia", "--yes", "--wait")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if api.bodies["PATCH /api/clusters/c1/apps/a1"] != `{"version":"1.3.0"}` {
		t.Fatalf("body %s", api.bodies["PATCH /api/clusters/c1/apps/a1"])
	}
	if len(job.exchanged) != 1 || len(job.audiences) != 1 || job.audiences[0] != job.edka.URL {
		t.Fatalf("exchanged %v for audiences %v", job.exchanged, job.audiences)
	}
	for _, bearer := range job.bearers {
		if bearer != "Bearer edka_trust_1" {
			t.Fatalf("bearers %v", job.bearers)
		}
	}
}

func TestATrustExchangesAgainWhileAWaitOutlastsItsToken(t *testing.T) {
	// A token that lasts a minute is always within two minutes of expiring, so
	// each request needs a new one, as the requests of a long wait do.
	job, _ := newActionsJob(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed"), packagedAppDetail("1.3.0", "1.3.0", "configuring"), packagedAppDetail("1.3.0", "1.3.0", "configuring"), packagedAppDetail("1.3.0", "1.3.0", "installed")), 60)

	_, errOut, err := job.execute(t, "apps", "update", "memos", "--cluster", "sinaia", "--yes", "--wait")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if len(job.exchanged) < 5 {
		t.Fatalf("exchanged %d times for %d requests", len(job.exchanged), len(job.bearers))
	}
	// Every GitHub token was exchanged once, and the last request carried the newest token.
	seen := map[string]bool{}
	for _, token := range job.exchanged {
		if seen[token] {
			t.Fatalf("exchanged %s twice: %v", token, job.exchanged)
		}
		seen[token] = true
	}
	if last := job.bearers[len(job.bearers)-1]; last != fmt.Sprintf("Bearer edka_trust_%d", len(job.exchanged)) {
		t.Fatalf("last request carried %s after %d exchanges", last, len(job.exchanged))
	}
}

func TestATrustReportsARefusalWithoutAskingForALogin(t *testing.T) {
	job, _ := newActionsJob(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed")), 900)
	t.Setenv("EDKA_TRUST", "")

	_, _, err := job.execute(t, "apps", "update", "memos", "--cluster", "sinaia", "--yes", "--trust", "00000000-0000-4000-8000-000000000000")
	if err == nil || !strings.Contains(err.Error(), "No trust with this ID") || strings.Contains(err.Error(), "edka login") {
		t.Fatalf("err %v", err)
	}
}

func TestATrustNeedsAJobThatMayRequestAnOIDCToken(t *testing.T) {
	job, _ := newActionsJob(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed")), 900)

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs([]string{"apps", "list", "--api-url", job.edka.URL, "--trust", testTrustID})
	t.Setenv("EDKA_CONFIG_DIR", t.TempDir())
	t.Setenv("EDKA_TOKEN", "")
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "id-token: write") {
		t.Fatalf("err %v", err)
	}
}

func TestATrustAndATokenDoNotMix(t *testing.T) {
	job, _ := newActionsJob(t, map[string][]string{"GET /api/cli/whoami": {`{"data":{"id":"` + testTrustID + `","name":"jude","trust":{"app":"jude"}}}`}}, 900)
	_, _, err := job.execute(t, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDKA_TOKEN", "test-token")
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs([]string{"whoami"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err %v", err)
	}
}

func TestLoginRefusesWhileATrustIsSet(t *testing.T) {
	t.Setenv("EDKA_TRUST", testTrustID)
	_, _, err := execute(t, "https://api.edka.io", "login", "--no-input")
	if err == nil || !strings.Contains(err.Error(), "unset EDKA_TRUST") {
		t.Fatalf("err %v", err)
	}
}

func TestTrustsCreateGrantsOneAppOnTheNamedClusters(t *testing.T) {
	server, api := newStepAPI(t, map[string][]string{
		"GET /api/clusters": {`{"data":[{"id":"c1","name":"orion"},{"id":"c2","name":"staging"}]}`},
		"POST /api/trusts":  {`{"success":true,"data":{"id":"` + testTrustID + `","name":"jude","repository":"prezly/jude","audience":"https://api.edka.io"}}`},
	})

	_, errOut, err := execute(t, server.URL, "trusts", "create", "jude", "--repository", "prezly/jude", "--ref", "refs/heads/main", "--app", "jude", "--clusters", "orion", "--workflow", ".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(api.bodies["POST /api/trusts"]), &body); err != nil {
		t.Fatal(err)
	}
	grants, _ := json.Marshal(body["grants"])
	if string(grants) != `[{"action":"publish","app":"jude"},{"action":"update","app":"jude","clusters":["c1"]}]` {
		t.Fatalf("grants %s", grants)
	}
	if body["workflow"] != ".github/workflows/release.yml" || fmt.Sprint(body["refs"]) != "[refs/heads/main]" {
		t.Fatalf("body %v", body)
	}
	if !strings.Contains(errOut, "EDKA_TRUST: "+testTrustID) || !strings.Contains(errOut, "id-token: write") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestTrustsCreateNeedsWhereTheJobsRun(t *testing.T) {
	server, api := newStepAPI(t, map[string][]string{})
	_, _, err := execute(t, server.URL, "trusts", "create", "jude", "--repository", "prezly/jude", "--app", "jude")
	if err == nil || !strings.Contains(err.Error(), "--ref or --environment") || api.writes() != 0 {
		t.Fatalf("err %v, requests %v", err, api.requests)
	}
}

func TestTrustsDeleteAsksFirst(t *testing.T) {
	server, api := newStepAPI(t, map[string][]string{
		"GET /api/trusts":                   {`{"data":[{"id":"` + testTrustID + `","name":"jude"}]}`},
		"DELETE /api/trusts/" + testTrustID: {`{"success":true,"data":{"id":"` + testTrustID + `","deleted":true}}`},
	})
	if _, _, err := execute(t, server.URL, "trusts", "delete", "jude"); err == nil || !strings.Contains(err.Error(), "rerun with --yes") || api.writes() != 0 {
		t.Fatalf("err %v, requests %v", err, api.requests)
	}
	if _, _, err := execute(t, server.URL, "trusts", "delete", "jude", "--yes"); err != nil || api.writes() != 1 {
		t.Fatalf("err %v, requests %v", err, api.requests)
	}
}
