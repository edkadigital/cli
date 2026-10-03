package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// diffRecord is a deployment as Edka returns it: the config holds the columns
// it mirrors, and a secret has a name and no value.
const diffRecord = `{"data":{"id":"d1","name":"api","namespace":"prod","spec_generation":5,"auto_rollback_enabled":true,"target_cpu_percentage":"80","expose_via_ingress":true,"hostname":"api.acme.dev","hostnames":["api.acme.dev"],"config":{"name":"api","expose_via_ingress":true,"hostname":"api.acme.dev","image_repository":"ghcr.io/acme/api","image_tag":"v2","image_digest":"sha256:0f1e","replicas":2,"port":8080,"hostnames":["api.acme.dev"],"env_variables":[{"name":"PORT","value":"8080"},{"name":"OLD_FLAG","value":"1"},{"name":"KEEP","value":"x"}],"secrets":[{"name":"DATABASE_URL","value":""}],"volumes":[{"type":"pvc","mountPath":"/data","size":"10Gi"}]}}}`

const diffRequest = `{
	"name": "other",
	"image_tag": "v3",
	"auto_rollback_enabled": false,
	"target_cpu_percentage": 80,
	"hostnames": ["api.acme.dev", "API.acme.io"],
	"secret_values": {"API_KEY": "s3cr3t-value"},
	"config": {
		"image_tag": "v3",
		"port": 8080,
		"env_variables": [{"name":"PORT","value":"9090"},{"name":"KEEP","value":"x"},{"name":"LOG_LEVEL","value":"debug"}],
		"secrets": [{"name":"DATABASE_URL","value":""},{"name":"API_KEY","value":"s3cr3t-value"}],
		"volumes": [{"type":"pvc","mountPath":"/data","size":"20Gi"}]
	}
}`

func diffAPI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	return fakeAPI(t, map[string]string{
		"GET /api/deployments":    `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`,
		"GET /api/deployments/d1": diffRecord,
	})
}

func TestUpDiffListsWhatASettingsRequestChanges(t *testing.T) {
	server, requests := diffAPI(t)
	out, errOut, err := execute(t, server.URL, "up", "api", "--data", diffRequest, "--diff")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, out,
		"api at generation 5: 8 changes. Nothing was sent.\n",
		"config.env_variables", "+ LOG_LEVEL=debug\n", "~ PORT: 8080 → 9090\n", "- OLD_FLAG\n",
		"config.image_tag", "v2 → v3\n",
		"config.secrets", "+ API_KEY\n",
		"config.volumes", `~ /data: {"size":"10Gi","type":"pvc"} → {"size":"20Gi","type":"pvc"}`,
		"config.image_digest", "sha256:0f1e → (not set) (the image is no longer pinned to a digest)\n",
		"auto_rollback_enabled", "true → false\n",
		"hostnames", "+ api.acme.io\n",
		"secret_values", "~ API_KEY (values not shown)\n",
		// A column Edka returns as text equals the number the request sends.
		"Unchanged: config.port, target_cpu_percentage\n",
		"Ignored: image_tag: the settings route does not read it; send config.image_tag\n",
		"Refused: name: a deployment keeps its name, api\n",
	)
	if !strings.Contains(errOut, "run the command without --diff and with --expected-generation 5") {
		t.Fatal(errOut)
	}
	if strings.Contains(out+errOut, "s3cr3t-value") {
		t.Fatalf("a secret value is printed:\n%s%s", out, errOut)
	}
	for _, request := range *requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("--diff sent %s", request)
		}
	}

	out, _, err = execute(t, server.URL, "up", "api", "--data", diffRequest, "--diff", "--json")
	var d settingsDiff
	if err != nil || json.Unmarshal([]byte(out), &d) != nil || d.Generation != 5 || len(d.Changes) != 8 || len(d.Ignored) != 1 || len(d.Refused) != 1 || strings.Contains(out, "s3cr3t-value") {
		t.Fatal(out, err)
	}
	for _, c := range d.Changes {
		if c.Field == "config.env_variables" && (len(c.Added) != 1 || len(c.Changed) != 1 || len(c.Removed) != 1) {
			t.Fatalf("env_variables: %+v", c)
		}
	}
}

func TestUpDiffOfARequestThatChangesNothing(t *testing.T) {
	server, _ := diffAPI(t)
	out, errOut, err := execute(t, server.URL, "up", "api", "--field", "config.image_tag=v2", "--field", "config.replicas:=2", "--diff")
	if err != nil || out != "api at generation 5: no change. Nothing was sent.\n\nUnchanged: config.image_tag, config.replicas\n" || errOut != "" {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
}

func TestUpDiffNeedsASettingsChangeToADeployment(t *testing.T) {
	server := silentAPI(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"up", "api", "--diff"}, "--diff is for a settings change; pass --data or --field"},
		{[]string{"up", "--field", "name=web", "--diff"}, "--diff is for a change to an existing deployment"},
		{[]string{"up", "api", "--expected-generation", "5"}, "--expected-generation is for a settings change"},
		{[]string{"up", "api", "--field", "config.image_tag=v3", "--expected-generation", "0"}, "expected-generation must be positive"},
		{[]string{"up", "api", "--field", "expected_generation:=4", "--expected-generation", "5"}, "the body names expected_generation 4 and --expected-generation 5"},
	} {
		if _, _, err := execute(t, server.URL, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
}

func TestUpSendsTheExpectedGeneration(t *testing.T) {
	sent, answer := "", `{"generation":6}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/deployments":
			fmt.Fprint(w, `{"data":[{"id":"d1","name":"api"}]}`)
		case r.Method == "PATCH" && r.URL.Path == "/api/deployments/d1/settings":
			body, _ := io.ReadAll(r.Body)
			sent = string(body)
			if strings.HasPrefix(answer, "409 ") {
				w.WriteHeader(http.StatusConflict)
			}
			fmt.Fprint(w, strings.TrimPrefix(answer, "409 "))
		default:
			t.Errorf("sent %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	if _, _, err := execute(t, server.URL, "up", "api", "--field", "config.image_tag=v3", "--expected-generation", "5", "--json"); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal([]byte(sent), &body) != nil || body["expected_generation"] != float64(5) || body["config"].(map[string]any)["image_tag"] != "v3" {
		t.Fatalf("sent %q", sent)
	}
	// Without the flag the body names no generation.
	if _, _, err := execute(t, server.URL, "up", "api", "--field", "config.image_tag=v3", "--json"); err != nil || strings.Contains(sent, "expected_generation") {
		t.Fatal(sent, err)
	}
	answer = `409 {"error":"Change refused","message":"The deployment changed after this change was prepared. Read it again and save."}`
	_, _, err := execute(t, server.URL, "up", "api", "--field", "config.image_tag=v3", "--expected-generation", "5")
	if err == nil || !strings.Contains(err.Error(), "The deployment changed after this change was prepared") || !strings.Contains(err.Error(), "api is no longer at generation 5; compare the change again with --diff") {
		t.Fatal(err)
	}
}

func TestUpDiffNamesTheSecretValuesOfAList(t *testing.T) {
	server, _ := diffAPI(t)
	// Edka reads a list of {name, value} as it reads an object of values by name.
	values := `"secret_values":[{"name":"DATABASE_URL","value":"s3cr3t-value"},{"name":" API_KEY ","value":""},{"name":"NO_VALUE"},{"value":"s3cr3t-value"},"s3cr3t-value"]`
	out, errOut, err := execute(t, server.URL, "up", "api", "--data", `{`+values+`,"config":{"secrets":[{"name":"DATABASE_URL"},{"name":"API_KEY"}]}}`, "--diff")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, out,
		"api at generation 5: 2 changes. Nothing was sent.\n",
		"config.secrets", "+ API_KEY\n",
		"secret_values", "~ API_KEY\n", "~ DATABASE_URL (values not shown)\n",
	)
	if strings.Contains(out, "NO_VALUE") || strings.Contains(out, "Ignored") || strings.Contains(out+errOut, "s3cr3t-value") {
		t.Fatalf("%s%s", out, errOut)
	}
	if !strings.Contains(errOut, "--expected-generation 5") {
		t.Fatal(errOut)
	}

	// Edka writes no value for a name the deployment has no secret of.
	out, errOut, err = execute(t, server.URL, "up", "api", "--data", `{`+values+`}`, "--diff")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, out,
		"api at generation 5: 1 change. Nothing was sent.\n",
		"secret_values", "~ DATABASE_URL (values not shown)\n",
		"Ignored: secret_values.API_KEY: the deployment has no secret of that name; add it to config.secrets\n",
	)
	out, errOut, err = execute(t, server.URL, "up", "api", "--data", `{"secret_values":{"API_KEY":"s3cr3t-value"}}`, "--diff")
	if err != nil || !strings.Contains(out, "api at generation 5: no change. Nothing was sent.\n") || !strings.Contains(out, "Ignored: secret_values.API_KEY: ") || errOut != "" {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
}

func TestUpDiffRefusesANewSecretWithoutAValue(t *testing.T) {
	server, _ := diffAPI(t)
	out, errOut, err := execute(t, server.URL, "up", "api", "--data", `{"secret_values":{"TOKEN":"s3cr3t-value","API_KEY":null},"config":{"secrets":[{"name":"DATABASE_URL"},{"name":"API_KEY","value":"s3cr3t-value"},{"name":"TOKEN"}]}}`, "--diff")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, out,
		"api at generation 5: 2 changes. Nothing was sent.\n",
		"config.secrets", "+ TOKEN\n",
		"secret_values", "~ TOKEN (values not shown)\n",
		"Refused: config.secrets: API_KEY is a new secret and has no value; send secret_values.API_KEY\n",
	)
	if strings.Contains(out, "+ API_KEY") || strings.Contains(out+errOut, "s3cr3t-value") {
		t.Fatalf("%s%s", out, errOut)
	}

	// A list whose only change is refused is neither changed nor unchanged.
	out, errOut, err = execute(t, server.URL, "up", "api", "--data", `{"config":{"secrets":[{"name":"DATABASE_URL"},{"name":"API_KEY"}]}}`, "--diff")
	if err != nil || out != "api at generation 5: no change. Nothing was sent.\n\nRefused: config.secrets: API_KEY is a new secret and has no value; send secret_values.API_KEY\n" || errOut != "" {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
}

func TestUpDiffShowsTheHostnamesAnUnexposedDeploymentLoses(t *testing.T) {
	server, _ := diffAPI(t)
	out, errOut, err := execute(t, server.URL, "up", "api", "--field", "config.expose_via_ingress:=false", "--diff")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, out,
		"api at generation 5: 2 changes. Nothing was sent.\n",
		"config.expose_via_ingress", "true → false\n",
		"config.hostnames", "- api.acme.dev (a deployment that is not exposed keeps no hostnames)\n",
	)
}

func TestDiffSettingsKeepsNoHostnameOfAnUnexposedDeployment(t *testing.T) {
	exposed := `{"name":"api","expose_via_ingress":true,"hostname":"api.acme.dev","hostnames":["api.acme.dev","b.acme.dev"],"config":{"expose_via_ingress":true,"hostname":"api.acme.dev","hostnames":["api.acme.dev","b.acme.dev"]}}`
	unexposed := `{"name":"api","expose_via_ingress":false,"hostname":null,"hostnames":[],"config":{"expose_via_ingress":false}}`
	dropped := ": a deployment that is not exposed keeps no hostnames"
	needed := ": an exposed deployment needs a hostname; "
	for _, tc := range []struct {
		record, body string
		changes      string
		unchanged    string
		ignored      string
		refused      string
	}{
		// The hostnames the request sends are removed with the rest.
		{record: exposed, body: `{"config":{"expose_via_ingress":false,"hostnames":["api.acme.dev","b.acme.dev"]}}`, changes: "config.expose_via_ingress config.hostnames -api.acme.dev,b.acme.dev", ignored: "config.hostnames" + dropped},
		{record: exposed, body: `{"hostname":"new.acme.dev","config":{"expose_via_ingress":false}}`, changes: "config.expose_via_ingress hostnames -api.acme.dev,b.acme.dev", ignored: "hostnames" + dropped},
		{record: unexposed, body: `{"config":{"hostnames":["api.acme.dev"]}}`, ignored: "config.hostnames" + dropped + "; send config.expose_via_ingress true"},
		{record: unexposed, body: `{"config":{"expose_via_ingress":false,"hostnames":[]}}`, unchanged: "config.expose_via_ingress, config.hostnames"},
		{record: unexposed, body: `{"config":{"expose_via_ingress":true,"hostnames":["api.acme.dev"]}}`, changes: "config.expose_via_ingress config.hostnames +api.acme.dev"},
		{record: exposed, body: `{"config":{"expose_via_ingress":true,"hostnames":["api.acme.dev","b.acme.dev"]}}`, unchanged: "config.expose_via_ingress, config.hostnames"},
		// Edka refuses a config that exposes the deployment and names no
		// hostname, even when hostnames are stored or at the top level.
		{record: unexposed, body: `{"config":{"expose_via_ingress":true}}`, refused: "config.expose_via_ingress" + needed + "send config.hostnames with it"},
		{record: exposed, body: `{"config":{"expose_via_ingress":true,"hostnames":[]}}`, refused: "config.expose_via_ingress" + needed + "send config.hostnames with it"},
		{record: exposed, body: `{"hostnames":["api.acme.dev","b.acme.dev"],"config":{"expose_via_ingress":true}}`, unchanged: "hostnames", refused: "config.expose_via_ingress" + needed + "send config.hostnames with it"},
		{record: unexposed, body: `{"expose_via_ingress":true}`, refused: "expose_via_ingress" + needed + "send config.expose_via_ingress with config.hostnames"},
		// And one that leaves an exposed deployment no hostname.
		{record: exposed, body: `{"config":{"hostnames":[]}}`, refused: "config.hostnames" + needed + "send config.expose_via_ingress false to stop exposing it"},
		{record: exposed, body: `{"hostname":null,"hostnames":[]}`, refused: "hostnames" + needed + "send config.expose_via_ingress false to stop exposing it"},
	} {
		var record, body map[string]any
		if err := json.Unmarshal([]byte(tc.record), &record); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
			t.Fatal(err)
		}
		d := diffSettings(record, body, nil, nil)
		changes := []string{}
		for _, c := range d.Changes {
			entry := c.Field
			if len(c.Added) > 0 {
				entry += " +" + strings.Join(c.Added, ",")
			}
			if len(c.Removed) > 0 {
				entry += " -" + strings.Join(c.Removed, ",")
			}
			changes = append(changes, entry)
		}
		if got := strings.Join(changes, " "); got != tc.changes {
			t.Errorf("%s: changes %q, want %q", tc.body, got, tc.changes)
		}
		if got := strings.Join(d.Unchanged, ", "); got != tc.unchanged {
			t.Errorf("%s: unchanged %q, want %q", tc.body, got, tc.unchanged)
		}
		if got := strings.Join(d.Ignored, " | "); got != tc.ignored {
			t.Errorf("%s: ignored %q, want %q", tc.body, got, tc.ignored)
		}
		if got := strings.Join(d.Refused, " | "); got != tc.refused {
			t.Errorf("%s: refused %q, want %q", tc.body, got, tc.refused)
		}
	}
}

func TestDiffSettingsReadsHostnamesAsEdkaDoes(t *testing.T) {
	record := map[string]any{"name": "api", "expose_via_ingress": true, "hostname": "api.acme.dev", "config": map[string]any{}}
	for _, tc := range []struct {
		body string
		want string
	}{
		// One hostname replaces the list.
		{`{"hostname":"new.acme.dev"}`, "hostnames +new.acme.dev -api.acme.dev"},
		// `hostname` leads `hostnames`, and a repeat counts once.
		{`{"hostname":"api.acme.dev","hostnames":["API.acme.dev.","b.acme.dev"]}`, "hostnames +b.acme.dev -"},
		// The top level decides when it names hostnames.
		{`{"hostnames":["api.acme.dev"],"config":{"hostnames":["other.acme.dev"]}}`, ""},
		{`{"config":{"hostname":"api.acme.dev","hostnames":["c.acme.dev"]}}`, "config.hostnames +c.acme.dev -"},
	} {
		var body map[string]any
		if err := json.Unmarshal([]byte(tc.body), &body); err != nil {
			t.Fatal(err)
		}
		got := ""
		for _, c := range diffSettings(record, body, nil, nil).Changes {
			got += fmt.Sprintf("%s +%s -%s", c.Field, strings.Join(c.Added, ","), strings.Join(c.Removed, ","))
		}
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.body, got, tc.want)
		}
	}
}
