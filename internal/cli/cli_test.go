package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/catalog"
	"github.com/edkadigital/cli/internal/config"
)

func execute(t *testing.T, base string, args ...string) (string, string, error) {
	t.Helper()
	return executeInput(t, base, "", args...)
}

// executeInput runs the CLI with input on stdin.
func executeInput(t *testing.T, base, input string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("EDKA_CONFIG_DIR", t.TempDir())
	t.Setenv("EDKA_TOKEN", "test-token")
	t.Setenv("EDKA_API_URL", base)
	t.Setenv("EDKA_PROFILE", "")
	t.Setenv("EDKA_CLUSTER", "")
	t.Setenv("EDKA_DEPLOYMENT", "")
	t.Setenv("EDKA_ORGANIZATION", "")
	var out, errOut bytes.Buffer
	root := New("test", strings.NewReader(input), &out, &errOut)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}
func TestGeneratedCatalogHasNoMissingOrDuplicateCommands(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	seen := map[string]bool{}
	for _, op := range c.Operations {
		if seen[op.Command] {
			t.Errorf("duplicate %s", op.Command)
		}
		seen[op.Command] = true
		cmd, args, err := root.Find(append([]string{"api"}, strings.Fields(op.Command)...))
		if err != nil || len(args) != 0 || cmd.RunE == nil {
			t.Errorf("unregistered %s", op.Command)
		}
		for _, p := range op.Params {
			if cmd.Flag(p.Flag) == nil {
				t.Errorf("missing parameter %s in %s", p.Flag, op.Command)
			}
		}
	}
	if len(c.Operations) < 400 {
		t.Fatal("catalog unexpectedly narrowed")
	}
}
func TestGeneratedCommandNames(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	commands := map[string]string{}
	for _, op := range c.Operations {
		commands[op.Method+" "+op.Path] = op.Command
	}
	for route, want := range map[string]string{
		// A path that ends in a verb is an action named after it.
		"POST /api/deployments/:id/restart":                         "deployments restart",
		"POST /api/cronjobs/:id/trigger":                            "cronjobs trigger",
		"PATCH /api/cronjobs/:id/suspend":                           "cronjobs suspend",
		"POST /api/clusters/:clusterId/explorer/actions/drain-node": "clusters explorer actions drain-node",
		"POST /api/clusters/:id/user-credentials/rotate":            "clusters user-credentials rotate-own",
		"POST /api/clusters/:id/update":                             "clusters run-update",
		// A path that ends in a noun keeps the method's verb.
		"POST /api/clusters/:clusterId/databases/:databaseId/backups": "clusters databases backups create",
		"PATCH /api/deployments/:id/settings":                         "deployments settings update",
		"PATCH /api/clusters/:id":                                     "clusters update",
		// A read of one record is get; a read of a collection is list.
		"GET /api/deployments/:id/status":               "deployments status get",
		"GET /api/deployments/:id/builds/:buildId/logs": "deployments builds logs get",
		"GET /api/global-secrets/:secretName/exists":    "global-secrets exists get",
		"GET /api/deployments/:id/builds":               "deployments builds list",
		"GET /api/clusters":                             "clusters list",
	} {
		if commands[route] != want {
			t.Errorf("%s is %q, want %q", route, commands[route], want)
		}
	}
}
func TestEndpointHelpShowsTheRouteOnce(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	described := 0
	for _, op := range c.Operations {
		route := op.Method + " " + op.Path
		if op.Summary == route {
			t.Errorf("%s repeats its route as its summary", op.Command)
		}
		cmd, _, err := root.Find(append([]string{"api"}, strings.Fields(op.Command)...))
		if err != nil {
			t.Fatal(err)
		}
		want := first(op.Summary, route)
		if cmd.Short != want || strings.Count(cmd.Long, route) != 1 || !strings.HasPrefix(cmd.Long, want) {
			t.Errorf("%s: short %q, long %q", op.Command, cmd.Short, cmd.Long)
		}
		if op.Summary != "" {
			described++
		}
	}
	if described < 100 {
		t.Fatalf("only %d endpoint commands have summaries", described)
	}
}

func TestResourceRoutingAndJSON(t *testing.T) {
	var called string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = r.Method + " " + r.URL.Path
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		fmt.Fprint(w, `{"data":[{"id":"c1","name":"production"}]}`)
	}))
	defer server.Close()
	out, errOut, err := execute(t, server.URL, "api", "clusters", "list", "--json")
	if err != nil || called != "GET /api/clusters" || !json.Valid([]byte(out)) || errOut != "" {
		t.Fatalf("%s %s %s %v", called, out, errOut, err)
	}
}
func TestClusterNameResolutionAndNestedParameters(t *testing.T) {
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/clusters" {
			fmt.Fprint(w, `{"data":[{"id":"c1","name":"production"}]}`)
		} else {
			fmt.Fprint(w, `{"data":{"id":"c1","name":"production"}}`)
		}
	}))
	defer server.Close()
	_, _, err := execute(t, server.URL, "api", "cluster", "get", "production", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/api/clusters,/api/clusters/c1" {
		t.Fatal(paths)
	}
}
func TestDestructiveRequestRequiresConfirmation(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			writes++
		}
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer server.Close()
	_, _, err := execute(t, server.URL, "api", "deployments", "delete", "deployment-id")
	if err == nil || !strings.Contains(err.Error(), "--yes") || writes != 0 {
		t.Fatalf("writes=%d error=%v", writes, err)
	}
	_, _, err = execute(t, server.URL, "api", "deployments", "delete", "deployment-id", "--yes")
	if err != nil || writes != 1 {
		t.Fatalf("writes=%d error=%v", writes, err)
	}
	// The catalog flags this route; its method and path words alone would not.
	posts := 0
	flagged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer flagged.Close()
	if _, _, err := execute(t, flagged.URL, "api", "cronjobs", "restore-version", "cronjob-id"); err == nil || !strings.Contains(err.Error(), "--yes") || posts != 0 {
		t.Fatalf("requests=%d error=%v", posts, err)
	}
	if _, _, err := execute(t, flagged.URL, "api", "post", "/api/cronjobs/cronjob-id/restore-version"); err == nil || !strings.Contains(err.Error(), "--yes") || posts != 0 {
		t.Fatalf("requests=%d error=%v", posts, err)
	}
	if _, _, err := execute(t, flagged.URL, "api", "cronjobs", "restore-version", "cronjob-id", "--yes"); err != nil || posts != 1 {
		t.Fatalf("requests=%d error=%v", posts, err)
	}
	out, _, err := execute(t, flagged.URL, "api", "cronjobs", "restore-version", "--help")
	if err != nil || !strings.Contains(out, "asks for confirmation") {
		t.Fatal(out, err)
	}
	if out, _, _ := execute(t, flagged.URL, "api", "cronjobs", "trigger", "--help"); strings.Contains(out, "asks for confirmation") {
		t.Fatal(out)
	}
}
func TestBodyAndQueryTypes(t *testing.T) {
	a := App{In: strings.NewReader(`{"config":{"port":8080}}`)}
	body, err := a.body("@-", []string{"name=web", "config.replicas:=2", "config.exposed:=true", "config.tags:=[\"a\",\"b\"]"})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	cfg := value["config"].(map[string]any)
	if cfg["replicas"] != float64(2) || cfg["port"] != float64(8080) || cfg["exposed"] != true {
		t.Fatal(string(body))
	}
	if _, err := a.body(`{"config":null}`, []string{"config.name=web"}); err == nil {
		t.Fatal("accepted conflicting object")
	}
	q, err := parseQuery([]string{"name=a&b", "label=one", "label=two"})
	if err != nil || q.Get("name") != "a&b" || len(q["label"]) != 2 {
		t.Fatal(q, err)
	}
}
func TestDryRunDoesNotWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("dry-run sent an API request") }))
	defer server.Close()
	out, _, err := execute(t, server.URL, "api", "clusters", "create", "--field", "name=test", "--field", "master_ha:=true", "--dry-run")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"POST"`) {
		t.Fatal(out, err)
	}
}
func TestProfilePrecedenceAndContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDKA_CONFIG_DIR", dir)
	t.Setenv("EDKA_PROFILE", "env")
	t.Setenv("EDKA_API_URL", "")
	c, _ := config.Load(dir)
	c.Profiles["env"] = config.Profile{APIURL: "https://env.example"}
	c.Profiles["flag"] = config.Profile{APIURL: "https://flag.example"}
	if err := config.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root := New("test", strings.NewReader(""), &out, &bytes.Buffer{})
	root.SetArgs([]string{"context", "--profile", "flag", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://flag.example") {
		t.Fatal(out.String())
	}
}
func TestEnvironmentClusterPinsDeployment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDKA_CONFIG_DIR", dir)
	t.Setenv("EDKA_PROFILE", "")
	t.Setenv("EDKA_API_URL", "")
	t.Setenv("EDKA_ORGANIZATION", "")
	c, _ := config.Load(dir)
	c.Profiles["default"] = config.Profile{APIURL: "https://api.example"}
	if err := config.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	t.Chdir(project)
	if err := os.WriteFile(filepath.Join(project, ".edka.json"), []byte(`{"version":1,"profile":"default","api_url":"https://api.example","cluster":"c1","deployment":"d1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cluster, deployment string
		set                 bool
		want                string
	}{
		{"", "", true, "d1"},    // nothing pinned: the link supplies both
		{"c2", "", false, "d1"}, // a shell's EDKA_CLUSTER alone keeps the link's deployment
		{"c2", "", true, ""},    // edka run pinned a cluster with no deployment
		{"c2", "d2", true, "d2"},
	} {
		t.Setenv("EDKA_CLUSTER", tc.cluster)
		t.Setenv("EDKA_DEPLOYMENT", tc.deployment)
		if !tc.set {
			if err := os.Unsetenv("EDKA_DEPLOYMENT"); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		root := New("test", strings.NewReader(""), &out, &bytes.Buffer{})
		root.SetArgs([]string{"context", "--json"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		var context map[string]any
		if err := json.Unmarshal(out.Bytes(), &context); err != nil {
			t.Fatal(err)
		}
		if got, _ := context["deployment"].(string); got != tc.want {
			t.Errorf("EDKA_CLUSTER=%q EDKA_DEPLOYMENT=%q (set %v): deployment %q, want %q", tc.cluster, tc.deployment, tc.set, got, tc.want)
		}
	}
}
func TestProfileListIsSorted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDKA_CONFIG_DIR", dir)
	t.Setenv("EDKA_PROFILE", "")
	c, _ := config.Load(dir)
	for _, name := range []string{"zeta", "alpha", "mid", "beta"} {
		c.Profiles[name] = config.Profile{APIURL: "https://" + name + ".example"}
	}
	if err := config.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	// Map order varies between runs, so one lucky order proves nothing.
	for range 5 {
		var out bytes.Buffer
		root := New("test", strings.NewReader(""), &out, &bytes.Buffer{})
		root.SetArgs([]string{"profile", "list", "--json"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, row := range rows {
			names = append(names, fmt.Sprint(row["name"]))
		}
		if strings.Join(names, ",") != "alpha,beta,default,mid,zeta" {
			t.Fatal(names)
		}
	}
}

// Commands that change local state print a record in JSON mode, so a script
// reads stdout after them as after any other command.
func TestLocalChangesPrintJSONRecords(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDKA_CONFIG_DIR", dir)
	t.Setenv("EDKA_PROFILE", "")
	c, _ := config.Load(dir)
	c.Profiles["work"] = config.Profile{APIURL: "https://work.example", OrganizationName: "Acme"}
	if err := config.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	t.Chdir(project)
	path := filepath.Join(project, config.LinkName)
	if err := os.WriteFile(path, []byte(`{"version":1,"profile":"work","api_url":"https://work.example","cluster":"c1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (map[string]any, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		root := New("test", strings.NewReader(""), &out, &errOut)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatal(args, err)
		}
		var record map[string]any
		if out.Len() > 0 {
			if err := json.Unmarshal(out.Bytes(), &record); err != nil {
				t.Fatal(out.String(), err)
			}
		}
		return record, out.String(), errOut.String()
	}
	if record, _, _ := run("profile", "use", "work", "--json"); record["name"] != "work" || record["status"] != "active" || record["api_url"] != "https://work.example" || record["organization"] != "Acme" {
		t.Fatal(record)
	}
	if record, _, _ := run("unlink", "--json"); record["removed"] != true || !strings.HasSuffix(fmt.Sprint(record["project_file"]), config.LinkName) {
		t.Fatal(record)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("link was not removed")
	}
	// Nothing is left to remove, and the command says so.
	if record, _, errOut := run("unlink", "--json"); record["removed"] != false || !strings.Contains(errOut, "No project link in this directory.") {
		t.Fatal(record, errOut)
	}
	// Without --json the commands print nothing on stdout.
	for _, args := range [][]string{{"profile", "use", "work"}, {"unlink"}} {
		if _, out, _ := run(args...); out != "" {
			t.Fatal(args, out)
		}
	}
}
func TestOperationsSearch(t *testing.T) {
	out, _, err := execute(t, "https://api.edka.io", "api", "operations", "--search", "revisions", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) == 0 {
		t.Fatal(out, err)
	}
	for _, row := range rows {
		if row["method"] != "GET" || !strings.HasPrefix(fmt.Sprint(row["command"]), "edka api ") || row["path"] == nil || row["status"] != nil {
			t.Fatal(row)
		}
	}
	out, _, err = execute(t, "https://api.edka.io", "api", "operations", "--search", "revisions")
	if err != nil || !strings.HasPrefix(out, "COMMAND") || !strings.Contains(out, "/api/deployments/:id/revisions") {
		t.Fatal(out, err)
	}
}
func TestLogsAndDelta(t *testing.T) {
	text, err := logText([]byte(`{"logs":"one\ntwo\n","podName":"web"}`))
	if err != nil || text != "one\ntwo\n" {
		t.Fatal(text, err)
	}
	for _, c := range []struct {
		before, after, want string
		continued           bool
	}{{"", "a\n", "a\n", true}, {"a\nb", "a\nb\nc", "\nc", true}, {"a\nb\nc", "b\nc\nd", "d", true}, {"a", "a", "", true}, {"a", "z", "z", false}, {"a\n", "", "", false}} {
		if got, continued := logDelta(c.before, c.after); got != c.want || continued != c.continued {
			t.Errorf("logDelta(%q, %q) = %q, %t; want %q, %t", c.before, c.after, got, continued, c.want, c.continued)
		}
	}
}

// --follow names the pod it reads, retries a failed read, waits while no pod
// runs, follows the new pod after a rollout, and says when lines may be
// missing. Edka's notes go to stderr, never into the log.
func TestLogsFollowAcrossPods(t *testing.T) {
	fastPolls(t)
	interval := minLogInterval
	minLogInterval = time.Millisecond
	t.Cleanup(func() { minLogInterval = interval })
	server, requests := sequenceAPI(t, map[string][]string{
		"GET /api/deployments": {`{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`},
		"GET /api/deployments/d1/logs": {
			`{"logs":"one\ntwo\n","podName":"api-a"}`,
			`502 {"error":"Bad gateway"}`,
			`{"logs":"one\ntwo\nthree\n","podName":"api-a"}`,
			`404 {"error":"No pods found for deployment"}`,
			`{"logs":"Container is still starting and has not produced logs yet.","podName":"api-b"}`,
			`{"logs":"boot\n","podName":"api-b"}`,
			`{"logs":"seven\neight\n","podName":"api-b"}`,
			`{"logs":"three\nfour\n","podName":"api-a"}`,
			`403 {"error":"Forbidden"}`,
		},
	})
	out, errOut, err := execute(t, server.URL, "logs", "api", "--follow", "--interval", "1ms")
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatal(err)
	}
	if out != "one\ntwo\nthree\nboot\nseven\neight\nfour\n" {
		t.Fatalf("%q", out)
	}
	inOrder(t, errOut,
		"Following pod api-a",
		"Could not read from Edka, trying again",
		"No pod is running; waiting for one…",
		"Following pod api-b",
		"Container is still starting and has not produced logs yet.",
		"Lines of pod api-b may be missing",
		"Following pod api-a",
	)
	if strings.Count(errOut, "may be missing") != 1 {
		t.Fatalf("one gap expected:\n%s", errOut)
	}
	if (*requests)[0] != "GET /api/deployments" {
		t.Fatal(*requests)
	}
}

// --since asks for the lines of the last duration, up to 2000 unless --tail
// says otherwise, and refuses a log from an Edka API that doesn't apply it.
func TestLogsSince(t *testing.T) {
	deployments := `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`
	server, requests := fakeAPI(t, map[string]string{
		"GET /api/deployments":         deployments,
		"GET /api/deployments/d1/logs": `{"logs":"ready\n","podName":"api-a","parameters":{"tailLines":2000,"sinceSeconds":600,"timestamps":false}}`,
	})
	out, _, err := execute(t, server.URL, "logs", "api", "--since", "10m")
	if err != nil || out != "ready\n" {
		t.Fatalf("out=%q %v", out, err)
	}
	if last := (*requests)[len(*requests)-1]; last != "GET /api/deployments/d1/logs?sinceSeconds=600&tailLines=2000" {
		t.Fatal(last)
	}
	if _, _, err := execute(t, server.URL, "logs", "api", "--since", "90s", "--tail", "50"); err != nil {
		t.Fatal(err)
	}
	if last := (*requests)[len(*requests)-1]; last != "GET /api/deployments/d1/logs?sinceSeconds=90&tailLines=50" {
		t.Fatal(last)
	}
	if _, _, err := execute(t, server.URL, "logs", "api", "--since", "500ms"); err == nil || !strings.Contains(err.Error(), "at least one second") {
		t.Fatal(err)
	}

	old, _ := fakeAPI(t, map[string]string{
		"GET /api/deployments":         deployments,
		"GET /api/deployments/d1/logs": `{"logs":"from yesterday\n","podName":"api-a","parameters":{"tailLines":2000}}`,
	})
	out, _, err = execute(t, old.URL, "logs", "api", "--since", "10m")
	if !errors.Is(err, errSinceUnsupported) || out != "" {
		t.Fatalf("out=%q %v", out, err)
	}
}

// --timestamps prints each line's time, or says that Edka sent none.
func TestLogsTimestamps(t *testing.T) {
	deployments := `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`
	server, requests := fakeAPI(t, map[string]string{
		"GET /api/deployments":         deployments,
		"GET /api/deployments/d1/logs": `{"logs":"2026-10-03T12:00:00.5Z ready\n","podName":"api-a","parameters":{"timestamps":true}}`,
	})
	out, errOut, err := execute(t, server.URL, "logs", "api", "--timestamps")
	if err != nil || out != "2026-10-03T12:00:00.5Z ready\n" || errOut != "" {
		t.Fatalf("out=%q err=%q %v", out, errOut, err)
	}
	if last := (*requests)[len(*requests)-1]; last != "GET /api/deployments/d1/logs?tailLines=100&timestamps=true" {
		t.Fatal(last)
	}
	old, _ := fakeAPI(t, map[string]string{
		"GET /api/deployments":         deployments,
		"GET /api/deployments/d1/logs": `{"logs":"ready\n","podName":"api-a","parameters":{"tailLines":100}}`,
	})
	out, errOut, err = execute(t, old.URL, "logs", "api", "--timestamps")
	if err != nil || out != "ready\n" || !strings.Contains(errOut, "doesn't add timestamps") {
		t.Fatalf("out=%q err=%q %v", out, errOut, err)
	}
}

// --follow reads with timestamps, so a line that repeats exactly prints each
// time it is logged, and the times stay off the output unless asked for.
func TestLogsFollowByTimestamps(t *testing.T) {
	fastPolls(t)
	interval := minLogInterval
	minLogInterval = time.Millisecond
	t.Cleanup(func() { minLogInterval = interval })
	reads := []string{
		"2026-10-03T12:00:01Z health ok\n2026-10-03T12:00:02Z health ok\n",
		"2026-10-03T12:00:02Z health ok\n2026-10-03T12:00:03Z health ok\n",
		"2026-10-03T12:00:03Z health ok\n2026-10-03T12:00:04Z served /\n",
	}
	for _, c := range []struct {
		flags []string
		want  string
	}{
		{nil, "health ok\nhealth ok\nhealth ok\nserved /\n"},
		{[]string{"--timestamps"}, "2026-10-03T12:00:01Z health ok\n2026-10-03T12:00:02Z health ok\n2026-10-03T12:00:03Z health ok\n2026-10-03T12:00:04Z served /\n"},
	} {
		var queries []string
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/deployments":
				fmt.Fprint(w, `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`)
			case "/api/deployments/d1/logs":
				queries = append(queries, r.URL.RawQuery)
				if calls == len(reads) {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"error":"Forbidden"}`)
					return
				}
				body, _ := json.Marshal(map[string]any{"logs": reads[calls], "podName": "api-a", "parameters": map[string]any{"timestamps": true}})
				calls++
				w.Write(body)
			}
		}))
		out, _, err := execute(t, server.URL, append([]string{"logs", "api", "--follow", "--interval", "1ms"}, c.flags...)...)
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "Forbidden") {
			t.Fatal(err)
		}
		if out != c.want {
			t.Errorf("%v: got %q want %q", c.flags, out, c.want)
		}
		if queries[0] != "tailLines=100&timestamps=true" {
			t.Fatal(queries)
		}
	}
	if got := withoutTimestamps("2026-10-03T12:00:01.123456789Z a b\nnot a time\n"); got != "a b\nnot a time\n" {
		t.Fatalf("%q", got)
	}
}

// One read prints Edka's note on stderr, so the log on stdout stays a log.
func TestLogsNoteGoesToStderr(t *testing.T) {
	server, _ := sequenceAPI(t, map[string][]string{
		"GET /api/deployments":         {`{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`},
		"GET /api/deployments/d1/logs": {`{"logs":"No previous container logs available. The pod has not been restarted.","podName":"api-a","parameters":{"previous":true,"noPreviousLogs":true}}`},
	})
	out, errOut, err := execute(t, server.URL, "logs", "api", "--previous")
	if err != nil || out != "" || !strings.Contains(errOut, "The pod has not been restarted.") {
		t.Fatalf("out=%q err=%q %v", out, errOut, err)
	}
}

// A container's last line can lack a line break, and a log can be that one
// line. It stays on stdout: only what Edka marks or words as a note is one.
func TestLogsLineWithoutLineBreakIsLog(t *testing.T) {
	fastPolls(t)
	interval := minLogInterval
	minLogInterval = time.Millisecond
	t.Cleanup(func() { minLogInterval = interval })
	for _, args := range [][]string{{"logs", "api"}, {"logs", "api", "--follow", "--interval", "1ms"}} {
		server, _ := sequenceAPI(t, map[string][]string{
			"GET /api/deployments": {`{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`},
			"GET /api/deployments/d1/logs": {
				`{"logs":"fatal: configuration missing","podName":"api-a"}`,
				`403 {"error":"Forbidden"}`,
			},
		})
		out, errOut, err := execute(t, server.URL, args...)
		if len(args) == 2 && err != nil || len(args) > 2 && (err == nil || !strings.Contains(err.Error(), "Forbidden")) {
			t.Fatal(args, err)
		}
		if out != "fatal: configuration missing\n" || strings.Contains(errOut, "fatal") {
			t.Fatalf("%v: out=%q err=%q", args, out, errOut)
		}
	}
	for body, note := range map[string]bool{
		`{"logs":"Container is still starting and has not produced logs yet."}`:                     true,
		`{"logs":"Container is not producing logs because it is currently ContainerCreating."}`:     true,
		`{"logs":"No previous container logs available.","parameters":{"noPreviousLogs":true}}`:     true,
		`{"logs":"Container is still starting and has not produced logs yet.\nnext","podName":"a"}`: false,
		`{"logs":"Error: listen EADDRINUSE :::8080","parameters":{"previous":false}}`:               false,
	} {
		read, err := readLog([]byte(body))
		if err != nil || (read.note != "") != note || (read.log != "") == note {
			t.Errorf("%s: %+v %v", body, read, err)
		}
	}
}
func TestOutputFilePermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "response.json")
	out, errOut, err := execute(t, server.URL, "api", "get", "/api/clusters", "--output-file", path)
	if err != nil || out != "" || !strings.Contains(errOut, "✓ Saved "+path) {
		t.Fatalf("out=%q err=%q %v", out, errOut, err)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestUpRoutesImageSettingsAndGitRedeploy(t *testing.T) {
	for _, tc := range []struct {
		git          bool
		fields       bool
		method, path string
	}{{false, false, "POST", "/api/deployments/d1/restart"}, {false, true, "PATCH", "/api/deployments/d1/settings"}, {true, false, "POST", "/api/deployments/d1/deploy"}} {
		t.Run(tc.path, func(t *testing.T) {
			writes := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/deployments":
					fmt.Fprint(w, `{"data":[{"id":"d1","name":"api"}]}`)
				case r.Method == "GET" && r.URL.Path == "/api/deployments/d1":
					git := "null"
					if tc.git {
						git = `"g1"`
					}
					fmt.Fprintf(w, `{"data":{"id":"d1","github_deployment_id":%s}}`, git)
				default:
					writes = append(writes, r.Method+" "+r.URL.Path)
					fmt.Fprint(w, `{"generation":2}`)
				}
			}))
			defer server.Close()
			args := []string{"up", "api", "--json"}
			if tc.fields {
				args = append(args, "--field", "config.image_tag=v2")
			}
			_, _, err := execute(t, server.URL, args...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(writes, ",") != tc.method+" "+tc.path {
				t.Fatal(writes)
			}
		})
	}
}
func TestWaitRejectsPreviousHealthyRollout(t *testing.T) {
	fastPolls(t)
	t.Setenv("EDKA_TOKEN", "test-token")
	reads := 0
	runtimeReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/deployments/d1/status" {
			runtimeReads++
			if reads < 2 {
				t.Error("accepted old runtime status")
			}
			fmt.Fprint(w, `{"data":{"status":"deployed"}}`)
			return
		}
		reads++
		if reads == 1 {
			fmt.Fprint(w, `{"data":{"spec_generation":2,"applied_generation":1,"healthy_generation":1,"status":"deploying"}}`)
		} else {
			fmt.Fprint(w, `{"data":{"spec_generation":2,"applied_generation":2,"healthy_generation":2,"status":"deployed"}}`)
		}
	}))
	defer server.Close()
	var out bytes.Buffer
	a := App{current: config.Profile{APIURL: server.URL}, Out: &out, Err: &bytes.Buffer{}, HTTP: server.Client(), output: "json"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := a.waitDeployment(ctx, "d1", "api", 2); err != nil {
		t.Fatal(err)
	}
	if reads != 2 || runtimeReads != 1 {
		t.Fatalf("reads=%d runtime=%d", reads, runtimeReads)
	}
}
func TestWaitFailsWhenRequestedRevisionIsReplaced(t *testing.T) {
	fastPolls(t)
	t.Setenv("EDKA_TOKEN", "test-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"spec_generation":3,"applied_generation":3,"healthy_generation":3,"status":"deployed"}}`)
	}))
	defer server.Close()
	a := App{current: config.Profile{APIURL: server.URL}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, HTTP: server.Client()}
	_, err := a.waitDeployment(context.Background(), "d1", "api", 2)
	if err == nil || !strings.Contains(err.Error(), "replaced by generation 3") {
		t.Fatal(err)
	}
}
func TestMissingLinkedProfileAllowsRepair(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	link := `{"version":1,"profile":"teammate","api_url":"https://api.edka.io","organization":"o1","cluster":"c1"}`
	if err := os.WriteFile(filepath.Join(dir, config.LinkName), []byte(link), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"version"}, {"context", "--json"}, {"profile", "list"}, {"help", "status"}, {"api", "operations", "--search", "nodepools"}, {"profile"}, {"api"}, {"apps"}} {
		if _, _, err := execute(t, "https://api.edka.io", args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, _, err := execute(t, "https://api.edka.io", "logout", "--profile", "typo"); err == nil {
		t.Fatal("signed out of a profile that does not exist")
	}
	_, _, err := execute(t, "https://api.edka.io", "status")
	if err == nil || !strings.Contains(err.Error(), config.LinkName) || !strings.Contains(err.Error(), "edka unlink") {
		t.Fatal(err)
	}
	if _, _, err := execute(t, "https://api.edka.io", "unlink"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, config.LinkName)); !os.IsNotExist(err) {
		t.Fatal("link was not removed")
	}
}

// doctorChecks runs `doctor --json` and returns each check's status and detail.
func doctorChecks(t *testing.T, base string) (map[string][2]string, error) {
	t.Helper()
	out, _, err := execute(t, base, "doctor", "--json")
	var rows []map[string]string
	if jsonErr := json.Unmarshal([]byte(out), &rows); jsonErr != nil {
		t.Fatal(out, jsonErr)
	}
	checks := map[string][2]string{}
	for _, row := range rows {
		checks[row["name"]] = [2]string{row["status"], row["detail"]}
	}
	return checks, err
}

// A project link that can't be read blocks commands that use it, but not help,
// the commands that need no context, or the `unlink` that removes it.
func TestCorruptLinkAllowsRepair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"email":"you@example.com","organization":{"id":"o1","name":"Acme"}}}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, config.LinkName)
	for _, link := range []string{`{"version":1,`, `{"version":2}`} {
		if err := os.WriteFile(path, []byte(link), 0600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"version"}, {"completion", "bash"}, {"help", "status"}, {"api", "operations", "--search", "nodepools"}, {"profile"}, {"profile", "list"}, {"profile", "use", "default"}, {}} {
			if _, _, err := execute(t, server.URL, args...); err != nil {
				t.Errorf("%s: %v: %v", link, args, err)
			}
		}
		for _, args := range [][]string{{"whoami"}, {"context"}} {
			_, _, err := execute(t, server.URL, args...)
			if err == nil || !strings.Contains(err.Error(), config.LinkName) || !strings.Contains(err.Error(), "edka unlink") {
				t.Errorf("%s: %v: %v", link, args, err)
			}
		}
		// Doctor names the link and still checks what does not depend on it.
		checks, err := doctorChecks(t, server.URL)
		if err == nil || checks["Project link"][0] != "failed" || !strings.Contains(checks["Project link"][1], config.LinkName) || checks["Configuration and profile"][0] != "ok" || checks["Identity and organization"][0] != "ok" {
			t.Errorf("%s: %v %v", link, checks, err)
		}
		if _, _, err := execute(t, server.URL, "unlink"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("link was not removed")
		}
		if _, _, err := execute(t, server.URL, "whoami"); err != nil {
			t.Fatal(err)
		}
	}
}

// A config that can't be read is a failed doctor check, and leaves the commands
// that need no context working.
func TestCorruptConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"version":1,`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		t.Setenv("EDKA_CONFIG_DIR", dir)
		var out bytes.Buffer
		root := New("test", strings.NewReader(""), &out, &bytes.Buffer{})
		root.SetArgs(args)
		err := root.ExecuteContext(context.Background())
		return out.String(), err
	}
	for _, args := range [][]string{{"version"}, {"completion", "zsh"}, {"help"}, {"unlink"}} {
		if _, err := run(args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"context"}, {"profile", "list"}} {
		if _, err := run(args...); err == nil || !strings.Contains(err.Error(), "invalid config") {
			t.Fatal(args, err)
		}
	}
	out, err := run("doctor", "--json")
	var rows []map[string]string
	if json.Unmarshal([]byte(out), &rows) != nil || err == nil || len(rows) != 1 || rows[0]["name"] != "Configuration and profile" || rows[0]["status"] != "failed" || !strings.Contains(rows[0]["detail"], "invalid config") {
		t.Fatal(out, err)
	}
}

// The table shows each check's status; a failure's reason follows it.
func TestDoctorTableExplainsFailures(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, config.LinkName), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"email":"you@example.com","organization":{"id":"o1","name":"Acme"}}}`)
	}))
	defer server.Close()
	out, errOut, err := execute(t, server.URL, "doctor", "--color", "never")
	if err == nil || !strings.Contains(out, "Project link") || !strings.Contains(errOut, "Project link: invalid ") || !strings.Contains(errOut, "edka unlink") || strings.Contains(errOut, "Identity and organization") {
		t.Fatal(out, errOut, err)
	}
}
func TestGeneratedCommandArguments(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method == "GET" && r.URL.Path == "/api/clusters" {
			fmt.Fprint(w, `{"data":[{"id":"c1","name":"production"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer server.Close()
	// The single-cluster route owns `clusters delete`; bulk deletion has its own name.
	if _, _, err := execute(t, server.URL, "api", "clusters", "delete", "production", "--yes", "--json"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(requests, ","); got != "GET /api/clusters,DELETE /api/clusters/c1" {
		t.Fatal(got)
	}
	requests = nil
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"api", "clusters", "delete-many", "--cluster", "production", "--yes"}, "--cluster does not apply"},
		{[]string{"api", "clusters", "list", "production"}, `"production"`},
		{[]string{"api", "deployments", "get", "d1", "--deployment", "api"}, "--deployment does not apply"},
		{[]string{"api", "clusters", "get", "production", "--cluster", "staging"}, "not both"},
		{[]string{"api", "clusters", "databases", "get", "db1", "--database-id", "db2", "--cluster", "production"}, "not both"},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	if len(requests) != 0 {
		t.Fatalf("invalid arguments reached the API: %v", requests)
	}
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	for command, use := range map[string]string{"clusters list": "list", "clusters get": "get [cluster]", "clusters databases get": "get [database-id]", "deployments builds get": "get [build-id]"} {
		cmd, _, err := root.Find(append([]string{"api"}, strings.Fields(command)...))
		if err != nil || cmd.Use != use {
			t.Errorf("%s: use %q, want %q (%v)", command, cmd.Use, use, err)
		}
	}
}
func TestDisruptiveOperationsRequireConfirmation(t *testing.T) {
	raw := 0
	rawServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw++
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer rawServer.Close()
	for _, path := range []string{"/api/clusters/c1/unprotect", "/api/clusters/c1/unprotect?reason=test", "/api/clusters/c1/Unprotect"} {
		if _, _, err := execute(t, rawServer.URL, "api", "post", path); err == nil || !strings.Contains(err.Error(), "--yes") || raw != 0 {
			t.Fatalf("%s: requests=%d error=%v", path, raw, err)
		}
	}
	if _, _, err := execute(t, rawServer.URL, "api", "post", "/api/clusters/c1/unprotect?reason=test", "--yes"); err != nil || raw != 1 {
		t.Fatalf("requests=%d error=%v", raw, err)
	}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/deployments":
			fmt.Fprint(w, `{"data":[{"id":"d1","name":"api"}]}`)
		default:
			writes++
			fmt.Fprint(w, `{"data":{}}`)
		}
	}))
	defer server.Close()
	_, _, err := execute(t, server.URL, "scale", "api", "--replicas", "0")
	if err == nil || !strings.Contains(err.Error(), "--yes") || writes != 0 {
		t.Fatalf("writes=%d error=%v", writes, err)
	}
	if _, _, err := execute(t, server.URL, "scale", "api", "--replicas", "2"); err != nil || writes != 1 {
		t.Fatalf("writes=%d error=%v", writes, err)
	}
}
func TestLogTailMatchesServerLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid tail reached the API") }))
	defer server.Close()
	_, _, err := execute(t, server.URL, "logs", "api", "--tail", "2001")
	if err == nil || !strings.Contains(err.Error(), "2000") {
		t.Fatal(err)
	}
}
