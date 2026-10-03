package cli

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/config"
)

// Cobra ends a completion with one of these directives.
const (
	shellFiles       = ":0"
	shellNoFiles     = ":4"
	shellDirectories = ":16"
)

// completions asks for the completions of a command line, as a shell does, and
// returns them with Cobra's directive.
func completions(t *testing.T, base string, line ...string) ([]string, string) {
	t.Helper()
	out, _, err := execute(t, base, append([]string{"__complete"}, line...)...)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[:len(lines)-1], lines[len(lines)-1]
}

func TestCompletionOffersNames(t *testing.T) {
	fixtures := maps.Clone(appFixtures)
	fixtures["GET /api/clusters/c1/apps/catalog"] = `{"data":[{"id":"p1","name":"Memos","slug":"memos","category":"notes"}]}`
	fixtures["GET /api/addons/catalog"] = `{"data":[{"name":"cert-manager","category":"security"},{"name":"reflector","category":"security"}]}`
	fixtures["GET /api/clusters/c1/addons"] = `{"data":[{"id":"x1","addon_name":"cert-manager","version":"1.16.2","status":"installed"}]}`
	server, _ := fakeAPI(t, fixtures)
	for _, test := range []struct {
		line string
		want []string
	}{
		{"logs ", []string{"api\tdeployed · sinaia · ghcr.io/acme/api:v2", "worker\tdeploying · staging · ghcr.io/acme/worker:latest"}},
		{"logs --cluster sinaia ", []string{"api\tdeployed · sinaia · ghcr.io/acme/api:v2"}},
		{"deployments restart w", []string{"worker\tdeploying · staging · ghcr.io/acme/worker:latest"}},
		{"build ", []string{"api\tdeployed · sinaia · ghcr.io/acme/api:v2", "worker\tdeploying · staging · ghcr.io/acme/worker:latest"}},
		{"status --deployment a", []string{"api\tdeployed · sinaia · ghcr.io/acme/api:v2"}},
		// A command takes one name.
		{"logs api ", nil},
		{"clusters get ", []string{"sinaia\tc1", "staging\tc2"}},
		{"apps list --cluster st", []string{"staging\tc2"}},
		{"apps get ", []string{"blog\tstrapi 5.46.0 · sinaia", "Whiteboard\texcalidraw 0.18.1 · staging"}},
		{"addons uninstall --cluster sinaia ", []string{"cert-manager\t1.16.2 · installed · sinaia"}},
		{"addons install r", []string{"reflector\tsecurity"}},
		{"addons catalog --category ", []string{"security"}},
		// Apps install from the cluster's catalog, or from Edka's when no cluster is in context.
		{"apps install ", []string{"strapi\tStrapi"}},
		{"apps install --cluster sinaia ", []string{"memos\tMemos"}},
		{"apps catalog --category ", []string{"cms"}},
	} {
		got, directive := completions(t, server.URL, strings.Split(test.line, " ")...)
		if strings.Join(got, "\n") != strings.Join(test.want, "\n") || directive != shellNoFiles {
			t.Errorf("edka %s<TAB> offered %q %s, want %q", test.line, got, directive, test.want)
		}
	}
}

func TestCompletionOffersFixedValuesAndCommands(t *testing.T) {
	for _, test := range []struct {
		line string
		want string
	}{
		{"--output ", "table,json,raw"},
		{"logs --color ", "auto,always,never"},
		{"--credential-store ", "auto,keyring,file"},
		{"completion ", "bash,zsh,fish,powershell"},
		{"help cronjobs s", "suspend\tStop scheduled runs"},
	} {
		got, directive := completions(t, "http://127.0.0.1:1", strings.Split(test.line, " ")...)
		if strings.Join(got, ",") != test.want || directive != shellNoFiles {
			t.Errorf("edka %s<TAB> offered %q %s, want %s", test.line, got, directive, test.want)
		}
	}
}

// A shell offers file names unless a completion says otherwise. Only the
// arguments and flags that take a path leave it to do so.
func TestCompletionOffersFileNamesOnlyForPaths(t *testing.T) {
	for line, want := range map[string]string{
		"status ":                              shellNoFiles,
		"logs --tail ":                         shellNoFiles,
		"scale api --replicas ":                shellNoFiles,
		"api clusters nodepools list ":         shellNoFiles,
		"api get ":                             shellNoFiles,
		"apps init ":                           shellNoFiles,
		"apps init memos --dir ":               shellFiles,
		"clusters kubeconfig x --output-file ": shellFiles,
		"api clusters create --data ":          shellFiles,
		"run -- ":                              shellFiles,
		"apps validate ":                       shellDirectories,
		"apps publish ":                        shellDirectories,
		"apps share ":                          shellDirectories,
	} {
		got, directive := completions(t, "http://127.0.0.1:1", strings.Split(line, " ")...)
		if directive != want || len(got) > 0 && want != shellNoFiles {
			t.Errorf("edka %s<TAB> offered %q %s, want %s", line, got, directive, want)
		}
	}
}

func TestCompletionIsSilentWhenAReadFails(t *testing.T) {
	server, requests := fakeAPI(t, map[string]string{"GET /api/clusters": `401 {"error":"Unauthorized"}`})
	out, errOut, err := execute(t, server.URL, "__complete", "clusters", "get", "")
	if err != nil || out != shellNoFiles+"\n" || errOut != "Completion ended with directive: ShellCompDirectiveNoFileComp\n" {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
	if len(*requests) != 1 {
		t.Fatal(*requests)
	}
}

func TestCompletionStopsWaitingForEdka(t *testing.T) {
	previous := completionTimeout
	completionTimeout = 50 * time.Millisecond
	t.Cleanup(func() { completionTimeout = previous })
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(server.Close)
	started := time.Now()
	got, directive := completions(t, server.URL, "--cluster", "")
	if len(got) > 0 || directive != shellNoFiles || time.Since(started) > 5*time.Second {
		t.Fatal(got, directive, time.Since(started))
	}
}

// A shell reads one completion per line, so a name that would add a line or
// move the cursor is replaced by its ID.
func TestCompletionLeavesOutControlCharacters(t *testing.T) {
	server, _ := fakeAPI(t, map[string]string{"GET /api/clusters": `{"data":[{"id":"c9","name":"bad\u001b[31m\nname","status":"act\tive"},{"id":"c1","name":"sinaia","status":"active"}]}`})
	got, _ := completions(t, server.URL, "--cluster", "")
	if strings.Join(got, "\n") != "c9\tactive\nsinaia\tactive" {
		t.Fatalf("%q", got)
	}
}

func TestCompletionOffersProfilesWithoutTheOneInContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDKA_CONFIG_DIR", dir)
	t.Setenv("EDKA_PROFILE", "gone")
	c, _ := config.Load(dir)
	c.Profiles["work"] = config.Profile{APIURL: "https://api.edka.io", OrganizationName: "Acme"}
	if err := config.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	for _, line := range [][]string{{"profile", "use", ""}, {"profile", "remove", ""}, {"logs", "--profile", ""}} {
		var out bytes.Buffer
		root := New("test", strings.NewReader(""), &out, &bytes.Buffer{})
		root.SetArgs(append([]string{"__complete"}, line...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if out.String() != "default\nwork\tAcme\n"+shellNoFiles+"\n" {
			t.Fatalf("%v: %q", line, out.String())
		}
	}
}

func TestHelpBelowTheRootListsTheCommonGlobalFlags(t *testing.T) {
	rare := []string{"--api-url", "--color", "--credential-store", "--debug", "--organization", "--timeout"}
	common := []string{"--cluster", "--deployment", "--json", "--no-input", "--output", "--profile", "--yes"}
	footer := "Use `edka --help` for the other global flags.\n"
	for _, line := range [][]string{{"logs", "--help"}, {"help", "deployments"}, {"api", "clusters", "nodepools", "list", "-h"}, {"clusters"}} {
		out, _, err := execute(t, "http://127.0.0.1:1", line...)
		if err != nil || !strings.HasSuffix(out, "\n\n"+footer) {
			t.Fatalf("%v: %v\n%s", line, err, out)
		}
		for _, flag := range rare {
			if strings.Contains(out, flag+" ") {
				t.Errorf("%v lists %s", line, flag)
			}
		}
		for _, flag := range common {
			if !strings.Contains(out, flag) {
				t.Errorf("%v does not list %s", line, flag)
			}
		}
	}
	for _, line := range [][]string{{"--help"}, {"help"}, {}} {
		out, _, err := execute(t, "http://127.0.0.1:1", line...)
		if err != nil || strings.Contains(out, footer) {
			t.Fatalf("%v: %v\n%s", line, err, out)
		}
		for _, flag := range append(rare, common...) {
			if !strings.Contains(out, flag) {
				t.Errorf("%v does not list %s", line, flag)
			}
		}
	}
}

// Help hides a flag only while it prints, so the flag still parses and still
// completes after it.
func TestGlobalFlagsLeftOutOfHelpStillWork(t *testing.T) {
	var out bytes.Buffer
	root := New("test", strings.NewReader(""), &out, &bytes.Buffer{})
	root.SetArgs([]string{"logs", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range rareGlobalFlags {
		if root.PersistentFlags().Lookup(name).Hidden {
			t.Errorf("--%s stayed hidden", name)
		}
	}
	if out, _, err := execute(t, "http://127.0.0.1:1", "version", "--timeout", "5s", "--color", "never", "--credential-store", "file"); err != nil || !strings.HasPrefix(out, "edka test\n") {
		t.Fatal(out, err)
	}
	if _, _, err := execute(t, "http://127.0.0.1:1", "version", "--timeout", "0s"); err == nil || !strings.Contains(err.Error(), "timeout must be positive") {
		t.Fatal(err)
	}
	if got, _ := completions(t, "http://127.0.0.1:1", "logs", "--timeo"); strings.Join(got, ",") != "--timeout\tHTTP request timeout" {
		t.Fatalf("%q", got)
	}
}
