package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/api"
)

const (
	verifyURL     = "https://console.example/accounts/acme/cli/verify/s1"
	needsPasskey  = `428 {"error":"step_up_required","message":"Recent identity verification is required.","operation":"deployments.restart"}`
	checkStarted  = `{"data":{"id":"s1","status":"pending","verify_url":"` + verifyURL + `","expires_at":"2026-10-09T12:05:00Z"}}`
	checkPending  = `{"data":{"id":"s1","status":"pending","expires_at":"2026-10-09T12:05:00Z"}}`
	checkApproved = `{"data":{"id":"s1","status":"approved","expires_at":"2026-10-09T12:06:00Z"}}`
	checkDenied   = `{"data":{"id":"s1","status":"denied","expires_at":"2026-10-09T12:05:00Z"}}`
	restartRoute  = "POST /api/deployments/d1/restart"
	restartBody   = `{"reason":"passkey test"}`
)

// sent is a request a passkeyAPI received.
type sent struct{ route, body string }

// passkeyAPI answers each route with its responses in turn, repeating the
// last one, and records every request with its body.
func passkeyAPI(t *testing.T, routes map[string][]string) (*httptest.Server, func() []sent) {
	t.Helper()
	fastPolls(t)
	var mu sync.Mutex
	requests := []sent{}
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		route := r.Method + " " + r.URL.Path
		mu.Lock()
		requests = append(requests, sent{route, string(body)})
		responses, ok := routes[route]
		response := ""
		if ok {
			response = responses[min(calls[route], len(responses)-1)]
			calls[route]++
		}
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
			return
		}
		var code int
		if n, _ := fmt.Sscanf(response, "%d ", &code); n == 1 && code >= 400 {
			w.WriteHeader(code)
			response = response[4:]
		}
		fmt.Fprint(w, response)
	}))
	t.Cleanup(server.Close)
	return server, func() []sent {
		mu.Lock()
		defer mu.Unlock()
		return append([]sent(nil), requests...)
	}
}

func routesOf(requests []sent) string {
	routes := make([]string, len(requests))
	for i, r := range requests {
		routes[i] = r.route
	}
	return strings.Join(routes, "\n")
}

// atTerminal makes commands behave as at a terminal, where someone can confirm
// a passkey check while they wait.
func atTerminal(t *testing.T) {
	t.Helper()
	previous := interactive
	interactive = func(*App) bool { return true }
	t.Cleanup(func() { interactive = previous })
}

// browser records the addresses commands open instead of opening them.
func browser(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	opened := []string{}
	previous := openBrowser
	openBrowser = func(address string) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, address)
		return nil
	}
	t.Cleanup(func() { openBrowser = previous })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), opened...)
	}
}

func restart(t *testing.T, base string) (string, string, error) {
	t.Helper()
	return execute(t, base, "api", "post", "/api/deployments/d1/restart", "--data", restartBody, "--yes", "--json")
}

// At a terminal, a request that needs a passkey check opens the check, waits
// until the user confirms it, and sends the same request again.
func TestPasskeyCheckConfirmedSendsTheRequestAgain(t *testing.T) {
	atTerminal(t)
	opened := browser(t)
	server, requests := passkeyAPI(t, map[string][]string{
		restartRoute:              {needsPasskey, `{"data":{"restarted":true}}`},
		"POST /api/cli/step-up":   {checkStarted},
		"GET /api/cli/step-up/s1": {checkPending, checkApproved},
	})
	out, errOut, err := restart(t, server.URL)
	if err != nil {
		t.Fatal(err, errOut)
	}
	if !strings.Contains(out, `"restarted": true`) {
		t.Fatalf("stdout %q", out)
	}
	got := requests()
	want := strings.Join([]string{restartRoute, "POST /api/cli/step-up", "GET /api/cli/step-up/s1", "GET /api/cli/step-up/s1", restartRoute}, "\n")
	if routesOf(got) != want {
		t.Fatalf("requests:\n%s", routesOf(got))
	}
	if got[0].body != restartBody || got[4].body != restartBody {
		t.Fatalf("sent %q, then %q", got[0].body, got[4].body)
	}
	var check map[string]any
	if json.Unmarshal([]byte(got[1].body), &check) != nil || check["operation"] != "deployments.restart" || len(check) != 1 {
		t.Fatalf("started the check with %q", got[1].body)
	}
	if browsed := opened(); len(browsed) != 1 || browsed[0] != verifyURL {
		t.Fatalf("opened %q", browsed)
	}
	inOrder(t, errOut, "Confirm with your passkey in the Edka console: "+verifyURL+"\n", "✓ Verified\n")
}

// A check the user declines, one that expires, and one nobody confirms in time
// end the command, and the request is not sent again.
func TestPasskeyCheckThatEndsUnconfirmed(t *testing.T) {
	atTerminal(t)
	browser(t)
	for _, tc := range []struct {
		name  string
		reads []string
		want  string
	}{
		{"declined", []string{checkPending, checkDenied}, "Verification was declined in the console."},
		{"expired", []string{checkPending, `404 {"error":"Not found"}`}, "Verification expired. Run the command again."},
		{"expired status", []string{`{"data":{"id":"s1","status":"expired"}}`}, "Verification expired. Run the command again."},
		{"not confirmed in time", []string{checkPending}, "Verification expired. Run the command again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait := verificationWait
			verificationWait = 50 * time.Millisecond
			t.Cleanup(func() { verificationWait = wait })
			server, requests := passkeyAPI(t, map[string][]string{
				restartRoute:              {needsPasskey, `{"data":{"restarted":true}}`},
				"POST /api/cli/step-up":   {checkStarted},
				"GET /api/cli/step-up/s1": tc.reads,
			})
			out, _, err := restart(t, server.URL)
			if err == nil || err.Error() != tc.want || out != "" {
				t.Fatalf("err %v, stdout %q", err, out)
			}
			if n := strings.Count(routesOf(requests()), restartRoute); n != 1 {
				t.Fatalf("sent the request %d times", n)
			}
		})
	}
}

// Edka still refusing the request after the user confirmed ends the command
// with that answer, without another check.
func TestPasskeyCheckRunsOnce(t *testing.T) {
	atTerminal(t)
	browser(t)
	server, requests := passkeyAPI(t, map[string][]string{
		restartRoute:              {needsPasskey},
		"POST /api/cli/step-up":   {checkStarted},
		"GET /api/cli/step-up/s1": {checkApproved},
	})
	_, _, err := restart(t, server.URL)
	var apiError *api.Error
	if !errors.As(err, &apiError) || apiError.Status != http.StatusPreconditionRequired || !strings.Contains(err.Error(), "run `edka verify`") {
		t.Fatalf("err %v", err)
	}
	want := strings.Join([]string{restartRoute, "POST /api/cli/step-up", "GET /api/cli/step-up/s1", restartRoute}, "\n")
	if got := routesOf(requests()); got != want {
		t.Fatalf("requests:\n%s", got)
	}
}

// Without a terminal nobody may be there to confirm, so the command starts the
// check, fails with its address and does not wait.
func TestPasskeyCheckWithoutATerminal(t *testing.T) {
	opened := browser(t)
	server, requests := passkeyAPI(t, map[string][]string{
		restartRoute:              {needsPasskey},
		"POST /api/cli/step-up":   {checkStarted},
		"GET /api/cli/step-up/s1": {checkApproved},
	})
	_, _, err := restart(t, server.URL)
	var stepUp *StepUpError
	var apiError *api.Error
	if !errors.As(err, &stepUp) || stepUp.URL != verifyURL || !errors.As(err, &apiError) || apiError.Status != http.StatusPreconditionRequired {
		t.Fatalf("err %v", err)
	}
	if want := "This action needs a passkey check. Approve it at " + verifyURL + ", then run the command again within 5 minutes. To approve first, run `edka verify`."; err.Error() != want {
		t.Fatalf("err %q", err)
	}
	if got := routesOf(requests()); got != restartRoute+"\nPOST /api/cli/step-up" {
		t.Fatalf("requests:\n%s", got)
	}
	if len(opened()) != 0 {
		t.Fatal("opened a browser without a terminal")
	}
}

// A check Edka does not start leaves Edka's own answer, which names `edka verify`.
func TestPasskeyCheckThatDoesNotStart(t *testing.T) {
	for _, started := range []string{`429 {"error":"Too many requests"}`, `{"data":{"id":"s1","status":"pending","verify_url":"javascript:alert(1)"}}`} {
		server, _ := passkeyAPI(t, map[string][]string{
			restartRoute:            {needsPasskey},
			"POST /api/cli/step-up": {started},
		})
		_, _, err := restart(t, server.URL)
		var apiError *api.Error
		if !errors.As(err, &apiError) || apiError.Status != http.StatusPreconditionRequired || !strings.Contains(err.Error(), "Recent identity verification is required. (HTTP 428)\nConfirm with your passkey: run `edka verify`") {
			t.Fatalf("err %v", err)
		}
	}
}

func TestVerify(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		browsed int
		stdout  string
	}{
		{"opens the browser", nil, 1, ""},
		{"prints the address only", []string{"--no-browser"}, 0, ""},
		{"json", []string{"--json"}, 1, `{"expires_at":"2026-10-09T12:06:00Z","verified":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := browser(t)
			server, requests := passkeyAPI(t, map[string][]string{
				"POST /api/cli/step-up":   {checkStarted},
				"GET /api/cli/step-up/s1": {checkPending, checkPending, checkApproved},
			})
			// verify waits without a terminal: it runs because someone asked for the check.
			out, errOut, err := execute(t, server.URL, append([]string{"verify"}, tc.args...)...)
			if err != nil {
				t.Fatal(err, errOut)
			}
			if tc.stdout == "" && out != "" {
				t.Fatalf("stdout %q", out)
			}
			if tc.stdout != "" {
				var got, want any
				if json.Unmarshal([]byte(out), &got) != nil || json.Unmarshal([]byte(tc.stdout), &want) != nil || fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("stdout %q", out)
				}
			}
			got := requests()
			if len(got) != 4 || got[0].route != "POST /api/cli/step-up" || got[0].body != "{}" {
				t.Fatalf("requests %v", got)
			}
			if len(opened()) != tc.browsed {
				t.Fatalf("opened %q", opened())
			}
			inOrder(t, errOut, "Confirm with your passkey in the Edka console: "+verifyURL+"\n", "✓ Verified. Sensitive actions from this login work for 5 minutes.\n")
		})
	}
	server, _ := passkeyAPI(t, map[string][]string{
		"POST /api/cli/step-up":   {checkStarted},
		"GET /api/cli/step-up/s1": {checkDenied},
	})
	browser(t)
	if _, _, err := execute(t, server.URL, "verify"); err == nil || err.Error() != "Verification was declined in the console." {
		t.Fatalf("err %v", err)
	}
}
