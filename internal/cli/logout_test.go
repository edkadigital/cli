package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/edkadigital/cli/internal/credential"
)

// Logout revokes the refresh token, then removes the saved credentials. When
// Edka can't revoke it, the credentials stay, so the token is not forgotten
// while it still works.
func TestLogoutRevokesBeforeItForgets(t *testing.T) {
	var mu sync.Mutex
	revoked, status := 0, http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "POST" || r.URL.Path != "/revoke" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil || r.PostForm.Get("token") != "refresh" {
			t.Errorf("revoked %q: %v", r.PostForm.Get("token"), err)
		}
		revoked++
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	t.Setenv("EDKA_CONFIG_DIR", dir)
	t.Setenv("EDKA_CREDENTIAL_STORE", "file")
	t.Setenv("EDKA_API_URL", server.URL)
	for _, name := range []string{"EDKA_PROFILE", "EDKA_TOKEN", "EDKA_CLUSTER", "EDKA_DEPLOYMENT", "EDKA_ORGANIZATION"} {
		t.Setenv(name, "")
	}
	store := credential.Store{Dir: dir, Mode: "file"}
	signIn := func() {
		t.Helper()
		if _, err := store.Save("default", &credential.Session{APIURL: server.URL, RevocationEndpoint: server.URL + "/revoke", ClientID: "cli-test", AccessToken: "access", RefreshToken: "refresh"}); err != nil {
			t.Fatal(err)
		}
	}
	signedIn := func() bool {
		t.Helper()
		_, err := store.Load("default")
		if err != nil && !errors.Is(err, credential.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}
	logout := func(args ...string) (string, string, error) {
		var out, errOut bytes.Buffer
		root := New("test", strings.NewReader(""), &out, &errOut)
		root.SetArgs(append([]string{"logout"}, args...))
		err := root.Execute()
		return out.String(), errOut.String(), err
	}

	signIn()
	_, errOut, err := logout()
	if err != nil || revoked != 1 || signedIn() || !strings.Contains(errOut, "✓ Signed out of default") {
		t.Fatal(errOut, err, revoked)
	}

	// Edka refuses the revocation: the credentials stay for another attempt.
	signIn()
	status = http.StatusServiceUnavailable
	_, _, err = logout()
	if err == nil || !strings.Contains(err.Error(), "revoke access (HTTP 503); retry logout or use --local") || !signedIn() {
		t.Fatal(err, signedIn())
	}
	// --local removes them without asking Edka.
	out, _, err := logout("--local", "--json")
	if err != nil || revoked != 2 || signedIn() || !strings.Contains(out, `"signed_out": true`) {
		t.Fatal(out, err, revoked)
	}

	// Signing out twice is not an error, and asks Edka nothing.
	if _, errOut, err := logout(); err != nil || revoked != 2 || !strings.Contains(errOut, "✓ Signed out of default") {
		t.Fatal(errOut, err, revoked)
	}
}
