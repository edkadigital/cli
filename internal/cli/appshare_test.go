package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const sharedTemplate = `format_version: 2
name: "Memos"
slug: "memos"
description: "A lightweight memo hub."
version: "1.2.0"
app_version: "0.25.1"
upstream:
  repository: "https://github.com/usememos/memos"
  license: "MIT"
maintainers:
  - github: "Octocat"
`

const communityValid = `{"data":{"valid":true,"profile":"community","diagnostics":[],"capabilities":{"images":[{"repository":"ghcr.io/usememos/memos","tag":"0.25.1","digest":"sha256:abc"}],"objects":[{"kind":"Deployment","count":1},{"kind":"Service","count":2}],"endpoints":[{"name":"Memos"}],"storage":[{"field":"storage_size","default":"5Gi"}],"databases":["postgresql"]}}}`
const communityInvalid = `{"data":{"valid":false,"profile":"community","diagnostics":[{"code":"community.image-digest","severity":"error","file":"template.yaml","line":61,"message":"The image has no digest.","fix":"Append @sha256:<digest>."}],"capabilities":null}}`

type ghCall struct {
	args  string
	stdin string
}

// fakeGitHub replaces the GitHub CLI. Each call is answered by the first rule
// whose text its arguments contain. A rule that starts with "once:" answers
// one call and is then passed over. An answer that starts with "!" is an
// error carrying the rest.
func fakeGitHub(t *testing.T, rules [][2]string) *[]ghCall {
	t.Helper()
	calls := &[]ghCall{}
	used := map[int]bool{}
	previous := runTool
	runTool = func(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		*calls = append(*calls, ghCall{args: joined, stdin: string(stdin)})
		for index, rule := range rules {
			if used[index] || !strings.Contains(joined, rule[0]) {
				continue
			}
			answer, once := strings.CutPrefix(rule[1], "once:")
			used[index] = once
			if message, failed := strings.CutPrefix(answer, "!"); failed {
				return nil, errors.New(message)
			}
			return []byte(answer), nil
		}
		t.Errorf("unexpected call: %s", joined)
		return nil, errors.New("unexpected call")
	}
	t.Cleanup(func() { runTool = previous })
	return calls
}

// The GitHub answers of a first share by octocat: no package upstream yet, no
// branch, no pull request.
func firstShare() [][2]string {
	return [][2]string{
		{"api user --jq .login", "octocat\n"},
		{"POST repos/edkadigital/apps/forks", `{"full_name":"octocat/apps","default_branch":"main","owner":{"login":"octocat"}}`},
		{"GET repos/octocat/apps/git/ref/heads/main", `{"ref":"refs/heads/main"}`},
		{"POST repos/octocat/apps/merge-upstream", `{"merge_type":"none"}`},
		{"GET repos/edkadigital/apps/git/ref/heads/main", `{"object":{"sha":"base-commit"}}`},
		{"GET repos/edkadigital/apps/git/commits/base-commit", `{"tree":{"sha":"base-tree"}}`},
		{"GET repos/edkadigital/apps/contents/apps/memos/template.yaml", "!gh: Not Found (HTTP 404)"},
		{"POST repos/octocat/apps/git/blobs", `{"sha":"blob"}`},
		{"POST repos/octocat/apps/git/trees", `{"sha":"tree"}`},
		{"POST repos/octocat/apps/git/commits", `{"sha":"new-commit"}`},
		{"POST repos/octocat/apps/git/refs", `{"ref":"refs/heads/memos-1.2.0"}`},
		{"POST repos/edkadigital/apps/pulls", `{"html_url":"https://github.com/edkadigital/apps/pull/7"}`},
	}
}

func callsTo(calls []ghCall, text string) []ghCall {
	var found []ghCall
	for _, call := range calls {
		if strings.Contains(call.args, text) {
			found = append(found, call)
		}
	}
	return found
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("not JSON: %q", body)
	}
	return value
}

func TestAppsShareOpensAPullRequestFromTheUsersAccount(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate, "icon.png": "\x89PNG\r\n\x1a\n\xff\xfe"})
	server, api := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/validate":                   {communityValid},
		"POST /api/custom-apps/memos/versions/1.2.0/share": {`{"success":true,"data":{}}`},
	})
	calls := fakeGitHub(t, firstShare())

	out, errOut, err := execute(t, server.URL, "apps", "share", dir, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" || !strings.Contains(errOut, "✓ Opened https://github.com/edkadigital/apps/pull/7") {
		t.Fatalf("out %q, err %q", out, errOut)
	}

	// The package is checked with the rules of the community catalog.
	if profile := decode(t, api.bodies["POST /api/custom-apps/validate"])["profile"]; profile != "community" {
		t.Fatalf("validated with profile %v", profile)
	}

	// Every file becomes a blob in the fork, with its bytes intact.
	blobs := callsTo(*calls, "POST repos/octocat/apps/git/blobs")
	if len(blobs) != 5 {
		t.Fatalf("%d blobs for 5 files", len(blobs))
	}
	var contents []string
	for _, blob := range blobs {
		data, _ := base64.StdEncoding.DecodeString(decode(t, blob.stdin)["content"].(string))
		contents = append(contents, string(data))
	}
	if !strings.Contains(strings.Join(contents, "|"), "\x89PNG\r\n\x1a\n\xff\xfe") || !strings.Contains(strings.Join(contents, "|"), `slug: "memos"`) {
		t.Fatalf("blob contents %q", contents)
	}

	// The package is one tree, placed at apps/<slug> on top of the upstream tree.
	trees := callsTo(*calls, "POST repos/octocat/apps/git/trees")
	if len(trees) != 2 {
		t.Fatalf("%d trees", len(trees))
	}
	packageTree := decode(t, trees[0].stdin)
	if _, based := packageTree["base_tree"]; based {
		t.Fatal("the package tree is built on another tree, so files that left the package would stay")
	}
	var paths []string
	for _, entry := range packageTree["tree"].([]any) {
		paths = append(paths, entry.(map[string]any)["path"].(string))
	}
	if strings.Join(paths, ",") != "README.md,chart/Chart.yaml,chart/templates/deployment.yaml,icon.png,template.yaml" {
		t.Fatalf("package tree holds %v", paths)
	}
	rootTree := decode(t, trees[1].stdin)
	placed := rootTree["tree"].([]any)[0].(map[string]any)
	if rootTree["base_tree"] != "base-tree" || placed["path"] != "apps/memos" || placed["mode"] != "040000" || placed["sha"] != "tree" {
		t.Fatalf("root tree %v", rootTree)
	}

	commit := decode(t, callsTo(*calls, "POST repos/octocat/apps/git/commits")[0].stdin)
	if commit["message"] != "Add Memos 1.2.0" || commit["parents"].([]any)[0] != "base-commit" {
		t.Fatalf("commit %v", commit)
	}
	ref := decode(t, callsTo(*calls, "POST repos/octocat/apps/git/refs")[0].stdin)
	if ref["ref"] != "refs/heads/memos-1.2.0" || ref["sha"] != "new-commit" {
		t.Fatalf("ref %v", ref)
	}

	pull := decode(t, callsTo(*calls, "POST repos/edkadigital/apps/pulls")[0].stdin)
	if pull["head"] != "octocat:memos-1.2.0" || pull["base"] != "main" || pull["title"] != "Add Memos 1.2.0" {
		t.Fatalf("pull request %v", pull)
	}
	body := pull["body"].(string)
	for _, expected := range []string{"## Memos 1.2.0", "| Upstream | https://github.com/usememos/memos |", "| License | MIT |", "| Maintainers | @Octocat |", "- Images: `ghcr.io/usememos/memos:0.25.1@sha256:abc`", "- Objects: Deployment, Service × 2", "- Databases: postgresql", "checks/chart.sh apps/memos"} {
		if !strings.Contains(body, expected) {
			t.Errorf("the pull request does not say %q:\n%s", expected, body)
		}
	}

	// Edka records the pull request on the published version.
	recorded := decode(t, api.bodies["POST /api/custom-apps/memos/versions/1.2.0/share"])
	if recorded["pull_request_url"] != "https://github.com/edkadigital/apps/pull/7" {
		t.Fatalf("recorded %v", recorded)
	}
}

func TestAppsShareStopsAtFindingsBeforeItTouchesGitHub(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {communityInvalid}})
	calls := fakeGitHub(t, nil)

	out, _, err := execute(t, server.URL, "apps", "share", dir, "--yes")
	if err == nil || !strings.Contains(err.Error(), "memos 1.2.0 has 1 error to fix before it can be shared") {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(out, "community.image-digest") || !strings.Contains(out, "Fix: Append @sha256:<digest>.") {
		t.Fatalf("out %q", out)
	}
	if len(*calls) != 0 {
		t.Fatalf("called GitHub: %v", *calls)
	}
}

func TestAppsShareNeedsTheGitHubCLI(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {communityValid}})
	calls := fakeGitHub(t, [][2]string{{"api user", `!exec: "gh": executable file not found in $PATH`}})

	_, _, err := execute(t, server.URL, "apps", "share", dir, "--yes")
	if err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("err %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls %v", *calls)
	}
}

func TestAppsShareComesFromAMaintainerOfThePackage(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {communityValid}})
	calls := fakeGitHub(t, [][2]string{{"api user --jq .login", "mallory\n"}})

	_, _, err := execute(t, server.URL, "apps", "share", dir, "--yes")
	if err == nil || !strings.Contains(err.Error(), "mallory is not under maintainers in template.yaml") || !strings.Contains(err.Error(), `- github: "mallory"`) {
		t.Fatalf("err %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("forked or wrote for an account that is not a maintainer: %v", *calls)
	}
}

func TestAppsShareAsksBeforeItOpensAnything(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {communityValid}})
	calls := fakeGitHub(t, [][2]string{{"api user --jq .login", "octocat\n"}})

	_, _, err := execute(t, server.URL, "apps", "share", dir)
	if err == nil || !strings.Contains(err.Error(), "Open a pull request to edkadigital/apps with memos 1.2.0, from the GitHub account octocat requires confirmation; rerun with --yes") {
		t.Fatalf("err %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls %v", *calls)
	}
}

func TestAppsShareUpdatesAPackageAndAPullRequestThatExist(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {communityValid}})
	rules := [][2]string{
		{"GET repos/edkadigital/apps/contents/apps/memos/template.yaml", `{"name":"template.yaml"}`},
		{"POST repos/octocat/apps/git/refs", "!gh: Reference already exists (HTTP 422)"},
		{"PATCH repos/octocat/apps/git/refs/heads/memos-1.2.0", `{"ref":"refs/heads/memos-1.2.0"}`},
		{"POST repos/edkadigital/apps/pulls", "!gh: A pull request already exists for octocat:memos-1.2.0 (HTTP 422)"},
		{"GET repos/edkadigital/apps/pulls?state=open&head=octocat%3Amemos-1.2.0", "https://github.com/edkadigital/apps/pull/7\n"},
	}
	calls := fakeGitHub(t, append(rules, firstShare()...))

	out, _, err := execute(t, server.URL, "apps", "share", dir, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	data := decode(t, out)["data"].(map[string]any)
	// The share is not recorded: this organization did not publish the package.
	if data["pull_request_url"] != "https://github.com/edkadigital/apps/pull/7" || data["branch"] != "memos-1.2.0" || data["recorded"] != false {
		t.Fatalf("data %v", data)
	}
	if commit := decode(t, callsTo(*calls, "POST repos/octocat/apps/git/commits")[0].stdin); commit["message"] != "Update Memos to 1.2.0" {
		t.Fatalf("commit %v", commit)
	}
	moved := decode(t, callsTo(*calls, "PATCH repos/octocat/apps/git/refs/heads/memos-1.2.0")[0].stdin)
	if moved["sha"] != "new-commit" || moved["force"] != true {
		t.Fatalf("moved %v", moved)
	}
}

func indexOfCall(calls []ghCall, text string) int {
	for index, call := range calls {
		if strings.Contains(call.args, text) {
			return index
		}
	}
	return -1
}

func TestAppsShareWaitsForANewForkBeforeItWritesToIt(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/validate":                   {communityValid},
		"POST /api/custom-apps/memos/versions/1.2.0/share": {`{"success":true,"data":{}}`},
	})
	// GitHub answers the fork request before the fork is there: first it is
	// not found, then it is a repository without commits.
	rules := [][2]string{
		{"GET repos/octocat/apps/git/ref/heads/main", "once:!gh: Not Found (HTTP 404)"},
		{"GET repos/octocat/apps/git/ref/heads/main", "once:!gh: Git Repository is empty. (HTTP 409)"},
	}
	calls := fakeGitHub(t, append(rules, firstShare()...))

	_, errOut, err := execute(t, server.URL, "apps", "share", dir, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if reads := callsTo(*calls, "GET repos/octocat/apps/git/ref/heads/main"); len(reads) != 3 {
		t.Fatalf("read the fork %d times", len(reads))
	}
	ready := -1
	for index, call := range *calls {
		if strings.Contains(call.args, "GET repos/octocat/apps/git/ref/heads/main") {
			ready = index
		}
	}
	for _, write := range []string{"POST repos/octocat/apps/merge-upstream", "POST repos/octocat/apps/git/blobs"} {
		if at := indexOfCall(*calls, write); at < ready {
			t.Fatalf("%s came before the fork was ready: %v", write, *calls)
		}
	}
	if strings.Count(errOut, "Waiting for GitHub to finish creating the fork octocat/apps") != 1 || !strings.Contains(errOut, "✓ Opened https://github.com/edkadigital/apps/pull/7") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestAppsShareStopsWhenGitHubDoesNotFinishTheFork(t *testing.T) {
	previous := forkReadyWait
	forkReadyWait = 30 * time.Millisecond
	t.Cleanup(func() { forkReadyWait = previous })
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, api := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {communityValid}})
	rules := [][2]string{{"GET repos/octocat/apps/git/ref/heads/main", "!gh: Not Found (HTTP 404)"}}
	calls := fakeGitHub(t, append(rules, firstShare()...))

	_, _, err := execute(t, server.URL, "apps", "share", dir, "--yes")
	if err == nil || !strings.Contains(err.Error(), "GitHub has not finished creating the fork octocat/apps; run the command again in a minute") {
		t.Fatalf("err %v", err)
	}
	if writes := callsTo(*calls, "POST repos/octocat/apps/"); len(writes) != 0 {
		t.Fatalf("wrote to a fork that is not there: %v", writes)
	}
	if api.count("POST /api/custom-apps/memos/versions/1.2.0/share") != 0 {
		t.Fatal("recorded a share that did not happen")
	}
}

func TestAppsShareStopsWhenTheForkCannotBeRead(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{"POST /api/custom-apps/validate": {communityValid}})
	rules := [][2]string{{"GET repos/octocat/apps/git/ref/heads/main", "!gh: Resource not accessible by personal access token (HTTP 403)"}}
	calls := fakeGitHub(t, append(rules, firstShare()...))

	_, errOut, err := execute(t, server.URL, "apps", "share", dir, "--yes")
	if err == nil || !strings.Contains(err.Error(), "cannot read the fork octocat/apps") || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("err %v", err)
	}
	// An answer that will not change is not waited on.
	if reads := callsTo(*calls, "GET repos/octocat/apps/git/ref/heads/main"); len(reads) != 1 || strings.Contains(errOut, "Waiting for GitHub") {
		t.Fatalf("reads %d, stderr %q", len(reads), errOut)
	}
}

func TestAppsShareReadsTheDefaultBranchOfAForkThatRenamedIt(t *testing.T) {
	dir := writePackage(t, map[string]string{"template.yaml": sharedTemplate})
	server, _ := newStepAPI(t, map[string][]string{
		"POST /api/custom-apps/validate":                   {communityValid},
		"POST /api/custom-apps/memos/versions/1.2.0/share": {`{"success":true,"data":{}}`},
	})
	rules := [][2]string{
		{"POST repos/edkadigital/apps/forks", `{"full_name":"octocat/apps","default_branch":"trunk","owner":{"login":"octocat"}}`},
		{"GET repos/octocat/apps/git/ref/heads/trunk", `{"ref":"refs/heads/trunk"}`},
	}
	calls := fakeGitHub(t, append(rules, firstShare()...))

	if _, _, err := execute(t, server.URL, "apps", "share", dir, "--yes"); err != nil {
		t.Fatal(err)
	}
	if synced := decode(t, callsTo(*calls, "POST repos/octocat/apps/merge-upstream")[0].stdin); synced["branch"] != "trunk" {
		t.Fatalf("synced %v", synced)
	}
	// The pull request still goes to the main branch of the community repository.
	if pull := decode(t, callsTo(*calls, "POST repos/edkadigital/apps/pulls")[0].stdin); pull["base"] != "main" {
		t.Fatalf("pull request %v", pull)
	}
}

// packageArchive packs files the way Edka serves a published version.
func packageArchive(t *testing.T, entries map[string]string, links map[string]string) string {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(zipped)
	for name, content := range entries {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		if err := writer.WriteHeader(&tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink}); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	zipped.Close()
	return buffer.String()
}

func TestAppsShareTakesThePublishedVersionOfAPackage(t *testing.T) {
	archive := packageArchive(t, map[string]string{"memos/template.yaml": sharedTemplate, "memos/chart/Chart.yaml": "name: memos\n"}, nil)
	server, api := newStepAPI(t, map[string][]string{
		"GET /api/custom-apps/memos":                        {`{"data":{"app":{"slug":"memos"},"versions":[{"version":"1.2.0","current":true},{"version":"1.1.0","current":false}]}}`},
		"GET /api/custom-apps/memos/versions/1.2.0/archive": {archive},
		"POST /api/custom-apps/validate":                    {communityValid},
		"POST /api/custom-apps/memos/versions/1.2.0/share":  {`{"success":true,"data":{}}`},
	})
	calls := fakeGitHub(t, firstShare())

	out, _, err := execute(t, server.URL, "apps", "share", "memos", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if data := decode(t, out)["data"].(map[string]any); data["recorded"] != true || data["version"] != "1.2.0" {
		t.Fatalf("data %v", data)
	}
	files := sentFiles(t, api.bodies["POST /api/custom-apps/validate"])
	if len(files) != 2 || files["template.yaml"] != sharedTemplate || files["chart/Chart.yaml"] != "name: memos\n" {
		t.Fatalf("validated %v", files)
	}
	if len(callsTo(*calls, "POST repos/octocat/apps/git/blobs")) != 2 {
		t.Fatalf("calls %v", *calls)
	}
}

func TestUnpackPackageTakesPlainFilesOfThePackageOnly(t *testing.T) {
	files, err := unpackPackage([]byte(packageArchive(t, map[string]string{"memos/template.yaml": "slug: memos\n"}, map[string]string{"memos/link": "/etc/passwd"})), "memos")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files["template.yaml"] != "slug: memos\n" {
		t.Fatalf("files %v", files)
	}

	for name, entries := range map[string]map[string]string{
		"a file of another directory": {"memos/template.yaml": "x", "other/file": "x"},
		"a path that leaves":          {"memos/template.yaml": "x", "memos/../../etc/passwd": "x"},
		"no template":                 {"memos/README.md": "x"},
	} {
		if _, err := unpackPackage([]byte(packageArchive(t, entries, nil)), "memos"); err == nil {
			t.Errorf("accepted an archive with %s", name)
		}
	}
	if _, err := unpackPackage([]byte("not an archive"), "memos"); err == nil {
		t.Error("accepted bytes that are not an archive")
	}
}
