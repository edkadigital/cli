// Package auth implements browser OAuth with PKCE and rotating refresh tokens.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/config"
	"github.com/edkadigital/cli/internal/credential"
	"github.com/edkadigital/cli/internal/devicekey"
	"github.com/gofrs/flock"
)

type Metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
	IssRequired           bool   `json:"authorization_response_iss_parameter_supported"`
}
type ServerConfig struct {
	Resource     string `json:"resource"`
	DiscoveryURL string `json:"discovery_url"`
	ConsoleURL   string `json:"console_url"`
	// DPoPAlgorithms are the proof algorithms the API accepts. ES256 there
	// means it binds tokens to the key that proves them (RFC 9449).
	DPoPAlgorithms []string `json:"dpop_signing_alg_values_supported"`
}

// BindsTokens reports whether the API binds tokens to a device key.
func (s ServerConfig) BindsTokens() bool {
	for _, algorithm := range s.DPoPAlgorithms {
		if algorithm == "ES256" {
			return true
		}
	}
	return false
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}
type LoginOptions struct {
	BaseURL   string
	Profile   string
	Dir       string
	ReadOnly  bool
	NoBrowser bool
	Port      int
	Output    io.Writer
	HTTP      *http.Client
}

func Random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// exchange sends a token request. With a key, it carries a proof of the key,
// and the tokens the server answers are bound to it.
func exchange(ctx context.Context, client *http.Client, endpoint string, values url.Values, key *devicekey.Key) (*tokenResponse, error) {
	post := func() (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(values.Encode()))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		if key != nil {
			proof, err := key.Proof("POST", endpoint, "")
			if err != nil {
				return nil, nil, err
			}
			req.Header.Set("DPoP", proof)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("OAuth request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := api.ReadBody(resp.Body)
		return resp, data, err
	}
	resp, data, err := post()
	if err != nil {
		return nil, err
	}
	var e struct {
		Description string `json:"error_description"`
		Error       string `json:"error"`
	}
	refusedProof := func() bool {
		e.Error, e.Description = "", ""
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode == http.StatusBadRequest && e.Error == "invalid_dpop_proof"
	}
	// A proof dated ahead of the server's clock is refused; send one dated by
	// the server's clock once.
	if key != nil && refusedProof() && key.SyncClock(resp.Header) {
		if resp, data, err = post(); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e.Error, e.Description = "", ""
		_ = json.Unmarshal(data, &e)
		message := e.Description
		if message == "" {
			message = e.Error
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("OAuth: %s; run `edka login` to authorize again", message)
	}
	var token tokenResponse
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	bound := key != nil && strings.EqualFold(token.TokenType, "DPoP")
	if token.AccessToken == "" || !bound && !strings.EqualFold(token.TokenType, "Bearer") || token.ExpiresIn <= 0 {
		return nil, errors.New("authorization server returned an invalid token response")
	}
	return &token, nil
}

// bound reports whether a token response bound its tokens to the key of the
// request.
func (t *tokenResponse) bound() bool { return strings.EqualFold(t.TokenType, "DPoP") }
func Discover(ctx context.Context, base string, client *http.Client) (*ServerConfig, *Metadata, error) {
	c := api.Client{BaseURL: base, HTTP: client}
	response, err := c.Do(ctx, "GET", "/api/cli/config", nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("CLI access unavailable: %w", err)
	}
	var server ServerConfig
	if err := json.Unmarshal(response.Body, &server); err != nil {
		return nil, nil, err
	}
	if server.Resource != base+"/api" {
		return nil, nil, errors.New("unexpected CLI resource identifier")
	}
	if err := api.SameOrigin(base, server.DiscoveryURL); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", server.DiscoveryURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("OAuth discovery failed (HTTP %d)", resp.StatusCode)
	}
	data, err := api.ReadBody(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, nil, err
	}
	for _, endpoint := range []string{meta.Issuer, meta.AuthorizationEndpoint, meta.TokenEndpoint, meta.RegistrationEndpoint} {
		if err := api.SameOrigin(base, endpoint); err != nil {
			return nil, nil, err
		}
	}
	if meta.RevocationEndpoint != "" {
		if err := api.SameOrigin(base, meta.RevocationEndpoint); err != nil {
			return nil, nil, err
		}
	}
	return &server, &meta, nil
}
func OpenBrowser(address string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", address)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	return cmd.Run()
}

// callback rejects mismatched state without consuming the legitimate request.
func Callback(state, issuer string, issRequired bool, result chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		if r.URL.Path != "/callback" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			writeCallbackPage(w, http.StatusBadRequest, callbackPage{Title: "Sign-in request not recognized", Detail: "This page does not match the sign-in waiting in your terminal. Return to your terminal and open the link it shows."})
			return
		}
		if (issRequired || q.Get("iss") != "") && q.Get("iss") != issuer {
			writeCallbackPage(w, http.StatusBadRequest, callbackPage{Title: "Sign-in rejected", Detail: "This authorization came from a different server than the one your terminal signs in to. To try again, run:", Retry: true})
			return
		}
		value := callbackResult{Code: q.Get("code")}
		if q.Get("error") != "" {
			value.Err = fmt.Errorf("authorization declined: %s", q.Get("error"))
		} else if value.Code == "" || len(q["code"]) != 1 {
			writeCallbackPage(w, http.StatusBadRequest, callbackPage{Title: "Sign-in incomplete", Detail: "The authorization response carried no code. To try again, run:", Retry: true})
			return
		}
		select {
		case result <- value:
		default:
			writeCallbackPage(w, http.StatusConflict, callbackPage{Title: "Authorization already received", Detail: "This sign-in already finished in the browser. Return to your terminal."})
			return
		}
		if value.Err != nil {
			writeCallbackPage(w, http.StatusOK, callbackPage{Title: "Sign-in cancelled", Detail: "The CLI has no access to your account. Return to your terminal."})
			return
		}
		writeCallbackPage(w, http.StatusOK, callbackPage{Received: true, Title: "Authorization received", Detail: "Return to your terminal to finish signing in. You can close this window."})
	})
}

type callbackResult struct {
	Code string
	Err  error
}

// stopServer closes the callback server once a response it is writing has
// been sent. The callback hands its result to Login before it writes its page,
// so closing at once could cut the page off in the browser.
func stopServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if server.Shutdown(ctx) != nil {
		_ = server.Close()
	}
}

// Login signs in with the browser. When the API binds tokens, it answers the
// key they are bound to, which the caller closes.
func Login(ctx context.Context, o LoginOptions) (_ *credential.Session, _ *ServerConfig, _ *devicekey.Key, err error) {
	client := o.HTTP
	if client == nil {
		client = api.HTTPClient(30 * time.Second)
	}
	server, meta, err := Discover(ctx, o.BaseURL, client)
	if err != nil {
		return nil, nil, nil, err
	}
	// The key comes first: a TPM can take a moment, and the browser should not
	// wait on it.
	var key *devicekey.Key
	if server.BindsTokens() {
		if key, err = devicekey.Create(); err != nil {
			return nil, nil, nil, fmt.Errorf("create the device key: %w", err)
		}
		defer func() {
			if err != nil {
				discardKey(key)
			}
		}()
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", o.Port))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open login callback: %w", err)
	}
	defer func() { _ = listener.Close() }()
	redirect := "http://" + listener.Addr().String() + "/callback"
	verifier, err := Random()
	if err != nil {
		return nil, nil, nil, err
	}
	state, err := Random()
	if err != nil {
		return nil, nil, nil, err
	}
	scope := "openid profile email offline_access cli:read"
	if !o.ReadOnly {
		scope += " cli:write"
	}
	// Registration is public metadata; persist it separately so cancelled logins
	// do not fill the provider's client capacity on every retry.
	registrationPath := filepath.Join(o.Dir, "clients", o.Profile+".json")
	var registration struct {
		BaseURL  string `json:"api_url"`
		ClientID string `json:"client_id"`
	}
	data, readErr := os.ReadFile(registrationPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, nil, nil, readErr
	}
	if readErr == nil {
		if err := json.Unmarshal(data, &registration); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid OAuth client registration: %w", err)
		}
	}
	if registration.BaseURL != o.BaseURL || registration.ClientID == "" {
		body, err := json.Marshal(map[string]any{"client_name": "Edka CLI", "client_uri": "https://github.com/edkadigital/cli", "redirect_uris": []string{redirect}, "token_endpoint_auth_method": "none", "application_type": "native", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "scope": scope, "resources": []string{server.Resource}, "dpop_bound_access_tokens": server.BindsTokens()})
		if err != nil {
			return nil, nil, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", meta.RegistrationEndpoint, strings.NewReader(string(body)))
		if err != nil {
			return nil, nil, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, nil, err
		}
		data, readErr := api.ReadBody(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, nil, nil, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, nil, nil, fmt.Errorf("register Edka CLI (HTTP %d); check CLI access is enabled", resp.StatusCode)
		}
		if err := json.Unmarshal(data, &registration); err != nil {
			return nil, nil, nil, err
		}
		if registration.ClientID == "" {
			return nil, nil, nil, errors.New("registration returned no client ID")
		}
		registration.BaseURL = o.BaseURL
		if err := config.WriteJSON(registrationPath, registration); err != nil {
			return nil, nil, nil, err
		}
	}
	result := make(chan callbackResult, 1)
	httpServer := &http.Server{Handler: Callback(state, meta.Issuer, meta.IssRequired, result), ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	defer stopServer(httpServer)
	endpoint, err := url.Parse(meta.AuthorizationEndpoint)
	if err != nil {
		return nil, nil, nil, err
	}
	q := endpoint.Query()
	for k, v := range map[string]string{"response_type": "code", "client_id": registration.ClientID, "redirect_uri": redirect, "scope": scope, "state": state, "code_challenge": Challenge(verifier), "code_challenge_method": "S256", "resource": server.Resource, "prompt": "consent"} {
		q.Set(k, v)
	}
	endpoint.RawQuery = q.Encode()
	fmt.Fprintf(o.Output, "\nAuthorize Edka CLI in your browser\n\n  %s\n\nWaiting for approval…  Press Ctrl+C to cancel.\n", endpoint.String())
	if !o.NoBrowser {
		if err := OpenBrowser(endpoint.String()); err != nil {
			fmt.Fprintln(o.Output, "Could not open a browser. Open the link above manually.")
		}
	}
	var callback callbackResult
	select {
	case <-ctx.Done():
		return nil, nil, nil, ctx.Err()
	case err := <-serverErrors:
		return nil, nil, nil, err
	case callback = <-result:
	}
	if callback.Err != nil {
		return nil, nil, nil, callback.Err
	}
	token, err := exchange(ctx, client, meta.TokenEndpoint, url.Values{"grant_type": {"authorization_code"}, "client_id": {registration.ClientID}, "redirect_uri": {redirect}, "code": {callback.Code}, "code_verifier": {verifier}, "resource": {server.Resource}}, key)
	if err != nil {
		return nil, nil, nil, err
	}
	session := &credential.Session{APIURL: o.BaseURL, Issuer: meta.Issuer, ClientID: registration.ClientID, TokenEndpoint: meta.TokenEndpoint, RevocationEndpoint: meta.RevocationEndpoint, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second), Scope: token.Scope}
	if key != nil && !token.bound() {
		discardKey(key)
		key = nil
	}
	if key != nil {
		saved := key.Saved()
		session.DeviceKey = &saved
	}
	return session, server, key, nil
}

// discardKey closes a key and deletes it from the hardware that keeps it by name.
func discardKey(key *devicekey.Key) {
	if key == nil {
		return
	}
	key.Close()
	_ = devicekey.Delete(key.Saved())
}

// Credentials are what a request needs: the access token and, when the token
// is bound to a device key, what opens that key.
type Credentials struct {
	AccessToken string
	DeviceKey   *devicekey.Saved
}

// Token answers the saved credentials of a profile, refreshed when the access
// token nears its end. A session signed in before the API bound tokens is
// bound to a new device key at that refresh.
func Token(ctx context.Context, store credential.Store, profile, base string, client *http.Client) (Credentials, error) {
	if !config.ValidName(profile) {
		return Credentials{}, fmt.Errorf("invalid profile name")
	}
	if err := os.MkdirAll(store.Dir, 0700); err != nil {
		return Credentials{}, err
	}
	lock := flock.New(filepath.Join(store.Dir, "refresh-"+profile+".lock"))
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return Credentials{}, err
	}
	if !locked {
		return Credentials{}, errors.New("another Edka command is refreshing credentials")
	}
	defer func() { _ = lock.Unlock() }()
	session, err := store.Load(profile)
	if err != nil {
		return Credentials{}, err
	}
	if session.APIURL != base {
		return Credentials{}, errors.New("credentials belong to another API origin; use another profile or run `edka login`")
	}
	if err := api.SameOrigin(base, session.TokenEndpoint); err != nil {
		return Credentials{}, err
	}
	if time.Until(session.ExpiresAt) > 60*time.Second {
		return Credentials{AccessToken: session.AccessToken, DeviceKey: session.DeviceKey}, nil
	}
	if session.RefreshToken == "" {
		return Credentials{}, credential.ErrNotFound
	}
	var key *devicekey.Key
	created := false
	if session.DeviceKey != nil {
		if key, err = devicekey.Open(*session.DeviceKey); err != nil {
			return Credentials{}, err
		}
	} else if bindsTokens(ctx, client, base) {
		// A key that can't be made leaves the session with bearer tokens.
		if key, err = devicekey.Create(); err == nil {
			created = true
		}
	}
	if key != nil {
		defer key.Close()
	}
	token, err := exchange(ctx, client, session.TokenEndpoint, url.Values{"grant_type": {"refresh_token"}, "client_id": {session.ClientID}, "refresh_token": {session.RefreshToken}, "resource": {base + "/api"}}, key)
	if err != nil {
		if created {
			_ = devicekey.Delete(key.Saved())
		}
		return Credentials{}, err
	}
	switch {
	case key != nil && token.bound():
		saved := key.Saved()
		session.DeviceKey = &saved
	case key != nil:
		_ = devicekey.Delete(key.Saved())
		session.DeviceKey = nil
	}
	session.AccessToken = token.AccessToken
	session.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	if token.RefreshToken != "" {
		session.RefreshToken = token.RefreshToken
	}
	if token.Scope != "" {
		session.Scope = token.Scope
	}
	if _, err := store.Save(profile, session); err != nil {
		return Credentials{}, fmt.Errorf("save refreshed credentials: %w", err)
	}
	return Credentials{AccessToken: session.AccessToken, DeviceKey: session.DeviceKey}, nil
}

// bindsTokens asks the API whether it binds tokens to a device key.
func bindsTokens(ctx context.Context, client *http.Client, base string) bool {
	response, err := (&api.Client{BaseURL: base, HTTP: client}).Do(ctx, "GET", "/api/cli/config", nil, nil)
	if err != nil {
		return false
	}
	var server ServerConfig
	return json.Unmarshal(response.Body, &server) == nil && server.BindsTokens()
}
func Revoke(ctx context.Context, client *http.Client, session *credential.Session) error {
	if session.RefreshToken == "" || session.RevocationEndpoint == "" {
		return nil
	}
	if err := api.SameOrigin(session.APIURL, session.RevocationEndpoint); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", session.RevocationEndpoint, strings.NewReader(url.Values{"client_id": {session.ClientID}, "token": {session.RefreshToken}, "token_type_hint": {"refresh_token"}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("revoke access (HTTP %d); retry logout or use --local", resp.StatusCode)
	}
	return nil
}
