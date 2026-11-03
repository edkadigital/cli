package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/credential"
	"github.com/edkadigital/cli/internal/devicekey"
)

// checkProof verifies the DPoP proof of a request as a server does, and
// answers the x coordinate of its key, which names the key, and its iat.
func checkProof(t *testing.T, r *http.Request, wantURL, accessToken string) (string, int64) {
	t.Helper()
	parts := strings.Split(r.Header.Get("DPoP"), ".")
	if len(parts) != 3 {
		t.Errorf("%s %s carries no proof", r.Method, r.URL.Path)
		return "", 0
	}
	var header struct {
		Typ, Alg string
		JWK      map[string]string
	}
	var claims struct {
		Jti, Htm, Htu, Ath string
		Iat                int64
	}
	decode := func(part string, into any) {
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err == nil {
			err = json.Unmarshal(data, into)
		}
		if err != nil {
			t.Error(err)
		}
	}
	decode(parts[0], &header)
	decode(parts[1], &claims)
	x, _ := base64.RawURLEncoding.DecodeString(header.JWK["x"])
	y, _ := base64.RawURLEncoding.DecodeString(header.JWK["y"])
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	if err != nil || header.Typ != "dpop+jwt" || header.Alg != "ES256" || len(signature) != 64 || !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Errorf("invalid proof header %+v", header)
	}
	wantAth := ""
	if accessToken != "" {
		sum := sha256.Sum256([]byte(accessToken))
		wantAth = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	if claims.Htm != r.Method || claims.Htu != wantURL || claims.Jti == "" || claims.Ath != wantAth {
		t.Errorf("proof claims %+v for %s %s", claims, r.Method, wantURL)
	}
	return header.JWK["x"], claims.Iat
}

func discardTestKey(saved *devicekey.Saved) {
	if saved != nil {
		_ = devicekey.Delete(*saved)
	}
}

func TestLoginBindsTokensToADeviceKey(t *testing.T) {
	var base, challenge, redirect, tokenKey string
	var mu sync.Mutex
	registered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/config":
			fmt.Fprintf(w, `{"resource":%q,"discovery_url":%q,"dpop_signing_alg_values_supported":["ES256"]}`, base+"/api", base+"/discovery")
		case "/discovery":
			fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"registration_endpoint":%q}`, base+"/api/auth", base+"/authorize", base+"/api/auth/oauth2/token", base+"/register")
		case "/register":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			registered = body["dpop_bound_access_tokens"] == true
			mu.Unlock()
			w.WriteHeader(201)
			fmt.Fprint(w, `{"client_id":"cli-test"}`)
		case "/api/auth/oauth2/token":
			_ = r.ParseForm()
			mu.Lock()
			if Challenge(r.Form.Get("code_verifier")) != challenge || r.Form.Get("redirect_uri") != redirect {
				t.Error("code exchange contract violated")
			}
			mu.Unlock()
			key, _ := checkProof(t, r, base+"/api/auth/oauth2/token", "")
			mu.Lock()
			tokenKey = key
			mu.Unlock()
			fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","token_type":"DPoP","expires_in":900}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	output := &loginOutput{urls: make(chan string, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		session *credential.Session
		key     *devicekey.Key
		err     error
	}
	done := make(chan result, 1)
	go func() {
		session, _, key, err := Login(ctx, LoginOptions{BaseURL: base, Dir: t.TempDir(), Profile: "default", NoBrowser: true, Output: output})
		done <- result{session, key, err}
	}()
	address := <-output.urls
	authorize, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	q := authorize.Query()
	mu.Lock()
	challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
	mu.Unlock()
	response, err := http.Get(redirect + "?" + url.Values{"state": {q.Get("state")}, "code": {"authorized-code"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	value := <-done
	if value.err != nil {
		t.Fatal(value.err)
	}
	defer value.key.Close()
	defer discardTestKey(value.session.DeviceKey)
	if !registered {
		t.Error("the client did not register for bound tokens")
	}
	if value.key == nil || value.session.DeviceKey == nil || value.key.JWK()["x"] != tokenKey {
		t.Fatalf("session %+v is not bound to the key that signed the token request", value.session)
	}
	t.Logf("protection: %s", value.key.Protection())
}

// refreshServer answers token refreshes. It binds them when bind is set, and
// records the key of each proof.
type refreshServer struct {
	t       *testing.T
	mu      sync.Mutex
	bind    bool
	keys    []string
	refuse  int
	skew    time.Duration
	iats    []int64
	server  *httptest.Server
	refresh string
}

func newRefreshServer(t *testing.T, bind bool) *refreshServer {
	s := &refreshServer{t: t, bind: bind, refresh: "refresh-1"}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/api/cli/config" {
			if s.bind {
				fmt.Fprint(w, `{"dpop_signing_alg_values_supported":["ES256"]}`)
			} else {
				fmt.Fprint(w, `{}`)
			}
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != s.refresh {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		w.Header().Set("Date", time.Now().Add(-s.skew).UTC().Format(http.TimeFormat))
		if r.Header.Get("DPoP") == "" {
			s.keys = append(s.keys, "")
			fmt.Fprint(w, `{"access_token":"bearer-access","refresh_token":"refresh-2","token_type":"Bearer","expires_in":900}`)
			s.refresh = "refresh-2"
			return
		}
		key, iat := checkProof(s.t, r, s.server.URL+"/token", "")
		s.keys = append(s.keys, key)
		s.iats = append(s.iats, iat)
		if s.refuse > 0 {
			s.refuse--
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_dpop_proof","error_description":"DPoP proof iat is outside the accepted window"}`)
			return
		}
		s.refresh = fmt.Sprintf("refresh-%d", len(s.keys)+1)
		fmt.Fprintf(w, `{"access_token":"bound-access","refresh_token":%q,"token_type":"DPoP","expires_in":900}`, s.refresh)
	}))
	return s
}

func (s *refreshServer) save(store credential.Store, saved *devicekey.Saved) {
	s.t.Helper()
	if _, err := store.Save("default", &credential.Session{APIURL: s.server.URL, TokenEndpoint: s.server.URL + "/token", ClientID: "cli-test", AccessToken: "stale", RefreshToken: s.refresh, ExpiresAt: time.Now().Add(-time.Minute), DeviceKey: saved}); err != nil {
		s.t.Fatal(err)
	}
}

func TestRefreshBindsASessionSignedInWithoutAKey(t *testing.T) {
	refresh := newRefreshServer(t, true)
	defer refresh.server.Close()
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	refresh.save(store, nil)
	client := api.HTTPClient(5 * time.Second)

	credentials, err := Token(context.Background(), store, "default", refresh.server.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	defer discardTestKey(credentials.DeviceKey)
	if credentials.AccessToken != "bound-access" || credentials.DeviceKey == nil || len(refresh.keys) != 1 || refresh.keys[0] == "" {
		t.Fatalf("credentials %+v, keys %v", credentials, refresh.keys)
	}
	session, err := store.Load("default")
	if err != nil || session.DeviceKey == nil {
		t.Fatalf("the bound session was not saved with its key: %+v %v", session, err)
	}

	// The next refresh proves the same key.
	refresh.save(store, session.DeviceKey)
	if _, err := Token(context.Background(), store, "default", refresh.server.URL, client); err != nil {
		t.Fatal(err)
	}
	if len(refresh.keys) != 2 || refresh.keys[1] != refresh.keys[0] {
		t.Fatalf("the second refresh proved another key: %v", refresh.keys)
	}
}

func TestRefreshKeepsBearerTokensWhereTheAPIBindsNone(t *testing.T) {
	refresh := newRefreshServer(t, false)
	defer refresh.server.Close()
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	refresh.save(store, nil)
	credentials, err := Token(context.Background(), store, "default", refresh.server.URL, api.HTTPClient(5*time.Second))
	if err != nil || credentials.AccessToken != "bearer-access" || credentials.DeviceKey != nil || len(refresh.keys) != 1 || refresh.keys[0] != "" {
		t.Fatalf("credentials %+v, err %v, keys %v", credentials, err, refresh.keys)
	}
}

func TestRefreshResendsAProofTheServerClockRefused(t *testing.T) {
	refresh := newRefreshServer(t, true)
	defer refresh.server.Close()
	refresh.skew, refresh.refuse = 2*time.Minute, 1
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	refresh.save(store, nil)
	credentials, err := Token(context.Background(), store, "default", refresh.server.URL, api.HTTPClient(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer discardTestKey(credentials.DeviceKey)
	if len(refresh.iats) != 2 || refresh.iats[0]-refresh.iats[1] < 110 || refresh.keys[0] != refresh.keys[1] {
		t.Fatalf("iats %v, keys %v", refresh.iats, refresh.keys)
	}
}

func TestRefreshRefusesAKeyThatDoesNotOpen(t *testing.T) {
	refresh := newRefreshServer(t, true)
	defer refresh.server.Close()
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	refresh.save(store, &devicekey.Saved{Protection: devicekey.Software, Handle: []byte("not a key")})
	if _, err := Token(context.Background(), store, "default", refresh.server.URL, api.HTTPClient(5*time.Second)); err == nil || !strings.Contains(err.Error(), "edka login") {
		t.Fatalf("err = %v", err)
	}
	if len(refresh.keys) != 0 {
		t.Fatal("refreshed without the session's key")
	}
}

func TestRefreshDeletesAKeyItCouldNotSave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory doesn't stop the owner on Windows")
	}
	refresh := newRefreshServer(t, true)
	defer refresh.server.Close()
	store := credential.Store{Dir: t.TempDir(), Mode: "file"}
	refresh.save(store, nil)
	credentials := filepath.Join(store.Dir, "credentials")
	if err := os.Chmod(credentials, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(credentials, 0o700) }()
	var deleted []devicekey.Saved
	defer func(previous func(devicekey.Saved) error) { deleteDeviceKey = previous }(deleteDeviceKey)
	deleteDeviceKey = func(saved devicekey.Saved) error { deleted = append(deleted, saved); return nil }

	if _, err := Token(context.Background(), store, "default", refresh.server.URL, api.HTTPClient(5*time.Second)); err == nil || !strings.Contains(err.Error(), "save refreshed credentials") {
		t.Fatalf("err = %v", err)
	}
	if len(refresh.keys) != 1 || len(deleted) != 1 {
		t.Fatalf("refreshes %d, deleted keys %d", len(refresh.keys), len(deleted))
	}
}
