package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	databaseSecret = `{"name":"database","namespace":"shop","keys":["PASSWORD","HOST"],"type":"Opaque","createdAt":"2026-09-01T10:00:00.000Z","managedFromEdka":true}`
	githubSecret   = `{"name":"github","namespace":"default","keys":["TOKEN"],"type":"Opaque","createdAt":"2026-09-02T10:00:00.000Z","managedFromEdka":true}`
)

// secretAPI serves the cluster sinaia with a secret in shop and one in
// default. A secret without a route answers 404, as Edka does.
func secretAPI(t *testing.T, extra map[string][]string) (*stepAPI, string) {
	t.Helper()
	steps := map[string][]string{
		"GET /api/clusters":                        {`{"data":[{"id":"c1","name":"sinaia"}]}`},
		"GET /api/clusters/c1/secrets":             {`{"data":[` + databaseSecret + `,` + githubSecret + `]}`},
		"GET /api/clusters/c1/secrets/database":    {`{"data":` + databaseSecret + `}`},
		"GET /api/clusters/c1/secrets/github":      {`{"data":` + githubSecret + `}`},
		"PUT /api/clusters/c1/secrets/database":    {`{"message":"Secret updated successfully","data":` + databaseSecret + `}`},
		"POST /api/clusters/c1/secrets":            {`{"message":"Secret created successfully","data":{"name":"api","namespace":"shop","type":"Opaque"}}`},
		"DELETE /api/clusters/c1/secrets/database": {`{"message":"Secret deleted successfully"}`},
		"GET /api/clusters/c1/namespaces":          {`{"namespaces":[{"name":"default","status":"Active"},{"name":"shop","status":"Active"}]}`},
	}
	for route, responses := range extra {
		steps[route] = responses
	}
	server, api := newStepAPI(t, steps)
	return api, server.URL
}

func (s *stepAPI) query(route string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[route]
}

func TestSecretsListAndGetShowKeysAndNoValues(t *testing.T) {
	api, base := secretAPI(t, nil)
	out, _, err := execute(t, base, "secrets", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || squeeze(lines[0]) != "NAME NAMESPACE KEYS TYPE CREATED CLUSTER" || !strings.HasPrefix(squeeze(lines[1]), "database shop HOST, PASSWORD Opaque 2026-09-01") || !strings.HasSuffix(squeeze(lines[2]), "sinaia") {
		t.Fatal(out)
	}
	if _, _, err := execute(t, base, "secrets", "list", "--namespace", "shop"); err != nil || api.query("GET /api/clusters/c1/secrets") != "namespace=shop" {
		t.Fatal(err, api.query("GET /api/clusters/c1/secrets"))
	}
	out, _, err = execute(t, base, "secrets", "--cluster", "sinaia", "get", "database", "--namespace", "shop")
	if err != nil || !strings.Contains(squeeze(out), "Name database Namespace shop Type Opaque Keys HOST, PASSWORD Created 2026-09-01") || api.query("GET /api/clusters/c1/secrets/database") != "namespace=shop" {
		t.Fatal(out, err)
	}
}

func TestSecretSetCreatesOrChanges(t *testing.T) {
	api, base := secretAPI(t, nil)
	// The namespace has no secret of that name, so the command creates one.
	out, errOut, err := executeInput(t, base, "s3cret-value\n", "secrets", "set", "api", "TOKEN", "--namespace", "shop", "--cluster", "sinaia", "--json")
	if err != nil || !strings.Contains(errOut, "✓ Created secret api in namespace shop of cluster sinaia with TOKEN") {
		t.Fatal(errOut, err)
	}
	if got := api.body("POST /api/clusters/c1/secrets"); got != `{"literals":[{"key":"TOKEN","value":"s3cret-value"}],"name":"api","namespace":"shop"}` {
		t.Fatal(got)
	}
	// The value goes to Edka and nowhere else.
	if strings.Contains(out+errOut, "s3cret") {
		t.Fatal(out, errOut)
	}

	// The secret exists, so the command sets the key and keeps the others.
	_, errOut, err = executeInput(t, base, "rotated", "secrets", "--cluster", "sinaia", "set", "database", "PASSWORD", "--namespace", "shop")
	if err != nil || !strings.Contains(errOut, "✓ Set PASSWORD on secret database in namespace shop of cluster sinaia") {
		t.Fatal(errOut, err)
	}
	if got := api.body("PUT /api/clusters/c1/secrets/database"); got != `{"literals":[{"key":"PASSWORD","value":"rotated"}],"removeKeys":[]}` || api.query("PUT /api/clusters/c1/secrets/database") != "namespace=shop" {
		t.Fatal(got, api.query("PUT /api/clusters/c1/secrets/database"))
	}
	if api.count("POST /api/clusters/c1/secrets") != 1 {
		t.Fatal(api.requests)
	}

	// A file gives the value of one key, and stdin the value of another.
	certificate := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(certificate, []byte("-----BEGIN CERTIFICATE-----\nabc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeInput(t, base, "hunter2", "secrets", "--cluster", "sinaia", "set", "database", "PASSWORD", "--from-file", "ca.crt="+certificate, "--namespace", "shop"); err != nil {
		t.Fatal(err)
	}
	if got := api.body("PUT /api/clusters/c1/secrets/database"); got != `{"literals":[{"key":"PASSWORD","value":"hunter2"},{"key":"ca.crt","value":"-----BEGIN CERTIFICATE-----\nabc\n"}],"removeKeys":[]}` {
		t.Fatal(got)
	}

	// A failure to read the secret is not taken for a missing one.
	api, base = secretAPI(t, map[string][]string{"GET /api/clusters/c1/secrets/database": {`500 {"error":"Failed to get secret metadata","message":"connect ETIMEDOUT"}`}})
	if _, _, err := executeInput(t, base, "x", "secrets", "--cluster", "sinaia", "set", "database", "PASSWORD", "--namespace", "shop"); err == nil || !strings.Contains(err.Error(), "HTTP 500") || api.writes() != 0 {
		t.Fatal(err, api.requests)
	}
}

func TestSecretSetRefusesBeforeSending(t *testing.T) {
	api, base := secretAPI(t, nil)
	for _, test := range []struct {
		line, input, want string
	}{
		{"set database PASSWORD=hunter2", "", "values go at the prompt or on stdin, not in arguments that shell history keeps; for example: printf %s \"$VALUE\" | edka secrets set database PASSWORD"},
		{"set database", "x", "name the keys to set, or give them with --from-file KEY=path"},
		{"set database PASS/WORD", "x", `invalid key "PASS/WORD"`},
		{"set database PASSWORD PASSWORD", "x", "PASSWORD is given more than once"},
		{"set database PASSWORD HOST", "x", "stdin holds one value; set one key at a time, or give the others with --from-file"},
		{"set database PASSWORD", "", "no value for PASSWORD on stdin"},
		{"set Database PASSWORD", "x", `secret name "Database" must be lowercase`},
		{"set database --from-file ca.crt", "x", `--from-file takes KEY=path: "ca.crt"`},
		{"set database --from-file ca.crt=/nonexistent/ca.crt", "x", "/nonexistent/ca.crt"},
		{"set database PASSWORD --namespace a/b", "x", `invalid namespace "a/b"`},
	} {
		_, _, err := executeInput(t, base, test.input, append([]string{"secrets", "--cluster", "sinaia"}, strings.Split(test.line, " ")...)...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("edka secrets %s: %v, want %q", test.line, err, test.want)
		}
	}
	if api.writes() != 0 {
		t.Fatal(api.requests)
	}
}

func TestSecretUnsetAndDelete(t *testing.T) {
	api, base := secretAPI(t, nil)
	_, errOut, err := execute(t, base, "secrets", "--cluster", "sinaia", "unset", "database", "HOST", "HOST", "--namespace", "shop")
	if err != nil || api.body("PUT /api/clusters/c1/secrets/database") != `{"literals":[],"removeKeys":["HOST"]}` || !strings.Contains(errOut, "✓ Removed HOST from secret database in namespace shop of cluster sinaia") {
		t.Fatal(errOut, err, api.body("PUT /api/clusters/c1/secrets/database"))
	}
	if _, _, err := execute(t, base, "secrets", "--cluster", "sinaia", "unset", "database", "TOKEN", "--namespace", "shop"); err == nil || !strings.Contains(err.Error(), "TOKEN is not set on secret database in namespace shop of cluster sinaia; it has HOST, PASSWORD") || api.count("PUT /api/clusters/c1/secrets/database") != 1 {
		t.Fatal(err, api.requests)
	}

	_, _, err = execute(t, base, "secrets", "--cluster", "sinaia", "delete", "database", "--namespace", "shop")
	if err == nil || !strings.Contains(err.Error(), "Delete secret database from namespace shop of cluster sinaia requires confirmation; rerun with --yes") || api.count("DELETE /api/clusters/c1/secrets/database") != 0 {
		t.Fatal(err, api.requests)
	}
	_, errOut, err = execute(t, base, "secrets", "--cluster", "sinaia", "delete", "database", "--namespace", "shop", "--yes")
	if err != nil || api.query("DELETE /api/clusters/c1/secrets/database") != "namespace=shop" || !strings.Contains(errOut, "✓ Deleted secret database from namespace shop of cluster sinaia") {
		t.Fatal(errOut, err)
	}
	// Edka's refusal of a secret it did not create reaches the user as it is.
	_, base = secretAPI(t, map[string][]string{"DELETE /api/clusters/c1/secrets/database": {`403 {"error":"Failed to delete secret","message":"Cannot delete a secret not created by the user"}`}})
	if _, _, err := execute(t, base, "secrets", "--cluster", "sinaia", "delete", "database", "--namespace", "shop", "--yes"); err == nil || !strings.Contains(err.Error(), "Cannot delete a secret not created by the user") {
		t.Fatal(err)
	}
}

// A secret looked for in the wrong namespace names the namespace that has it.
func TestSecretNotFoundNamesItsNamespace(t *testing.T) {
	_, base := secretAPI(t, map[string][]string{"GET /api/clusters/c1/secrets/database": {`404 {"error":"Failed to get secret metadata","message":"Secret not found"}`}})
	for _, line := range []string{"get database", "unset database HOST", "delete database --yes"} {
		_, _, err := execute(t, base, append([]string{"secrets", "--cluster", "sinaia"}, strings.Split(line, " ")...)...)
		if err == nil || !strings.Contains(err.Error(), "secret database is not in namespace default of cluster sinaia, but in namespace shop; pass --namespace shop") {
			t.Errorf("edka secrets %s: %v", line, err)
		}
	}
	if _, _, err := execute(t, base, "secrets", "--cluster", "sinaia", "get", "nope"); err == nil || !strings.Contains(err.Error(), "secret nope was not found in namespace default of cluster sinaia; list them with `edka secrets list`") {
		t.Fatal(err)
	}
}

func TestSecretCompletion(t *testing.T) {
	api, base := secretAPI(t, nil)
	for _, test := range []struct {
		line string
		want []string
	}{
		{"secrets get --cluster sinaia ", []string{"database\tHOST, PASSWORD", "github\tTOKEN"}},
		{"secrets unset --cluster sinaia --namespace shop database ", []string{"HOST", "PASSWORD"}},
		{"secrets unset --cluster sinaia --namespace shop database HOST ", []string{"PASSWORD"}},
		{"secrets set --cluster sinaia database ", nil},
		{"secrets list --cluster sinaia --namespace ", []string{"default", "shop"}},
	} {
		got, directive := completions(t, base, strings.Split(test.line, " ")...)
		if strings.Join(got, "\n") != strings.Join(test.want, "\n") || directive != shellNoFiles {
			t.Errorf("edka %s<TAB> offered %q %s, want %q", test.line, got, directive, test.want)
		}
	}
	// The names come from the namespace on the command line.
	if _, _ = completions(t, base, "secrets", "delete", "--cluster", "sinaia", "--namespace", "shop", ""); api.query("GET /api/clusters/c1/secrets") != "namespace=shop" {
		t.Fatal(api.query("GET /api/clusters/c1/secrets"))
	}
}
