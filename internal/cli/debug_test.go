package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// debugAPI answers with a request ID, and with 500 for the databases of c1.
func debugAPI(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req-7")
		switch r.URL.Path {
		case "/api/clusters":
			fmt.Fprint(w, `{"data":[{"id":"c1","name":"sinaia"}]}`)
		case "/api/clusters/c1/databases":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"boom"}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDebugPrintsEachRequestToStderr(t *testing.T) {
	server := debugAPI(t)
	out, errOut, err := execute(t, server.URL, "clusters", "list", "--json", "--debug")
	if err != nil {
		t.Fatal(err)
	}
	// Stdout stays what a script reads.
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(out), &body) != nil || len(body.Data) != 1 {
		t.Fatal(out)
	}
	if !regexp.MustCompile(`^debug: GET http://127\.0\.0\.1:\d+/api/clusters 200 \d+ms \(request req-7\)\n$`).MatchString(errOut) {
		t.Fatalf("%q", errOut)
	}

	_, errOut, err = execute(t, server.URL, "api", "get", "/api/clusters", "--query", "limit=5", "--debug")
	if err != nil || !regexp.MustCompile(`^debug: GET http://127\.0\.0\.1:\d+/api/clusters\?limit=5 200 \d+ms`).MatchString(errOut) {
		t.Fatalf("%q %v", errOut, err)
	}

	_, errOut, err = execute(t, server.URL, "databases", "list", "--debug")
	lines := strings.Split(strings.TrimSpace(errOut), "\n")
	if err == nil || len(lines) != 2 || !strings.Contains(lines[0], "/api/clusters 200 ") || !strings.Contains(lines[1], "/api/clusters/c1/databases 500 ") {
		t.Fatalf("%q %v", errOut, err)
	}
}

func TestDebugPrintsARequestThatFails(t *testing.T) {
	_, errOut, err := execute(t, "http://127.0.0.1:1", "clusters", "list", "--debug")
	if err == nil || !regexp.MustCompile(`^debug: GET http://127\.0\.0\.1:1/api/clusters failed \d+ms: dial tcp `).MatchString(errOut) {
		t.Fatalf("%q %v", errOut, err)
	}
}

func TestDebugPrintsNoCredentialAndIsOffByDefault(t *testing.T) {
	server := debugAPI(t)
	// execute signs in with the token "test-token".
	_, errOut, err := execute(t, server.URL, "api", "post", "/api/clusters", "--field", "name=staging", "--debug")
	if err != nil || !strings.Contains(errOut, "debug: POST ") || strings.Contains(errOut, "test-token") || strings.Contains(errOut, "staging") {
		t.Fatalf("%q %v", errOut, err)
	}
	if _, errOut, err := execute(t, server.URL, "clusters", "list"); err != nil || errOut != "" {
		t.Fatalf("%q %v", errOut, err)
	}
	t.Setenv("EDKA_DEBUG", "1")
	if _, errOut, err := execute(t, server.URL, "clusters", "list"); err != nil || !strings.HasPrefix(errOut, "debug: GET ") {
		t.Fatalf("%q %v", errOut, err)
	}
	t.Setenv("EDKA_DEBUG", "0")
	if _, errOut, err := execute(t, server.URL, "clusters", "list"); err != nil || errOut != "" {
		t.Fatalf("%q %v", errOut, err)
	}
}
