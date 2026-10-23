package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/api"
)

const testTrust = "7d1e4c62-3c4b-4b8e-9b1f-0a4f2c1d9e01"

// runner stands in for the GitHub Actions runner that hands out OIDC tokens.
type runner struct {
	mu        sync.Mutex
	audiences []string
	server    *httptest.Server
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	r := &runner{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer runner-request-token" || req.URL.Query().Get("api-version") != "2.0" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.mu.Lock()
		r.audiences = append(r.audiences, req.URL.Query().Get("audience"))
		n := len(r.audiences)
		r.mu.Unlock()
		fmt.Fprintf(w, `{"count":1,"value":"github-token-%d"}`, n)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *runner) env(name string) string {
	switch name {
	case ActionsRequestURL:
		return r.server.URL + "/token?api-version=2.0"
	case ActionsRequestToken:
		return "runner-request-token"
	}
	return ""
}

// edka stands in for the exchange endpoint.
type edka struct {
	mu       sync.Mutex
	exchange []string
	answer   string
	server   *httptest.Server
}

func newEdka(t *testing.T, expiresIn int) *edka {
	t.Helper()
	e := &edka{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/cli/github-actions/token" || req.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct{ Trust, Token string }
		_ = json.NewDecoder(req.Body).Decode(&body)
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.answer != "" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, e.answer)
			return
		}
		e.exchange = append(e.exchange, body.Trust+" "+body.Token)
		fmt.Fprintf(w, `{"access_token":"edka_trust_%d","token_type":"Bearer","expires_in":%d}`, len(e.exchange), expiresIn)
	}))
	t.Cleanup(e.server.Close)
	return e
}

func TestTrustExchangesAGithubTokenForTheAPIOrigin(t *testing.T) {
	github, server := newRunner(t), newEdka(t, 900)
	trust, err := NewTrust(testTrust, server.server.URL, "test", api.HTTPClient(5*time.Second), github.env)
	if err != nil {
		t.Fatal(err)
	}
	token, err := trust.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "edka_trust_1" {
		t.Fatalf("token %q", token)
	}
	if len(github.audiences) != 1 || github.audiences[0] != server.server.URL {
		t.Fatalf("audiences %v, want %s", github.audiences, server.server.URL)
	}
	if server.exchange[0] != testTrust+" github-token-1" {
		t.Fatalf("exchange %v", server.exchange)
	}
}

func TestTrustExchangesAgainBeforeTheTokenExpires(t *testing.T) {
	github, server := newRunner(t), newEdka(t, 900)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	trust, err := NewTrust(testTrust, server.server.URL, "test", api.HTTPClient(5*time.Second), github.env)
	if err != nil {
		t.Fatal(err)
	}
	trust.Now = func() time.Time { return now }
	ctx := context.Background()
	for _, step := range []struct {
		after time.Duration
		want  string
	}{
		{0, "edka_trust_1"},
		{10 * time.Minute, "edka_trust_1"},
		// Two minutes before the end of 15, the next request gets a new one.
		{3*time.Minute + time.Second, "edka_trust_2"},
		{12 * time.Minute, "edka_trust_2"},
		{2 * time.Minute, "edka_trust_3"},
	} {
		now = now.Add(step.after)
		token, err := trust.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if token != step.want {
			t.Fatalf("after %s: token %q, want %q", step.after, token, step.want)
		}
	}
	// Each exchange used a GitHub token of its own.
	if len(github.audiences) != 3 || server.exchange[2] != testTrust+" github-token-3" {
		t.Fatalf("audiences %v, exchanges %v", github.audiences, server.exchange)
	}
}

func TestTrustNeedsAnActionsJobThatMayRequestATokenAndATrustID(t *testing.T) {
	github := newRunner(t)
	if _, err := NewTrust(testTrust, "https://api.edka.io", "test", nil, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "id-token: write") {
		t.Fatalf("err %v", err)
	}
	if _, err := NewTrust("releases", "https://api.edka.io", "test", nil, github.env); err == nil || !strings.Contains(err.Error(), "ID of a trust") {
		t.Fatalf("err %v", err)
	}
}

func TestTrustSendsTheRequestTokenOnlyOverHTTPS(t *testing.T) {
	trust, err := NewTrust(testTrust, "https://api.edka.io", "test", nil, func(name string) string {
		if name == ActionsRequestURL {
			return "http://runner.example.com/token?api-version=2.0"
		}
		return "runner-request-token"
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trust.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("err %v", err)
	}
}

func TestTrustFollowsNoRedirectWithTheRequestToken(t *testing.T) {
	leaked := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		leaked = req.Header.Get("Authorization") != ""
	}))
	defer elsewhere.Close()
	redirecting := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusFound))
	defer redirecting.Close()
	trust, err := NewTrust(testTrust, "https://api.edka.io", "test", api.HTTPClient(5*time.Second), func(name string) string {
		if name == ActionsRequestURL {
			return redirecting.URL + "/token"
		}
		return "runner-request-token"
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trust.Token(context.Background()); err == nil || leaked {
		t.Fatalf("err %v, leaked %v", err, leaked)
	}
}

func TestTrustReportsWhyEdkaRefusedTheToken(t *testing.T) {
	github, server := newRunner(t), newEdka(t, 900)
	server.answer = `{"error":"access_denied","reason":"ref","message":"This trust doesn't accept jobs on refs/heads/feature."}`
	trust, err := NewTrust(testTrust, server.server.URL, "test", api.HTTPClient(5*time.Second), github.env)
	if err != nil {
		t.Fatal(err)
	}
	_, err = trust.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "doesn't accept jobs on refs/heads/feature") || !strings.Contains(err.Error(), "compares the job's repository") || strings.Contains(err.Error(), "edka login") {
		t.Fatalf("err %v", err)
	}
}

func TestAudienceIsTheOriginAsEdkaWritesIt(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.edka.io":          "https://api.edka.io",
		"https://API.Edka.io:443":      "https://api.edka.io",
		"http://localhost:8080":        "http://localhost:8080",
		"https://api.example.com:8443": "https://api.example.com:8443",
	} {
		got, err := Audience(base)
		if err != nil || got != want {
			t.Errorf("Audience(%q) = %q, %v; want %q", base, got, err, want)
		}
	}
}
