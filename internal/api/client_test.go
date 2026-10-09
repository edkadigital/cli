package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOriginValidation(t *testing.T) {
	for _, raw := range []string{"https://api.edka.io", "http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080"} {
		if _, err := NormalizeBase(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://evil.example", "https://token@api.edka.io", "https://api.edka.io/path", "https://api.edka.io?token=x", "file:///tmp/x"} {
		if _, err := NormalizeBase(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
func TestRedirectCannotCarryToken(t *testing.T) {
	calls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("followed foreign redirect") }))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, foreign.URL, http.StatusFound) }))
	defer origin.Close()
	c := Client{BaseURL: origin.URL, Token: "secret"}
	_, err := c.Do(context.Background(), "GET", "/api/clusters", nil, nil)
	if err == nil || calls != 0 {
		t.Fatalf("redirect err=%v calls=%d", err, calls)
	}
}
func TestPathStaysUnderAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("sent the token to %s", r.URL.Path)
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL, Token: "secret"}
	for _, path := range []string{"/api/../oauth2/token", "/api/%2e%2e/oauth2/token", "/api/clusters/%2E%2E%2F..%2Foauth2", "/api/clusters/./c1", `/api/..\oauth2/token`, "/oauth2/token", "//evil.example/api/"} {
		if _, err := c.Do(context.Background(), "GET", path, nil, nil); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
}
func TestRequestContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/clusters" || r.URL.Query().Get("name") != "hello & goodbye" || r.Header.Get("Authorization") != "Bearer scoped" || r.Header.Get("X-Organization-ID") != "org" {
			t.Errorf("wrong request: %v", r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL, Token: "scoped", Organization: "org"}
	response, err := c.Do(context.Background(), "GET", "/api/clusters", url.Values{"name": {"hello & goodbye"}}, nil)
	if err != nil || !strings.Contains(string(response.Body), "data") {
		t.Fatal(err)
	}
}
func TestTransientFailures(t *testing.T) {
	var status atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) }))
	c := Client{BaseURL: server.URL}
	for code, want := range map[int]bool{408: true, 429: true, 500: true, 501: true, 502: true, 503: true, 504: true, 522: true, 400: false, 401: false, 403: false, 404: false, 409: false} {
		status.Store(int32(code))
		if _, err := c.Do(context.Background(), "GET", "/api/clusters", nil, nil); Transient(err) != want {
			t.Errorf("HTTP %d: transient=%v (%v)", code, !want, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, "GET", "/api/clusters", nil, nil); err == nil || Transient(err) {
		t.Errorf("a cancelled request: %v", err)
	}
	server.Close()
	if _, err := c.Do(context.Background(), "GET", "/api/clusters", nil, nil); !Transient(err) {
		t.Errorf("an unreachable server: %v", err)
	}
	if Transient(nil) || Transient(errors.New("response exceeds 32 MiB; narrow your query")) {
		t.Error("no failure, or one that reading again can't change")
	}
}

// An error keeps the body's code, reason, details and invalid fields, and
// nothing else of it.
func TestErrorKeepsValidationDetail(t *testing.T) {
	body := `{"error":"Invalid configuration","message":"Invalid app configuration","code":"invalid_configuration","details":"` + strings.Repeat("d", 600) + `","fields":[{"field":"database_password","message":"Required","value":"hunter2"},{"message":"no field"},"text",{"field":"replicas","message":"Too small"}],"stack":"at handler.ts:1"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "request-1")
		w.WriteHeader(400)
		w.Write([]byte(body))
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL}
	_, err := c.Do(context.Background(), "POST", "/api/clusters/c1/apps", nil, []byte(`{}`))
	var e *Error
	if !errors.As(err, &e) || e.Code != "invalid_configuration" || e.Reason != "Invalid configuration" || e.Message != "Invalid app configuration" {
		t.Fatalf("%#v", err)
	}
	if len(e.Fields) != 2 || e.Fields[0] != (FieldError{"database_password", "Required"}) || e.Fields[1] != (FieldError{"replicas", "Too small"}) {
		t.Fatalf("%#v", e.Fields)
	}
	if len([]rune(e.Details)) != maxErrorDetails || !strings.HasSuffix(e.Details, "…") {
		t.Fatalf("%d characters of details", len([]rune(e.Details)))
	}
	text := e.Error()
	if !strings.HasPrefix(text, "Invalid app configuration (HTTP 400)\nddd") || !strings.Contains(text, "…\n  database_password: Required\n  replicas: Too small\nRequest ID: request-1") || strings.Contains(text, "hunter2") || strings.Contains(text, "handler.ts") {
		t.Fatal(text)
	}
	// A cut keeps whole characters, and a text of the limit's length is kept.
	for _, tc := range []struct {
		text string
		n    int
		want string
	}{{"héllo", 5, "héllo"}, {"héllo!", 5, "héll…"}, {"日本語のテキスト", 4, "日本語…"}, {"", 3, ""}} {
		if got := clip(tc.text, tc.n); got != tc.want {
			t.Errorf("clip(%q, %d) = %q", tc.text, tc.n, got)
		}
	}
	// Details that only repeat the message are not printed twice.
	e = &Error{Status: 400, Message: "Validation error: name is required", Details: "name is required"}
	if e.Error() != "Validation error: name is required (HTTP 400)" {
		t.Fatal(e.Error())
	}
}

// The secret store and synced secret routes send "fields" as an object of
// messages by field. Those fields are kept too, in name order.
func TestErrorKeepsFieldsByName(t *testing.T) {
	body := `{"error":"Check the highlighted fields","message":"Check the highlighted fields","fields":{"store":"Allowed in cs-troi","namespace":"This namespace does not exist","source.mode":["not","text"],"":"no field"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(body))
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL}
	_, err := c.Do(context.Background(), "POST", "/api/clusters/c1/synced-secrets", nil, []byte(`{}`))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("%#v", err)
	}
	if len(e.Fields) != 2 || e.Fields[0] != (FieldError{"namespace", "This namespace does not exist"}) || e.Fields[1] != (FieldError{"store", "Allowed in cs-troi"}) {
		t.Fatalf("%#v", e.Fields)
	}
	if text := e.Error(); text != "Check the highlighted fields (HTTP 400)\n  namespace: This namespace does not exist\n  store: Allowed in cs-troi" {
		t.Fatal(text)
	}
}

func TestCancellationAndAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "request-1")
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"No access"}`))
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL}
	_, err := c.Do(context.Background(), "DELETE", "/api/clusters/id", nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.RequestID != "request-1" || e.Status != 403 {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	_, err = c.Do(ctx, "GET", "/api/clusters", nil, nil)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}
