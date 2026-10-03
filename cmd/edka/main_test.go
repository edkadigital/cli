package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
)

// runMain runs main with args in a child process, where it can exit.
func runMain(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestMainHelper", "--"}, args...)...)
	cmd.Env = append(append(os.Environ(), env...), "EDKA_MAIN_HELPER=1", "EDKA_CONFIG_DIR="+t.TempDir())
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}
func TestSyntaxErrorsRespectJSON(t *testing.T) {
	for _, args := range [][]string{{"nonexistent", "--json"}, {"--output=json", "nonexistent"}, {"nonexistent", "--output", "json"}} {
		out, errOut, code := runMain(t, nil, args...)
		if code == 0 {
			t.Fatal("invalid command succeeded")
		}
		if out != "" || !json.Valid([]byte(errOut)) {
			t.Fatalf("stdout=%s stderr=%s", out, errOut)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(errOut), &value); err != nil {
			t.Fatal(err)
		}
		if value["exit_code"] != float64(1) || value["error"] == nil {
			t.Fatal(value)
		}
	}
}

// A script reads an API error's code, reason and invalid fields from the JSON
// on stderr; without --json the fields follow the message.
func TestAPIErrorsKeepCodeAndFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "request-1")
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"Invalid configuration","message":"Invalid app configuration","code":"invalid_configuration","fields":[{"field":"database_password","message":"Required"}]}`)
	}))
	defer server.Close()
	env := []string{"EDKA_API_URL=" + server.URL, "EDKA_TOKEN=test-token", "NO_COLOR=1"}
	out, errOut, code := runMain(t, env, "api", "get", "/api/apps", "--json")
	var value struct {
		Error     string
		ExitCode  int `json:"exit_code"`
		Status    int
		RequestID string `json:"request_id"`
		Code      string
		Reason    string
		Details   *string
		Fields    []map[string]string
	}
	if err := json.Unmarshal([]byte(errOut), &value); err != nil || out != "" || code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q: %v", code, out, errOut, err)
	}
	if value.Error != "Invalid app configuration (HTTP 400)\n  database_password: Required\nRequest ID: request-1" || value.ExitCode != 1 || value.Status != 400 || value.RequestID != "request-1" || value.Code != "invalid_configuration" || value.Reason != "Invalid configuration" || value.Details != nil || len(value.Fields) != 1 || value.Fields[0]["field"] != "database_password" || value.Fields[0]["message"] != "Required" {
		t.Fatal(errOut)
	}
	_, errOut, code = runMain(t, env, "api", "get", "/api/apps")
	if code != 1 || !strings.Contains(errOut, "Error: Invalid app configuration (HTTP 400)\n  database_password: Required\nRequest ID: request-1") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}
func TestErrorsKeepSuggestionIndent(t *testing.T) {
	_, errOut, code := runMain(t, nil, "apps", "lsit")
	if code != 1 || !strings.Contains(errOut, "Did you mean this?\n\tlist") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

// A script that leaves out an app's settings reads them, with their options,
// from the JSON error.
func TestJSONErrorsListMissingSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/clusters":
			fmt.Fprint(w, `{"data":[{"id":"c1","name":"sinaia"}]}`)
		case "GET /api/clusters/c1/apps/catalog":
			fmt.Fprint(w, `{"data":[{"id":"cat-wb","name":"Whiteboard","slug":"whiteboard","inputs_schema":{"general":[{"name":"postgres_instance","config":{"type":"dynamic-select","label":"PostgreSQL","data_source":"cluster-postgresql-instances","required":true}}]}}]}`)
		case "GET /api/clusters/c1/apps/data-sources/cluster-postgresql-instances":
			fmt.Fprint(w, `{"data":[{"value":"pg-1","label":"orders (orders.postgres)"}]}`)
		case "POST /api/clusters/c1/apps/cat-wb/instances":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"Invalid configuration","message":"Invalid app configuration: postgres_instance: …","fields":[{"field":"postgres_instance","message":"Required"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	out, errOut, code := runMain(t, []string{"EDKA_TOKEN=t", "EDKA_API_URL=" + server.URL}, "apps", "install", "whiteboard", "--cluster", "sinaia", "--json")
	var value struct {
		Status int `json:"status"`
		Fields []struct {
			Field   string `json:"field"`
			Options []struct {
				Value string `json:"value"`
			} `json:"options"`
		} `json:"fields"`
	}
	if code != 1 || out != "" || json.Unmarshal([]byte(errOut), &value) != nil {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if value.Status != 400 || len(value.Fields) != 1 || value.Fields[0].Field != "postgres_instance" || len(value.Fields[0].Options) != 1 || value.Fields[0].Options[0].Value != "pg-1" {
		t.Fatal(errOut)
	}
}

// A command started by `edka run` reports its own failure; edka only passes
// its exit code on.
func TestRunKeepsChildExitCodeQuietly(t *testing.T) {
	_, errOut, code := runMain(t, []string{"EDKA_CHILD_EXIT=3"}, "run", "--", os.Args[0], "-test.run=TestChildExitHelper")
	if code != 3 || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}
func TestChildExitHelper(t *testing.T) {
	if os.Getenv("EDKA_CHILD_EXIT") == "3" {
		os.Exit(3)
	}
}
func TestMainHelper(t *testing.T) {
	if os.Getenv("EDKA_MAIN_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"edka"}, os.Args[i+1:]...)
			break
		}
	}
	os.Exit(run())
}

// `go install …@v1.2.3` sets no version at build time, so the binary takes the
// tag Go recorded, and `edka upgrade` treats it as that release.
func TestBuildVersion(t *testing.T) {
	for _, tc := range []struct{ set, module, want string }{
		{"v1.2.3", "v9.9.9", "v1.2.3"},
		{"dev", "v1.2.3", "dev"},
		{"", "v1.2.3", "v1.2.3"},
		{"", "v1.3.0-rc.1", "v1.3.0-rc.1"},
		{"", "v1.2.4-0.20261003101500-0123456789ab", "dev"},
		{"", "v0.0.0-20261003101500-0123456789ab", "dev"},
		{"", "v1.2.3+dirty", "dev"},
		{"", "(devel)", "dev"},
		{"", "", "dev"},
	} {
		if got := buildVersion(tc.set, &debug.BuildInfo{Main: debug.Module{Version: tc.module}}); got != tc.want {
			t.Errorf("set %q, module %q: %q", tc.set, tc.module, got)
		}
	}
	if got := buildVersion("", nil); got != "dev" {
		t.Error(got)
	}
}
