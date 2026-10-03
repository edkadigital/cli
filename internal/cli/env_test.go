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

const envConfig = `{"env_variables":[{"name":"PORT","value":"8080"},{"name":"LOG_LEVEL","value":"info"}],"secrets":[{"name":"API_TOKEN","value":""}]}`

// envAPI serves deployment api with config and records each settings body,
// as JSON with sorted keys.
func envAPI(t *testing.T, config string) (*httptest.Server, *[]string) {
	t.Helper()
	bodies := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/deployments":
			fmt.Fprint(w, `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`)
		case "GET /api/deployments/d1":
			fmt.Fprintf(w, `{"data":{"id":"d1","name":"api","config":%s}}`, config)
		case "PATCH /api/deployments/d1/settings":
			var body any
			data, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(data, &body); err != nil {
				t.Errorf("settings body is not JSON: %s", data)
			}
			sorted, _ := json.Marshal(body)
			bodies = append(bodies, string(sorted))
			fmt.Fprint(w, `{"message":"Deployment update initiated","taskId":"j1","generation":4,"config":{}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

func TestEnvListsVariablesAndSecretNames(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	out, _, err := execute(t, server.URL, "env", "--deployment", "api")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, out, "NAME", "VALUE", "PORT", "8080", "LOG_LEVEL", "info", "API_TOKEN", "(secret)")
	out, _, err = execute(t, server.URL, "deployments", "env", "list", "--deployment", "api", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(out), &list) != nil || len(list.Data) != 3 || list.Data[0]["value"] != "8080" || list.Data[2]["secret"] != true {
		t.Fatal(out)
	}
	if _, hasValue := list.Data[2]["value"]; hasValue {
		t.Fatalf("secret listed with a value: %s", out)
	}
	empty, _ := envAPI(t, `{}`)
	out, _, err = execute(t, empty.URL, "env", "--deployment", "api")
	if err != nil || out != "No variables or secrets on api.\n" {
		t.Fatal(out, err)
	}
	if len(*bodies) != 0 {
		t.Fatal(*bodies)
	}
}

func TestEnvSetKeepsTheOtherVariables(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	out, errOut, err := execute(t, server.URL, "env", "set", "LOG_LEVEL=debug", "NEW=a=b", "--deployment", "api")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"config":{"env_variables":[{"name":"PORT","value":"8080"},{"name":"LOG_LEVEL","value":"debug"},{"name":"NEW","value":"a=b"}]}}`
	if len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
	if out != "" || errOut != "✓ Set LOG_LEVEL, NEW on api. Generation 4 is rolling out.\n  Next: edka deployments status api\n" {
		t.Fatalf("%q %q", out, errOut)
	}
	out, _, err = execute(t, server.URL, "env", "set", "PORT=9090", "--deployment", "api", "--json")
	if err != nil || !strings.Contains(out, `"generation": 4`) {
		t.Fatal(out, err)
	}
}

func TestEnvSetWithoutChangesSendsNothing(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	_, errOut, err := execute(t, server.URL, "env", "set", "PORT=8080", "--deployment", "api")
	if err != nil || len(*bodies) != 0 || !strings.Contains(errOut, "Nothing to change: PORT already set on api.") {
		t.Fatal(*bodies, errOut, err)
	}
	// No response exists to print, so JSON mode gets a record of its own.
	out, _, err := execute(t, server.URL, "env", "set", "PORT=8080", "--deployment", "api", "--json")
	var record struct {
		Changed    *bool
		Deployment string
		Keys       []string
	}
	if err != nil || len(*bodies) != 0 || json.Unmarshal([]byte(out), &record) != nil || record.Changed == nil || *record.Changed || record.Deployment != "d1" || strings.Join(record.Keys, ",") != "PORT" {
		t.Fatal(*bodies, out, err)
	}
}

func TestEnvSetRejectsBadArgumentsBeforeReading(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"PORT"}, `set a variable as KEY=value, or a secret with --secret: "PORT"`},
		{[]string{"9LIVES=1"}, `invalid name "9LIVES"`},
		{[]string{"MY-VAR=1"}, `invalid name "MY-VAR"`},
		{[]string{"A=1", "A=2"}, "A is given more than once"},
		{[]string{"--secret", "API_TOKEN=hunter2"}, "not in arguments that shell history keeps"},
	} {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
		_, _, err := execute(t, server.URL, append([]string{"env", "set", "--deployment", "api"}, tc.args...)...)
		server.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "hunter2") || requests != 0 {
			t.Errorf("%v: %v after %d requests", tc.args, err, requests)
		}
	}
}

func TestEnvSetDoesNotTurnASecretIntoAVariable(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	_, _, err := execute(t, server.URL, "env", "set", "API_TOKEN=visible", "--deployment", "api")
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN is a secret") || len(*bodies) != 0 {
		t.Fatal(*bodies, err)
	}
}

func TestEnvSetSecretReadsStdin(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	out, errOut, err := executeInput(t, server.URL, "s3cret value\n", "env", "set", "--secret", "DB_URL", "--deployment", "api")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"config":{"secrets":[{"name":"API_TOKEN","value":""},{"name":"DB_URL","value":""}]},"secret_values":{"DB_URL":"s3cret value"}}`
	if len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
	if strings.Contains(out+errOut, "s3cret") || !strings.Contains(errOut, "✓ Set DB_URL on api as a secret.") {
		t.Fatalf("%q %q", out, errOut)
	}
	// A variable of the same name becomes the secret; Windows line endings are trimmed too.
	*bodies = nil
	_, errOut, err = executeInput(t, server.URL, "9090\r\n", "env", "set", "--secret", "PORT", "--deployment", "api")
	if err != nil {
		t.Fatal(err)
	}
	want = `{"config":{"env_variables":[{"name":"LOG_LEVEL","value":"info"}],"secrets":[{"name":"API_TOKEN","value":""},{"name":"PORT","value":""}]},"secret_values":{"PORT":"9090"}}`
	if len(*bodies) != 1 || (*bodies)[0] != want || !strings.Contains(errOut, "Moving PORT from variables to secrets.") {
		t.Fatal(*bodies, errOut)
	}
}

func TestEnvSetSecretNeedsOneValueOnStdin(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	for _, tc := range []struct {
		input string
		args  []string
		want  string
	}{
		{"", []string{"DB_URL"}, "no value for DB_URL on stdin"},
		{"\n", []string{"DB_URL"}, "no value for DB_URL on stdin"},
		{"a\n", []string{"DB_URL", "OTHER"}, "set one secret at a time"},
		{strings.Repeat("x", maxSecretValue+1), []string{"DB_URL"}, "exceeds 1 MiB"},
	} {
		_, _, err := executeInput(t, server.URL, tc.input, append([]string{"env", "set", "--secret", "--deployment", "api"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	if len(*bodies) != 0 {
		t.Fatal(*bodies)
	}
}

func TestEnvUnsetRemovesVariablesAndSecrets(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	_, errOut, err := execute(t, server.URL, "env", "unset", "LOG_LEVEL", "API_TOKEN", "LOG_LEVEL", "--deployment", "api")
	if err != nil {
		t.Fatal(err)
	}
	// Removing the last secret sends an empty list, which Edka accepts; null it rejects.
	want := `{"config":{"env_variables":[{"name":"PORT","value":"8080"}],"secrets":[]}}`
	if len(*bodies) != 1 || (*bodies)[0] != want || !strings.Contains(errOut, "✓ Removed LOG_LEVEL, API_TOKEN from api.") {
		t.Fatal(*bodies, errOut)
	}
	*bodies = nil
	if _, _, err := execute(t, server.URL, "env", "unset", "PORT", "--deployment", "api"); err != nil {
		t.Fatal(err)
	}
	if want := `{"config":{"env_variables":[{"name":"LOG_LEVEL","value":"info"}]}}`; len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
	*bodies = nil
	if _, _, err := execute(t, server.URL, "env", "unset", "PORT", "LOG_LEVEL", "--deployment", "api"); err != nil {
		t.Fatal(err)
	}
	if want := `{"config":{"env_variables":[]}}`; len(*bodies) != 1 || (*bodies)[0] != want {
		t.Fatal(*bodies)
	}
	*bodies = nil
	_, _, err = execute(t, server.URL, "env", "unset", "PORT", "MISSING", "GONE", "--deployment", "api")
	if err == nil || err.Error() != "api has no variable or secret named MISSING, GONE" || len(*bodies) != 0 {
		t.Fatal(*bodies, err)
	}
}

func TestEnvRefusesToWriteWithoutTheStoredConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/deployments":
			fmt.Fprint(w, `{"data":[{"id":"d1","name":"api"}]}`)
		case "GET /api/deployments/d1":
			fmt.Fprint(w, `{"data":{"id":"d1","name":"api"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	_, _, err := execute(t, server.URL, "env", "set", "PORT=1", "--deployment", "api")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatal(err)
	}
}

func TestEnvWaitFollowsTheRollout(t *testing.T) {
	fastPolls(t)
	server, requests := sequenceAPI(t, map[string][]string{
		"GET /api/deployments": {`{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`},
		"GET /api/deployments/d1": {
			`{"data":{"id":"d1","config":` + envConfig + `,"spec_generation":3,"applied_generation":3,"healthy_generation":3}}`,
			`{"data":{"spec_generation":4,"applied_generation":4,"healthy_generation":4,"status":"deployed"}}`,
		},
		"PATCH /api/deployments/d1/settings": {`{"message":"Deployment update initiated","generation":4}`},
		"GET /api/deployments/d1/status":     {`{"data":{"name":"api","status":"deployed","replicas":{"desired":1,"updated":1,"ready":1,"available":1}}}`},
	})
	out, errOut, err := execute(t, server.URL, "env", "set", "PORT=9090", "--deployment", "api", "--wait")
	if err != nil {
		t.Fatal(err, errOut)
	}
	inOrder(t, errOut, "✓ Set PORT on api. Waiting for generation 4…\n", "✓ api is running generation 4\n")
	inOrder(t, out, "Status", "deployed")
	if got := strings.Join(*requests, ","); !strings.HasPrefix(got, "GET /api/deployments,GET /api/deployments/d1,PATCH /api/deployments/d1/settings,") {
		t.Fatal(got)
	}
}

func TestEnvUnknownCommandSuggestsTheRightOne(t *testing.T) {
	_, _, err := execute(t, "http://127.0.0.1:1", "env", "sett", "PORT=1")
	if err == nil || !strings.Contains(err.Error(), `unknown command "sett" for "edka env"`) || !strings.Contains(err.Error(), "Did you mean this?\n\tset") {
		t.Fatal(err)
	}
}

// envStore is deployment api behind a settings route that, as Edka does,
// refuses a request built from another generation than the stored one.
type envStore struct {
	generation int
	variables  []map[string]string
	secrets    []string
	// afterRead runs once a read has been answered, as another writer that
	// saves between this client's read and its save.
	afterRead func(*envStore)
	// refuse answers every save with this 409 body.
	refuse string
	reads  int
	saves  []map[string]any
}

func (s *envStore) names() string {
	names := []string{}
	for _, v := range s.variables {
		names = append(names, v["name"])
	}
	return strings.Join(names, ",") + "|" + strings.Join(s.secrets, ",")
}

func (s *envStore) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/deployments":
			fmt.Fprint(w, `{"data":[{"id":"d1","name":"api","cluster_name":"sinaia"}]}`)
		case "GET /api/deployments/d1":
			s.reads++
			secrets := []map[string]string{}
			for _, name := range s.secrets {
				secrets = append(secrets, map[string]string{"name": name, "value": ""})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": "d1", "name": "api", "spec_generation": s.generation, "config": map[string]any{"env_variables": s.variables, "secrets": secrets}}})
			if s.afterRead != nil {
				s.afterRead(s)
			}
		case "PATCH /api/deployments/d1/settings":
			var body struct {
				ExpectedGeneration *int `json:"expected_generation"`
				Config             struct {
					Variables *[]map[string]string `json:"env_variables"`
					Secrets   *[]map[string]string `json:"secrets"`
				}
			}
			var raw map[string]any
			data, _ := io.ReadAll(r.Body)
			if json.Unmarshal(data, &body) != nil || json.Unmarshal(data, &raw) != nil {
				t.Errorf("settings body is not JSON: %s", data)
			}
			s.saves = append(s.saves, raw)
			if s.refuse != "" {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, s.refuse)
				return
			}
			if body.ExpectedGeneration != nil && *body.ExpectedGeneration != s.generation {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `{"error":"Change refused","message":"The deployment changed after this change was prepared. Read it again and save."}`)
				return
			}
			if body.Config.Variables != nil {
				s.variables = *body.Config.Variables
			}
			if body.Config.Secrets != nil {
				s.secrets = nil
				for _, secret := range *body.Config.Secrets {
					s.secrets = append(s.secrets, secret["name"])
				}
			}
			s.generation++
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"message":"Deployment update initiated","generation":%d}`, s.generation)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newEnvStore holds PORT and the secret API_TOKEN at generation 4.
func newEnvStore() *envStore {
	return &envStore{generation: 4, variables: []map[string]string{{"name": "PORT", "value": "8080"}}, secrets: []string{"API_TOKEN"}}
}

// savedOnce is another writer that adds a variable and a secret after the
// first read.
func savedOnce(s *envStore) {
	s.afterRead = nil
	s.variables = append(s.variables, map[string]string{"name": "CONCURRENT_KEY", "value": "1"})
	s.secrets = append(s.secrets, "CONCURRENT_SECRET")
	s.generation++
}

// A save that lands between this command's read and its own save keeps its
// keys: Edka refuses the stale request, and the command reads again.
func TestEnvChangeKeepsAConcurrentSave(t *testing.T) {
	for _, tc := range []struct {
		input string
		args  []string
		want  string
	}{
		{"", []string{"set", "LOG_LEVEL=debug"}, "PORT,CONCURRENT_KEY,LOG_LEVEL|API_TOKEN,CONCURRENT_SECRET"},
		{"", []string{"unset", "PORT"}, "CONCURRENT_KEY|API_TOKEN,CONCURRENT_SECRET"},
		{"", []string{"unset", "API_TOKEN"}, "PORT,CONCURRENT_KEY|CONCURRENT_SECRET"},
		{"s3cret", []string{"set", "--secret", "DB_URL"}, "PORT,CONCURRENT_KEY|API_TOKEN,CONCURRENT_SECRET,DB_URL"},
		{"9090", []string{"set", "--secret", "PORT"}, "CONCURRENT_KEY|API_TOKEN,CONCURRENT_SECRET,PORT"},
	} {
		store := newEnvStore()
		store.afterRead = savedOnce
		out, errOut, err := executeInput(t, store.serve(t).URL, tc.input, append([]string{"env", "--deployment", "api"}, tc.args...)...)
		if err != nil || store.names() != tc.want {
			t.Errorf("%v: %s: %v", tc.args, store.names(), err)
			continue
		}
		// The first request names the generation it read; the second the one it read again.
		if len(store.saves) != 2 || store.saves[0]["expected_generation"] != float64(4) || store.saves[1]["expected_generation"] != float64(5) || store.generation != 6 {
			t.Errorf("%v: %v", tc.args, store.saves)
		}
		if !strings.Contains(errOut, "api changed while this change was being saved; reading it again.") || strings.Contains(out+errOut, "s3cret") {
			t.Errorf("%v: %q", tc.args, errOut)
		}
		// The value is read once and sent again with the second request.
		if tc.input != "" && fmt.Sprint(store.saves[1]["secret_values"]) != fmt.Sprint(store.saves[0]["secret_values"]) {
			t.Errorf("%v: %v", tc.args, store.saves[1])
		}
	}
}

func TestEnvChangeStopsWhenTheDeploymentKeepsChanging(t *testing.T) {
	store := newEnvStore()
	store.afterRead = func(s *envStore) { s.generation++ }
	_, _, err := execute(t, store.serve(t).URL, "env", "set", "LOG_LEVEL=debug", "--deployment", "api")
	if err == nil || !strings.Contains(err.Error(), "api changed each of the 3 times this change was sent, so nothing was saved; run the command again") || !strings.Contains(err.Error(), "(HTTP 409)") {
		t.Fatal(err)
	}
	if len(store.saves) != 3 || store.names() != "PORT|API_TOKEN" {
		t.Fatal(store.saves, store.names())
	}
}

// Only the refusal of a stale change is sent again.
func TestEnvChangeDoesNotRepeatAnotherConflict(t *testing.T) {
	store := newEnvStore()
	store.refuse = `{"error":"MetalLB is not ready"}`
	_, _, err := execute(t, store.serve(t).URL, "env", "set", "LOG_LEVEL=debug", "--deployment", "api")
	if err == nil || !strings.Contains(err.Error(), "MetalLB is not ready (HTTP 409)") || len(store.saves) != 1 || store.reads != 1 {
		t.Fatal(err, store.saves, store.reads)
	}
}

// A secret's value is read before the lists, so the lists are as fresh as the
// request that sends them.
func TestEnvSetSecretReadsTheValueBeforeTheLists(t *testing.T) {
	store := newEnvStore()
	_, _, err := executeInput(t, store.serve(t).URL, "", "env", "set", "--secret", "DB_URL", "--deployment", "api")
	if err == nil || !strings.Contains(err.Error(), "no value for DB_URL on stdin") || store.reads != 0 || len(store.saves) != 0 {
		t.Fatal(err, store.reads, store.saves)
	}
}

func TestEnvDiffShowsTheChangeAndSendsNothing(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	for _, tc := range []struct {
		args []string
		want []string
		not  []string
	}{
		{[]string{"env", "set", "PORT=9090", "DEBUG=1"}, []string{"api: 1 change. Nothing was sent.\n", "config.env_variables", "+ DEBUG=1\n", "~ PORT: 8080 → 9090\n"}, []string{"LOG_LEVEL", "- "}},
		{[]string{"env", "set", "PORT=8080"}, []string{"api: no change. Nothing was sent.\n"}, []string{"config."}},
		{[]string{"env", "unset", "LOG_LEVEL", "API_TOKEN"}, []string{"api: 2 changes. Nothing was sent.\n", "config.env_variables", "- LOG_LEVEL\n", "config.secrets", "- API_TOKEN\n"}, []string{"PORT"}},
		// A variable that becomes a secret leaves the variables.
		{[]string{"env", "set", "--secret", "PORT", "DATABASE_URL"}, []string{"api: 3 changes. Nothing was sent.\n", "config.env_variables", "- PORT\n", "config.secrets", "+ PORT\n", "+ DATABASE_URL\n", "secret_values", "~ DATABASE_URL\n", "~ PORT (values not shown)\n"}, []string{"8080"}},
	} {
		out, errOut, err := execute(t, server.URL, append(tc.args, "--deployment", "api", "--diff")...)
		if err != nil {
			t.Fatal(tc.args, err, errOut)
		}
		inOrder(t, out, tc.want...)
		for _, not := range tc.not {
			if strings.Contains(out, not) {
				t.Errorf("%v: %q in\n%s", tc.args, not, out)
			}
		}
		// The lists are planned again when the change is sent, so no generation is named.
		if errOut != "" {
			t.Errorf("%v: stderr %q", tc.args, errOut)
		}
	}
	if len(*bodies) != 0 {
		t.Fatalf("--diff sent %v", *bodies)
	}
}

// A diff shows no secret value, so it reads none: not from stdin, and not at a prompt.
func TestEnvDiffOfASecretReadsNoValue(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	out, errOut, err := executeInput(t, server.URL, "s3cr3t-value\n", "env", "set", "--secret", "DATABASE_URL", "--deployment", "api", "--diff", "--json")
	var d settingsDiff
	if err != nil || json.Unmarshal([]byte(out), &d) != nil || len(d.Changes) != 2 || len(d.Refused) != 0 || strings.Contains(out+errOut, "s3cr3t") || len(*bodies) != 0 {
		t.Fatal(out, errOut, err, *bodies)
	}
	// Two secrets need two values when they are set, and none to be compared.
	if _, _, err := execute(t, server.URL, "env", "set", "--secret", "A_KEY", "B_KEY", "--deployment", "api", "--diff"); err != nil {
		t.Fatal(err)
	}
}

func TestEnvDiffKeepsTheChecksOfTheChange(t *testing.T) {
	server, bodies := envAPI(t, envConfig)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"env", "unset", "MISSING"}, "api has no variable or secret named MISSING"},
		{[]string{"env", "set", "API_TOKEN=plain"}, "API_TOKEN is a secret"},
		{[]string{"env", "set", "--secret", "API_TOKEN=plain"}, "secret values go at the prompt or on stdin"},
	} {
		if _, _, err := execute(t, server.URL, append(tc.args, "--deployment", "api", "--diff")...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	if len(*bodies) != 0 {
		t.Fatalf("--diff sent %v", *bodies)
	}
}
