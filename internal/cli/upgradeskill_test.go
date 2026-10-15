package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/edkadigital/cli/internal/selfupdate/updatetest"
)

// TestMain runs this test binary as edka when a test installs a copy of it as
// a release and the upgrade runs the copy with the arguments of a command.
// EDKA_TEST_RELEASE is the version the copy runs as.
func TestMain(m *testing.M) {
	version := os.Getenv("EDKA_TEST_RELEASE")
	if version == "" || len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-test.") {
		os.Exit(m.Run())
	}
	root := New(version, os.Stdin, os.Stdout, os.Stderr)
	root.SetArgs(os.Args[1:])
	if err := root.Execute(); err != nil {
		// As cmd/edka prints the error of a command run with --json.
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": err.Error(), "exit_code": 1})
		os.Exit(1)
	}
	os.Exit(0)
}

// installOldSkill has edka v1.2.3 install its skill in target, which is the
// user's: one with another SKILL.md and a reference the current skill does
// not have.
func installOldSkill(t *testing.T, target string) {
	t.Helper()
	root := New("v1.2.3", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs([]string{"skill", "install"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	record := skillRecordOf(t, target)
	for name, content := range map[string]string{"SKILL.md": "old skill\n", "references/gone.md": "old reference\n"} {
		if err := os.WriteFile(filepath.Join(target, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		record.Files[name] = skillHash([]byte(content))
	}
	if err := os.WriteFile(filepath.Join(target, skillManifest), jsonBody(record), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillRecordOf(t *testing.T, target string) skillRecord {
	t.Helper()
	var record skillRecord
	data, err := os.ReadFile(filepath.Join(target, skillManifest))
	if err != nil || json.Unmarshal(data, &record) != nil {
		t.Fatal(string(data), err)
	}
	return record
}

// releaseRuns has the binary that an upgrade installed run as edka at version
// when the upgrade runs it, and returns the arguments of each run.
func releaseRuns(t *testing.T, version string) *[][]string {
	t.Helper()
	tool := runTool
	t.Cleanup(func() { runTool = tool })
	var runs [][]string
	runTool = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		runs = append(runs, args)
		if installedBinary(t, name) != "new binary" {
			t.Errorf("ran %s, which is not the release", name)
		}
		var out, errOut bytes.Buffer
		root := New(version, bytes.NewReader(stdin), &out, &errOut)
		root.SetArgs(args)
		if err := root.ExecuteContext(ctx); err != nil {
			return out.Bytes(), errors.New(errOut.String() + string(jsonBody(map[string]any{"error": err.Error(), "exit_code": 1})))
		}
		return out.Bytes(), nil
	}
	return &runs
}

type upgradeResult struct {
	Updated bool
	Skill   *skillRefresh
}

func upgradeJSON(t *testing.T, out string) upgradeResult {
	t.Helper()
	var result upgradeResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(out, err)
	}
	return result
}

func TestUpgradeHasTheReleaseRewriteTheSkill(t *testing.T) {
	target := skillHome(t)
	installOldSkill(t, target)
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--json")
	if err != nil || errOut != "✓ Upgraded edka v1.2.3 to v1.3.0\n✓ Refreshed the agent skill in "+target+" for edka v1.3.0\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if result := upgradeJSON(t, out); !result.Updated || result.Skill == nil || *result.Skill != (skillRefresh{Path: target, Refreshed: true}) {
		t.Fatalf("%+v", result)
	}
	if want := [][]string{{"skill", "install", "--dir", filepath.Dir(target), "--no-input", "--json"}}; !slices.EqualFunc(*runs, want, slices.Equal) {
		t.Fatal(*runs)
	}
	// edka v1.3.0 wrote its skill in place of the files v1.2.3 wrote.
	if record := skillRecordOf(t, target); record.CLI != "v1.3.0" {
		t.Fatalf("%+v", record)
	}
	got, _ := os.ReadFile(filepath.Join(target, "SKILL.md"))
	want, _ := os.ReadFile(filepath.Join(skillSource, "SKILL.md"))
	if len(want) == 0 || !bytes.Equal(got, want) {
		t.Fatal("SKILL.md is not the skill of v1.3.0")
	}
	if _, err := os.Stat(filepath.Join(target, "references", "gone.md")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestUpgradeKeepsASkillWithChangedFiles(t *testing.T) {
	target := skillHome(t)
	installOldSkill(t, target)
	for name, content := range map[string]string{"SKILL.md": "old skill\nMy own rule.\n", "notes.md": "mine\n"} {
		if err := os.WriteFile(filepath.Join(target, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	path, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade")
	want := "✓ Upgraded edka v1.2.3 to v1.3.0\nKept the agent skill in " + target + ", since files in it changed after the install (SKILL.md, notes.md). Run `edka skill install` to replace it.\n"
	if err != nil || out != "" || errOut != want || installedBinary(t, path) != "new binary" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	_, out, _, err = asRelease(t, "v1.2.3", release, "upgrade", "--json")
	if result := upgradeJSON(t, out); err != nil || result.Skill == nil || *result.Skill != (skillRefresh{Path: target, Reason: "files in it changed after the install (SKILL.md, notes.md)"}) {
		t.Fatalf("%+v %v", result, err)
	}
	// The upgrade never asked the release to write the skill.
	if len(*runs) != 0 {
		t.Fatal(*runs)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); string(got) != "old skill\nMy own rule.\n" {
		t.Fatal("SKILL.md was replaced")
	}
	if record := skillRecordOf(t, target); record.CLI != "v1.2.3" {
		t.Fatalf("%+v", record)
	}
}

// A skill directory without a manifest is not one the CLI wrote.
func TestUpgradeLeavesASkillItDidNotInstall(t *testing.T) {
	target := skillHome(t)
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	upgrade := func() {
		t.Helper()
		_, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--json")
		if err != nil || errOut != "✓ Upgraded edka v1.2.3 to v1.3.0\n" || !strings.Contains(out, `"skill": null`) {
			t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
		}
	}
	upgrade()
	// Another tool installed this one, such as `npx skills add`.
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("copied\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	upgrade()
	if got, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); string(got) != "copied\n" {
		t.Fatal("SKILL.md was replaced")
	}
	if len(*runs) != 0 {
		t.Fatal(*runs)
	}
}

// A link at ~/.claude/skills/edka is the user's, even to a skill the CLI wrote.
func TestUpgradeKeepsALinkToASkill(t *testing.T) {
	target := skillHome(t)
	shared := filepath.Join(t.TempDir(), "skills")
	root := New("v1.2.3", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs([]string{"skill", "install", "--dir", shared})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "edka"), target); err != nil {
		t.Skip("symbolic links need privileges here:", err)
	}
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, _, errOut, err := asRelease(t, "v1.2.3", release, "upgrade")
	if err != nil || !strings.Contains(errOut, "Kept the agent skill in "+target+", since it is a link.") || len(*runs) != 0 {
		t.Fatalf("stderr=%q err=%v runs=%v", errOut, err, *runs)
	}
	if record := skillRecordOf(t, target); record.CLI != "v1.2.3" {
		t.Fatalf("%+v", record)
	}
}

// The binary was upgraded, so the upgrade succeeds when the release can't
// write the skill, and says what failed.
func TestUpgradeReportsASkillTheReleaseCouldNotWrite(t *testing.T) {
	target := skillHome(t)
	installOldSkill(t, target)
	tool := runTool
	t.Cleanup(func() { runTool = tool })
	runTool = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return nil, errors.New(`{"error":"unknown command \"skill\" for \"edka\"","exit_code":1}`)
	}
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	path, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--json")
	want := "✓ Upgraded edka v1.2.3 to v1.3.0\nKept the agent skill in " + target + ", since edka v1.3.0 could not write it: unknown command \"skill\" for \"edka\". Run `edka skill install` to replace it.\n"
	if err != nil || errOut != want || installedBinary(t, path) != "new binary" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if result := upgradeJSON(t, out); !result.Updated || result.Skill == nil || result.Skill.Refreshed || result.Skill.Path != target {
		t.Fatalf("%+v", result)
	}
	if record := skillRecordOf(t, target); record.CLI != "v1.2.3" {
		t.Fatalf("%+v", record)
	}
}

// This binary embeds the skill of the version an upgrade replaces, so the
// release writes the new one. Here the release is a copy of this test binary
// that runs as edka v1.3.0.
func TestUpgradeRunsTheReleaseToRewriteTheSkill(t *testing.T) {
	target := skillHome(t)
	installOldSkill(t, target)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	release := updatetest.Publish(t, "v1.3.0", binary)
	_, output := upgradeAsProgram(t, release, "EDKA_TEST_RELEASE=v1.3.0")
	if !strings.Contains(output, "✓ Refreshed the agent skill in "+target+" for edka v1.3.0\n") {
		t.Fatal(output)
	}
	if record := skillRecordOf(t, target); record.CLI != "v1.3.0" || record.Files["references/gone.md"] != "" {
		t.Fatalf("%+v", record)
	}
}
