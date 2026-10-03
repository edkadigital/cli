package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePackage writes a small package directory and returns its path.
func writePackage(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "memos")
	files := map[string]string{
		"template.yaml":                   "format_version: 2\nname: \"Memos\"\nslug: \"memos\"\nversion: \"1.2.0\"\n",
		"README.md":                       "# Memos\n",
		"chart/Chart.yaml":                "apiVersion: v2\nname: memos\nversion: 1.2.0\n",
		"chart/templates/deployment.yaml": "kind: Deployment\n",
		".DS_Store":                       "ignored",
		".git/config":                     "ignored",
	}
	for path, content := range extra {
		files[path] = content
	}
	for path, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// endlessFile serves bytes for as long as it is read, and counts them.
type endlessFile struct{ served int }

func (f *endlessFile) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = 'x'
	}
	f.served += len(p)
	return len(p), nil
}

func TestReadAtMostStopsOneByteAfterTheLimit(t *testing.T) {
	file := &endlessFile{}
	if _, err := readAtMost(file, 100); !errors.Is(err, errOverLimit) {
		t.Fatalf("err %v", err)
	}
	if file.served != 101 {
		t.Fatalf("read %d bytes of a file that may hold 100", file.served)
	}

	if data, err := readAtMost(strings.NewReader("abc"), 3); err != nil || string(data) != "abc" {
		t.Fatalf("data %q, err %v", data, err)
	}
	if _, err := readAtMost(strings.NewReader("abcd"), 3); !errors.Is(err, errOverLimit) {
		t.Fatalf("err %v", err)
	}
	// Nothing is left of the limit, and an empty file still fits.
	if data, err := readAtMost(strings.NewReader(""), 0); err != nil || len(data) != 0 {
		t.Fatalf("data %q, err %v", data, err)
	}
}

func TestAppsValidateRefusesADirectoryOverOneMegabyteBeforeItSendsIt(t *testing.T) {
	dir := writePackage(t, map[string]string{"data/dump.sql": strings.Repeat("x", 3<<20)})
	server, api := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {validPackage}})

	_, _, err := execute(t, server.URL, "apps", "validate", dir)
	if err == nil || !strings.Contains(err.Error(), "holds more than 1 MB; a package holds at most 1 MB") {
		t.Fatalf("err %v", err)
	}
	if len(api.requests) != 0 {
		t.Fatalf("sent %v", api.requests)
	}
}

const validPackage = `{"data":{"valid":true,"profile":"organization","diagnostics":[],"capabilities":null}}`
const invalidPackage = `{"data":{"valid":false,"profile":"organization","diagnostics":[{"code":"chart.version","severity":"error","file":"chart/Chart.yaml","line":3,"message":"The chart is version \"1.1.0\". It must carry the package version, \"1.2.0\".","fix":"Set version: 1.2.0 in Chart.yaml."}],"capabilities":null}}`

func sentFiles(t *testing.T, body string) map[string]any {
	t.Helper()
	var request struct {
		Files   map[string]any `json:"files"`
		Profile string         `json:"profile"`
		Via     string         `json:"via"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	return request.Files
}

func TestAppsValidateSendsThePackageAndReportsItValid(t *testing.T) {
	dir := writePackage(t, map[string]string{"icon.png": "\x89PNG\r\n\x1a\n\xff\xfe"})
	server, api := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {validPackage}})

	out, errOut, err := execute(t, server.URL, "apps", "validate", dir)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" || !strings.Contains(errOut, "✓ memos 1.2.0 is valid (5 files)") {
		t.Fatalf("out %q, err %q", out, errOut)
	}
	files := sentFiles(t, api.bodies["POST /api/custom-apps/validate"])
	if len(files) != 5 || files["template.yaml"] == nil || files["chart/templates/deployment.yaml"] != "kind: Deployment\n" {
		t.Fatalf("sent %v", files)
	}
	for _, ignored := range []string{".DS_Store", ".git/config"} {
		if _, sent := files[ignored]; sent {
			t.Fatalf("sent %s", ignored)
		}
	}
	// A file that is not text travels as base64.
	if icon, ok := files["icon.png"].(map[string]any); !ok || icon["base64"] == "" {
		t.Fatalf("icon sent as %v", files["icon.png"])
	}
	if !strings.Contains(api.bodies["POST /api/custom-apps/validate"], `"profile":"organization"`) {
		t.Fatalf("body %s", api.bodies["POST /api/custom-apps/validate"])
	}
}

func TestAppsValidatePrintsEachFindingAndFails(t *testing.T) {
	dir := writePackage(t, nil)
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {invalidPackage}})

	out, _, err := execute(t, server.URL, "apps", "validate", dir, "--community")
	if err == nil || !strings.Contains(err.Error(), "memos 1.2.0 has 1 error to fix") {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{"✗ chart/Chart.yaml:3  chart.version", "It must carry the package version", "Fix: Set version: 1.2.0 in Chart.yaml."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestAppsValidateJSONPrintsTheAPIAnswer(t *testing.T) {
	dir := writePackage(t, nil)
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {invalidPackage}})

	out, _, err := execute(t, server.URL, "apps", "validate", dir, "--json")
	if err == nil {
		t.Fatal("an invalid package must fail")
	}
	var answer struct {
		Data struct {
			Valid       bool             `json:"valid"`
			Diagnostics []map[string]any `json:"diagnostics"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &answer) != nil || answer.Data.Valid || len(answer.Data.Diagnostics) != 1 {
		t.Fatalf("out %q", out)
	}
}

func TestAppsValidateRefusesADirectoryThatIsNotAPackage(t *testing.T) {
	server, api := newStepAPI(t, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := execute(t, server.URL, "apps", "validate", dir)
	if err == nil || !strings.Contains(err.Error(), "holds no template.yaml") {
		t.Fatalf("err %v", err)
	}
	if len(api.requests) != 0 {
		t.Fatalf("sent %v", api.requests)
	}
}

func TestAppsPublishStoresOnlyAValidPackage(t *testing.T) {
	dir := writePackage(t, nil)
	server, api := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {invalidPackage}})

	out, _, err := execute(t, server.URL, "apps", "publish", dir)
	if err == nil || !strings.Contains(err.Error(), "nothing was published") {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(out, "chart.version") || api.count("POST /api/custom-apps") != 0 {
		t.Fatalf("out %q, requests %v", out, api.requests)
	}
}

func TestAppsPublishReportsTheVersionItStored(t *testing.T) {
	dir := writePackage(t, nil)
	server, api := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/validate": {validPackage},
		"POST /api/custom-apps":          {`{"success":true,"data":{"app":{"id":"p1","name":"Memos","slug":"memos"},"version":{"id":"v1","version":"1.2.0"},"created":true,"unchanged":false}}`},
	})

	_, errOut, err := execute(t, server.URL, "apps", "publish", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "✓ Published memos 1.2.0") {
		t.Fatalf("err %q", errOut)
	}
	body := api.bodies["POST /api/custom-apps"]
	if !strings.Contains(body, `"via":"cli"`) || len(sentFiles(t, body)) != 4 {
		t.Fatalf("body %s", body)
	}
}

func TestAppsPublishSaysWhenTheFilesAreAlreadyPublished(t *testing.T) {
	dir := writePackage(t, nil)
	server, _ := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/validate": {validPackage},
		"POST /api/custom-apps":          {`{"success":true,"data":{"app":{"id":"p1","name":"Memos","slug":"memos"},"version":{"id":"v1","version":"1.2.0"},"created":false,"unchanged":true}}`},
	})

	_, errOut, err := execute(t, server.URL, "apps", "publish", dir)
	if err != nil || !strings.Contains(errOut, "memos 1.2.0 is already published with these files") {
		t.Fatalf("err %v, stderr %q", err, errOut)
	}
}

func TestAppsInitWritesTheFilesEdkaReturns(t *testing.T) {
	server, api := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/scaffold": {`{"success":true,"data":{"files":{"template.yaml":"format_version: 2\n","chart/Chart.yaml":"apiVersion: v2\n","chart/templates/deployment.yaml":"kind: Deployment\n"}}}`},
	})
	dir := filepath.Join(t.TempDir(), "memos")

	_, errOut, err := execute(t, server.URL, "apps", "init", "memos", "--image", "ghcr.io/usememos/memos", "--tag", "0.25.1", "--port", "5230", "--with", "access,storage", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "✓ Wrote 3 files to "+dir) {
		t.Fatalf("stderr %q", errOut)
	}
	for path, want := range map[string]string{"template.yaml": "format_version: 2\n", "chart/templates/deployment.yaml": "kind: Deployment\n"} {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v", path, got, err)
		}
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(api.bodies["POST /api/custom-apps/scaffold"]), &body); err != nil {
		t.Fatal(err)
	}
	if body["slug"] != "memos" || body["name"] != "memos" || body["image"] != "ghcr.io/usememos/memos" || body["tag"] != "0.25.1" || body["port"] != float64(5230) {
		t.Fatalf("body %v", body)
	}
	if with, _ := body["with"].([]any); len(with) != 2 || with[0] != "access" || with[1] != "storage" {
		t.Fatalf("with %v", body["with"])
	}
}

func TestAppsInitWithoutPartsSendsNoPartsList(t *testing.T) {
	server, api := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/scaffold": {`{"success":true,"data":{"files":{"template.yaml":"format_version: 2\n"}}}`},
	})

	if _, _, err := execute(t, server.URL, "apps", "init", "memos", "--image", "ghcr.io/usememos/memos", "--dir", filepath.Join(t.TempDir(), "memos")); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(api.bodies["POST /api/custom-apps/scaffold"]), &body); err != nil {
		t.Fatal(err)
	}
	if _, sent := body["with"]; sent {
		t.Fatalf("with %v: the API takes a list of parts or nothing", body["with"])
	}
}

func TestAppsInitRefusesADirectoryWithFilesAndAPathThatLeavesIt(t *testing.T) {
	server, api := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/scaffold": {`{"success":true,"data":{"files":{"../outside.yaml":"x"}}}`},
	})
	full := writePackage(t, nil)
	if _, _, err := execute(t, server.URL, "apps", "init", "memos", "--image", "ghcr.io/usememos/memos", "--dir", full); err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("err %v", err)
	}
	if len(api.requests) != 0 {
		t.Fatalf("sent %v", api.requests)
	}

	parent := t.TempDir()
	dir := filepath.Join(parent, "memos")
	_, _, err := execute(t, server.URL, "apps", "init", "memos", "--image", "ghcr.io/usememos/memos", "--dir", dir)
	if err == nil || !strings.Contains(err.Error(), "cannot write") {
		t.Fatalf("err %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "outside.yaml")); statErr == nil {
		t.Fatal("wrote outside the directory")
	}
}

func packagedAppDetail(installed, latest, status string) string {
	resolved, current := "v1", "v2"
	if installed == latest {
		resolved = current
	}
	return `{"success":true,"data":{"id":"a1","cluster_id":"c1","app_name":"Memos","display_name":"memos","status":"` + status + `","version":"` + installed + `","catalog_app":{"name":"Memos","origin":"organization","version":"` + installed + `","latest_version":"` + latest + `","resolved_version_id":"` + resolved + `","current_version_id":"` + current + `"}}}`
}

func updateSteps(detail ...string) map[string][]string {
	return map[string][]string{
		"GET /api/clusters":                           {`{"data":[{"id":"c1","name":"sinaia"}]}`},
		"GET /api/clusters/c1/apps/instances":         {`{"data":[{"id":"a1","cluster_id":"c1","app_name":"Memos","display_name":"memos","instance_slug":"memos","status":"installed","version":"1.2.0"}]}`},
		"GET /api/clusters/c1/apps/a1":                detail,
		"GET /api/clusters/c1/apps/a1/versions/1.3.0": {`{"success":true,"data":{"installed":{"version":"1.2.0"},"target":{"version":"1.3.0"},"changes":[{"text":"Image ghcr.io/usememos/memos: 0.25.1 to 0.26.0","tone":"neutral"},{"text":"Setting Image tag: 0.25.1 to 0.26.0","tone":"neutral"},{"text":"Setting Memory limit stays 2Gi. The new default is 1Gi","tone":"warning"}]}}`},
		"PATCH /api/clusters/c1/apps/a1":              {`{"success":true,"message":"App configuration update initiated","app_id":"a1","job_id":"7"}`},
	}
}

func TestAppsUpdateMovesToTheLatestVersionAndWaits(t *testing.T) {
	server, api := newStepAPI(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed"), packagedAppDetail("1.3.0", "1.3.0", "configuring"), packagedAppDetail("1.3.0", "1.3.0", "installed")))

	_, errOut, err := execute(t, server.URL, "apps", "update", "memos", "--cluster", "sinaia", "--yes", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	if api.bodies["PATCH /api/clusters/c1/apps/a1"] != `{"version":"1.3.0"}` {
		t.Fatalf("body %s", api.bodies["PATCH /api/clusters/c1/apps/a1"])
	}
	if !strings.Contains(errOut, "✓ memos runs 1.3.0") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestAppsUpdateSendsNothingWhenTheAppRunsTheVersion(t *testing.T) {
	server, api := newStepAPI(t, updateSteps(packagedAppDetail("1.3.0", "1.3.0", "installed")))

	_, errOut, err := execute(t, server.URL, "apps", "update", "memos", "--cluster", "sinaia", "--yes")
	if err != nil || !strings.Contains(errOut, "already runs 1.3.0") || api.writes() != 0 {
		t.Fatalf("err %v, stderr %q, requests %v", err, errOut, api.requests)
	}
}

func TestAppsUpdateAsksBeforeItChangesAnApp(t *testing.T) {
	server, api := newStepAPI(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed")))

	_, _, err := execute(t, server.URL, "apps", "update", "memos", "--cluster", "sinaia", "--version", "1.3.0")
	if err == nil || !strings.Contains(err.Error(), "from 1.2.0 to 1.3.0") || !strings.Contains(err.Error(), "rerun with --yes") {
		t.Fatalf("err %v", err)
	}
	if api.writes() != 0 {
		t.Fatalf("requests %v", api.requests)
	}
}

func TestAppsUpdateListsWhatTheVersionChangesBeforeItAsks(t *testing.T) {
	server, api := newStepAPI(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed")))

	_, errOut, err := execute(t, server.URL, "apps", "update", "memos", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "rerun with --yes") {
		t.Fatalf("err %v", err)
	}
	want := "Version 1.3.0 changes:\n  - Image ghcr.io/usememos/memos: 0.25.1 to 0.26.0\n  - Setting Image tag: 0.25.1 to 0.26.0\n  ! Setting Memory limit stays 2Gi. The new default is 1Gi\n"
	if !strings.Contains(errOut, want) {
		t.Fatalf("stderr %q", errOut)
	}
	if api.writes() != 0 {
		t.Fatalf("requests %v", api.requests)
	}
}

func TestAppsUpdateSaysWhenAVersionChangesNothingItRunsOrCreates(t *testing.T) {
	steps := updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed"))
	steps["GET /api/clusters/c1/apps/a1/versions/1.3.0"] = []string{`{"success":true,"data":{"changes":[]}}`}
	server, _ := newStepAPI(t, steps)

	out, errOut, err := execute(t, server.URL, "apps", "update", "memos", "--cluster", "sinaia", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "Version 1.3.0 runs and creates the same things as 1.2.0.") {
		t.Fatalf("stderr %q", errOut)
	}
	if !strings.Contains(out, `"job_id"`) {
		t.Fatalf("stdout %q", out)
	}
}

func TestAppsUpdateStopsWhenTheVersionCannotBeApplied(t *testing.T) {
	steps := updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed"))
	steps["GET /api/clusters/c1/apps/a1/versions/1.3.0"] = []string{`409 {"error":"Version withdrawn","message":"Version 1.3.0 of 'Memos' was withdrawn"}`}
	server, api := newStepAPI(t, steps)

	_, _, err := execute(t, server.URL, "apps", "update", "memos", "--cluster", "sinaia", "--yes")
	if err == nil || !strings.Contains(err.Error(), "was withdrawn") {
		t.Fatalf("err %v", err)
	}
	if api.writes() != 0 {
		t.Fatalf("requests %v", api.requests)
	}
}

func TestAppsUpdateReportsAFailedUpdate(t *testing.T) {
	failed := strings.Replace(packagedAppDetail("1.3.0", "1.3.0", "failed"), `"status":"failed"`, `"status":"failed","progress":{"message":"Helm job failed"}`, 1)
	server, _ := newStepAPI(t, updateSteps(packagedAppDetail("1.2.0", "1.3.0", "installed"), failed))

	_, _, err := execute(t, server.URL, "apps", "update", "memos", "--cluster", "sinaia", "--yes", "--wait")
	if err == nil || !strings.Contains(err.Error(), "memos failed: Helm job failed") {
		t.Fatalf("err %v", err)
	}
}

func TestAppsPackagesListsWhatTheOrganizationPublished(t *testing.T) {
	server, _ := newStepAPI(t, map[string][]string{
		"GET /api/custom-apps": {`{"success":true,"data":[{"id":"p1","name":"Memos","slug":"memos","version":"1.3.0","app_version":"0.25.1","instances":2,"published_at":"2026-10-01T10:00:00Z"}]}`},
	})

	out, _, err := execute(t, server.URL, "apps", "custom")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "Memos", "memos", "1.3.0", "0.25.1", "2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}
