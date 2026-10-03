package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/credential"
)

func TestPKCEAndStateValidation(t *testing.T) {
	verifier, err := Random()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if Challenge(verifier) != base64.RawURLEncoding.EncodeToString(sum[:]) || len(verifier) < 43 {
		t.Fatal("invalid challenge")
	}
	results := make(chan callbackResult, 1)
	handler := Callback("state", "https://api.edka.io/api/auth", true, results)
	for _, query := range []string{"state=wrong&code=secret", "state=state&code=secret&iss=https://evil.example", "state=state&code=secret&state=state"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1/callback?"+query, nil))
		if response.Code != 400 {
			t.Errorf("%s got %d", query, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1/callback?state=state&code=ok&iss=https://api.edka.io/api/auth", nil))
	if response.Code != 200 || (<-results).Code != "ok" {
		t.Fatal("valid callback failed")
	}
	if strings.Contains(response.Body.String(), "?code=") {
		t.Fatal("code leaked in HTML")
	}
}

// The callback's CSP allows inline styles only, so every page has to work
// without scripts or fetched resources.
func TestCallbackPages(t *testing.T) {
	for _, tc := range []struct {
		query    string
		taken    bool
		status   int
		title    string
		received bool
	}{
		{query: "state=state&code=ok", status: 200, title: "Authorization received", received: true},
		{query: "state=state&error=access_denied", status: 200, title: "Sign-in cancelled"},
		{query: "state=state&code=ok", taken: true, status: 409, title: "Authorization already received"},
		{query: "state=wrong&code=ok", status: 400, title: "Sign-in request not recognized"},
		{query: "state=state&code=ok&iss=https://evil.example", status: 400, title: "Sign-in rejected"},
		{query: "state=state", status: 400, title: "Sign-in incomplete"},
	} {
		results := make(chan callbackResult, 1)
		if tc.taken {
			results <- callbackResult{Code: "earlier"}
		}
		response := httptest.NewRecorder()
		Callback("state", "https://api.edka.io/api/auth", false, results).ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1/callback?"+tc.query, nil))
		body := response.Body.String()
		if response.Code != tc.status || response.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s: got %d %q", tc.query, response.Code, response.Header().Get("Content-Type"))
		}
		if !strings.Contains(body, `<span class="sr">`+tc.title+`</span>`) {
			t.Errorf("%s: missing title %q", tc.query, tc.title)
		}
		if strings.Contains(body, `class="p `) != tc.received {
			t.Errorf("%s: arrival animation rendered = %v", tc.query, !tc.received)
		}
		if strings.Contains(body, "<script") || strings.Contains(body, "src=") {
			t.Errorf("%s: page needs more than the CSP allows", tc.query)
		}
		if strings.Contains(body, "ZgotmplZ") {
			t.Errorf("%s: html/template rejected a value in the page", tc.query)
		}
	}
}

type loginOutput struct {
	once sync.Once
	urls chan string
}

func (w *loginOutput) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "http") && strings.Contains(s, "/authorize?") {
			w.once.Do(func() { w.urls <- s })
		}
	}
	return len(p), nil
}
func TestCompletePKCELogin(t *testing.T) {
	var base, challenge, redirect string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/config":
			json.NewEncoder(w).Encode(map[string]string{"resource": base + "/api", "discovery_url": base + "/discovery", "console_url": "https://console.edka.io"})
		case "/discovery":
			json.NewEncoder(w).Encode(map[string]any{"issuer": base + "/api/auth", "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "registration_endpoint": base + "/register", "revocation_endpoint": base + "/revoke", "authorization_response_iss_parameter_supported": true})
		case "/register":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["token_endpoint_auth_method"] != "none" || body["application_type"] != "native" {
				t.Error("client must be public native")
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"client_id":"cli-test"}`))
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			mu.Lock()
			if Challenge(r.Form.Get("code_verifier")) != challenge || r.Form.Get("redirect_uri") != redirect || r.Form.Get("code") != "authorized-code" || r.Form.Get("resource") != base+"/api" {
				t.Error("code exchange contract violated")
			}
			mu.Unlock()
			w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":900,"scope":"cli:read"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	output := &loginOutput{urls: make(chan string, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		session *credential.Session
		err     error
	}
	done := make(chan result, 1)
	go func() {
		session, _, err := Login(ctx, LoginOptions{BaseURL: base, Dir: t.TempDir(), Profile: "default", ReadOnly: true, NoBrowser: true, Output: output})
		done <- result{session, err}
	}()
	var address string
	select {
	case address = <-output.urls:
	case <-ctx.Done():
		t.Fatal("no authorization URL")
	}
	authorize, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	q := authorize.Query()
	if q.Get("code_challenge_method") != "S256" || strings.Contains(q.Get("scope"), "cli:write") {
		t.Fatal("incorrect scope or PKCE")
	}
	mu.Lock()
	challenge = q.Get("code_challenge")
	redirect = q.Get("redirect_uri")
	mu.Unlock()
	callback := redirect + "?" + url.Values{"state": {q.Get("state")}, "iss": {base + "/api/auth"}, "code": {"authorized-code"}}.Encode()
	response, err := http.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	value := <-done
	if value.err != nil {
		t.Fatal(value.err)
	}
	if value.session.AccessToken != "access" || value.session.RefreshToken != "refresh" || value.session.APIURL != base {
		t.Fatalf("wrong session: %+v", value.session)
	}
}

// The callback hands Login its result before it writes its page, so the server
// has to send a response it is writing before it closes.
func TestStopServerSendsTheResponseBeingWritten(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "page")
	})}
	go server.Serve(listener)
	type result struct {
		body string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			done <- result{"", err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		done <- result{string(body), err}
	}()
	<-started
	stopServer(server)
	if got := <-done; got.err != nil || got.body != "page" {
		t.Fatal(got.body, got.err)
	}
}
func TestConcurrentRefreshRotatesOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("refresh_token") != "old-refresh" {
			t.Error("wrong refresh token")
		}
		fmt.Fprint(w, `{"access_token":"new","refresh_token":"rotated","token_type":"Bearer","expires_in":900}`)
	}))
	defer server.Close()
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	_, err := store.Save("default", &credential.Session{APIURL: server.URL, TokenEndpoint: server.URL + "/token", ClientID: "client", AccessToken: "old", RefreshToken: "old-refresh", ExpiresAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := Token(context.Background(), store, "default", server.URL, api.HTTPClient(time.Second))
			if err != nil || token != "new" {
				t.Errorf("%q %v", token, err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("refreshed %d times", calls)
	}
	session, err := store.Load("default")
	if err != nil || session.RefreshToken != "rotated" {
		t.Fatal("rotation was not persisted")
	}
	if _, err := Token(context.Background(), store, "default", "https://foreign.example", api.HTTPClient(time.Second)); err == nil {
		t.Fatal("reused credentials at foreign origin")
	}
}
func TestDiscoveryRejectsForeignTokenEndpoint(t *testing.T) {
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/config" {
			fmt.Fprintf(w, `{"resource":%q,"discovery_url":%q}`, base+"/api", base+"/discovery")
		} else {
			fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":"https://foreign.example/token","registration_endpoint":%q}`, base, base+"/authorize", base+"/register")
		}
	}))
	defer server.Close()
	base = server.URL
	if _, _, err := Discover(context.Background(), base, api.HTTPClient(time.Second)); err == nil {
		t.Fatal("trusted a foreign token endpoint")
	}
}

// revocation records the revocation requests a server receives.
type revocation struct {
	mu     sync.Mutex
	forms  []url.Values
	status int
	// location is where the server redirects to, when status is a redirect.
	location string
}

func (r *revocation) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Error(err)
		}
		if req.Method != "POST" || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("revocation sent as %s %s", req.Method, req.Header.Get("Content-Type"))
		}
		r.mu.Lock()
		r.forms = append(r.forms, req.PostForm)
		status, location := r.status, r.location
		r.mu.Unlock()
		if location != "" {
			w.Header().Set("Location", location)
		}
		if status != 0 {
			w.WriteHeader(status)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (r *revocation) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.forms)
}

func TestRevokeSendsTheRefreshTokenToItsOwnOrigin(t *testing.T) {
	var received revocation
	server := received.server(t)
	session := &credential.Session{APIURL: server.URL, RevocationEndpoint: server.URL + "/revoke", ClientID: "cli-test", AccessToken: "access", RefreshToken: "refresh"}
	if err := Revoke(context.Background(), api.HTTPClient(time.Second), session); err != nil {
		t.Fatal(err)
	}
	if received.count() != 1 {
		t.Fatal(received.forms)
	}
	// The refresh token is what keeps access alive, so it is the one revoked.
	form := received.forms[0]
	if form.Get("token") != "refresh" || form.Get("token_type_hint") != "refresh_token" || form.Get("client_id") != "cli-test" || len(form) != 3 {
		t.Fatal(form)
	}

	// Without a refresh token or a revocation endpoint there is nothing to revoke.
	for _, session := range []*credential.Session{
		{APIURL: server.URL, RevocationEndpoint: server.URL + "/revoke", AccessToken: "access"},
		{APIURL: server.URL, RefreshToken: "refresh"},
	} {
		if err := Revoke(context.Background(), api.HTTPClient(time.Second), session); err != nil || received.count() != 1 {
			t.Fatal(err, received.forms)
		}
	}
}

// A saved session names its revocation endpoint. One that names another origin
// would send the refresh token there.
func TestRevokeRefusesAnEndpointOnAnotherOrigin(t *testing.T) {
	var received revocation
	foreign := received.server(t)
	for _, base := range []string{"https://api.edka.io", "http://127.0.0.1:1"} {
		session := &credential.Session{APIURL: base, RevocationEndpoint: foreign.URL + "/revoke", ClientID: "cli-test", RefreshToken: "refresh"}
		if err := Revoke(context.Background(), api.HTTPClient(time.Second), session); err == nil || !strings.Contains(err.Error(), "outside the configured API origin") {
			t.Fatal(base, err)
		}
	}
	if received.count() != 0 {
		t.Fatalf("the refresh token left for another origin: %v", received.forms)
	}
}

func TestRevokeReportsAServerThatRefuses(t *testing.T) {
	received := revocation{status: 503}
	server := received.server(t)
	session := &credential.Session{APIURL: server.URL, RevocationEndpoint: server.URL + "/revoke", ClientID: "cli-test", RefreshToken: "refresh"}
	err := Revoke(context.Background(), api.HTTPClient(time.Second), session)
	if err == nil || !strings.Contains(err.Error(), "revoke access (HTTP 503); retry logout or use --local") {
		t.Fatal(err)
	}
	// A server that can't be reached is an error too, so logout keeps the
	// credentials it could not revoke.
	server.Close()
	if err := Revoke(context.Background(), api.HTTPClient(time.Second), session); err == nil {
		t.Fatal("revoked against a closed server")
	}
	// A redirect is not followed, so the token does not reach where it points.
	var elsewhere revocation
	redirected := revocation{status: 307, location: elsewhere.server(t).URL + "/revoke"}
	moved := redirected.server(t)
	session = &credential.Session{APIURL: moved.URL, RevocationEndpoint: moved.URL + "/revoke", ClientID: "cli-test", RefreshToken: "refresh"}
	if err := Revoke(context.Background(), api.HTTPClient(time.Second), session); err == nil || redirected.count() != 1 || elsewhere.count() != 0 {
		t.Fatal(err, redirected.forms, elsewhere.forms)
	}
}

func TestExchangeRejectsWhatIsNotABearerToken(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"description", `{"error":"invalid_grant","error_description":"Refresh token expired"}`, 400, "OAuth: Refresh token expired; run `edka login` to authorize again"},
		{"error code", `{"error":"invalid_grant"}`, 400, "OAuth: invalid_grant; run `edka login`"},
		{"status text", `<html>bad gateway</html>`, 502, "OAuth: Bad Gateway; run `edka login`"},
		{"not JSON", `<html>ok</html>`, 200, "invalid character"},
		{"no access token", `{"token_type":"Bearer","expires_in":900}`, 200, "invalid token response"},
		{"another token type", `{"access_token":"access","token_type":"mac","expires_in":900}`, 200, "invalid token response"},
		{"no lifetime", `{"access_token":"access","token_type":"Bearer"}`, 200, "invalid token response"},
		{"negative lifetime", `{"access_token":"access","token_type":"Bearer","expires_in":-5}`, 200, "invalid token response"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(test.status)
			fmt.Fprint(w, test.body)
		}))
		token, err := exchange(context.Background(), api.HTTPClient(time.Second), server.URL+"/token", url.Values{"grant_type": {"refresh_token"}})
		server.Close()
		if err == nil || token != nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: %v %v, want %q", test.name, token, err, test.want)
		}
	}
	// The token type is compared without regard to case, as RFC 6749 has it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Error("exchange did not ask for JSON")
		}
		fmt.Fprint(w, `{"access_token":"access","token_type":"bearer","expires_in":900}`)
	}))
	defer server.Close()
	if token, err := exchange(context.Background(), api.HTTPClient(time.Second), server.URL+"/token", url.Values{}); err != nil || token.AccessToken != "access" {
		t.Fatal(token, err)
	}
}

func TestTokenUsesAStoredTokenUntilItNearsExpiry(t *testing.T) {
	requests := 0
	var mu sync.Mutex
	status, body := 200, `{"access_token":"new","token_type":"Bearer","expires_in":900,"scope":"cli:read cli:write"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := api.HTTPClient(time.Second)
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	save := func(session credential.Session) {
		t.Helper()
		session.APIURL, session.TokenEndpoint, session.ClientID = server.URL, server.URL+"/token", "cli-test"
		if _, err := store.Save("default", &session); err != nil {
			t.Fatal(err)
		}
	}

	// A token with more than a minute left is used as it is.
	save(credential.Session{AccessToken: "current", RefreshToken: "refresh", ExpiresAt: time.Now().Add(5 * time.Minute)})
	if token, err := Token(context.Background(), store, "default", server.URL, client); err != nil || token != "current" || requests != 0 {
		t.Fatal(token, err, requests)
	}
	// One with less is refreshed. A response without a refresh token keeps the
	// stored one, and its scope replaces the stored scope.
	save(credential.Session{AccessToken: "stale", RefreshToken: "refresh", Scope: "cli:read", ExpiresAt: time.Now().Add(30 * time.Second)})
	if token, err := Token(context.Background(), store, "default", server.URL, client); err != nil || token != "new" || requests != 1 {
		t.Fatal(token, err, requests)
	}
	session, err := store.Load("default")
	if err != nil || session.AccessToken != "new" || session.RefreshToken != "refresh" || session.Scope != "cli:read cli:write" || time.Until(session.ExpiresAt) < 14*time.Minute {
		t.Fatalf("%+v %v", session, err)
	}

	// An expired token without a refresh token means signing in again.
	save(credential.Session{AccessToken: "stale", ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := Token(context.Background(), store, "default", server.URL, client); !errors.Is(err, credential.ErrNotFound) || requests != 1 {
		t.Fatal(err, requests)
	}

	// A refresh the server refuses leaves the stored session as it was.
	mu.Lock()
	status, body = 400, `{"error":"invalid_grant","error_description":"Refresh token revoked"}`
	mu.Unlock()
	save(credential.Session{AccessToken: "stale", RefreshToken: "revoked", ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := Token(context.Background(), store, "default", server.URL, client); err == nil || !strings.Contains(err.Error(), "Refresh token revoked") {
		t.Fatal(err)
	}
	if session, err := store.Load("default"); err != nil || session.AccessToken != "stale" || session.RefreshToken != "revoked" {
		t.Fatalf("%+v %v", session, err)
	}
}

// A saved session names its token endpoint. One on another origin would get
// the refresh token.
func TestTokenRefusesATokenEndpointOnAnotherOrigin(t *testing.T) {
	var received revocation
	foreign := received.server(t)
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	base := "http://127.0.0.1:1"
	if _, err := store.Save("default", &credential.Session{APIURL: base, TokenEndpoint: foreign.URL + "/token", ClientID: "cli-test", AccessToken: "stale", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Token(context.Background(), store, "default", base, api.HTTPClient(time.Second)); err == nil || !strings.Contains(err.Error(), "outside the configured API origin") || received.count() != 0 {
		t.Fatal(err, received.forms)
	}
	if _, err := Token(context.Background(), store, "../escape", base, api.HTTPClient(time.Second)); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
		t.Fatal(err)
	}
	if _, err := Token(context.Background(), store, "work", base, api.HTTPClient(time.Second)); !errors.Is(err, credential.ErrNotFound) {
		t.Fatal(err)
	}
}
