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
	"github.com/edkadigital/cli/skills"
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

// installOldSkills has edka v1.2.3 install skills in the user's skills
// directory parent: the named ones, or all. Its edka skill has another SKILL.md
// and a reference the current skill does not have.
func installOldSkills(t *testing.T, parent string, names ...string) {
	t.Helper()
	root := New("v1.2.3", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs(append([]string{"skill", "install"}, names...))
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "edka")
	if _, err := os.Stat(target); err != nil {
		return
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
	Skills  []skillRefresh
}

func upgradeJSON(t *testing.T, out string) upgradeResult {
	t.Helper()
	var result upgradeResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(out, err)
	}
	return result
}

// installArgs are the arguments an upgrade runs the release with to write the
// skills names in parent.
func installArgs(parent string, names ...string) []string {
	return append([]string{"skill", "install", "--dir", parent, "--no-input", "--json"}, names...)
}

func TestUpgradeHasTheReleaseRewriteTheSkills(t *testing.T) {
	parent := skillHome(t)
	installOldSkills(t, parent)
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--json")
	if err != nil || errOut != "✓ Upgraded edka v1.2.3 to v1.3.0\n✓ Refreshed the edka and edka-app skills in "+parent+" for edka v1.3.0\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	want := []skillRefresh{{Name: "edka", Path: filepath.Join(parent, "edka"), Refreshed: true}, {Name: "edka-app", Path: filepath.Join(parent, "edka-app"), Refreshed: true}}
	if result := upgradeJSON(t, out); !result.Updated || !slices.Equal(result.Skills, want) {
		t.Fatalf("%+v", result)
	}
	if want := [][]string{installArgs(parent, "edka", "edka-app")}; !slices.EqualFunc(*runs, want, slices.Equal) {
		t.Fatal(*runs)
	}
	// edka v1.3.0 wrote its skills in place of the files v1.2.3 wrote.
	for _, name := range skills.Names {
		if record := skillRecordOf(t, filepath.Join(parent, name)); record.CLI != "v1.3.0" {
			t.Fatalf("%s: %+v", name, record)
		}
	}
	got, _ := os.ReadFile(filepath.Join(parent, "edka", "SKILL.md"))
	source, _ := os.ReadFile(filepath.Join(skillsSource, "edka", "SKILL.md"))
	if len(source) == 0 || !bytes.Equal(got, source) {
		t.Fatal("SKILL.md is not the skill of v1.3.0")
	}
	if _, err := os.Stat(filepath.Join(parent, "edka", "references", "gone.md")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// The release rewrites the skills in the directory of each agent, found now or
// not, that an earlier install wrote to.
func TestUpgradeRewritesTheSkillsOfEveryAgent(t *testing.T) {
	parent := skillHome(t)
	installOldSkills(t, parent)
	cursor := filepath.Join(homeOf(parent), ".cursor", "skills")
	root := New("v1.2.3", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs([]string{"skill", "install", "--agent", "cursor"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--json")
	want := "✓ Upgraded edka v1.2.3 to v1.3.0\n✓ Refreshed the edka and edka-app skills in " + parent + " for edka v1.3.0\n✓ Refreshed the edka and edka-app skills in " + cursor + " for edka v1.3.0\n"
	if err != nil || errOut != want {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if result := upgradeJSON(t, out); len(result.Skills) != 4 || !result.Skills[3].Refreshed || result.Skills[3].Path != filepath.Join(cursor, "edka-app") {
		t.Fatalf("%+v", result)
	}
	if want := [][]string{installArgs(parent, "edka", "edka-app"), installArgs(cursor, "edka", "edka-app")}; !slices.EqualFunc(*runs, want, slices.Equal) {
		t.Fatal(*runs)
	}
	if record := skillRecordOf(t, filepath.Join(cursor, "edka")); record.CLI != "v1.3.0" {
		t.Fatalf("%+v", record)
	}
}

// The release writes the skills the user installed, and no other.
func TestUpgradeRewritesOnlyTheInstalledSkills(t *testing.T) {
	parent := skillHome(t)
	installOldSkills(t, parent, "edka")
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade")
	if err != nil || errOut != "✓ Upgraded edka v1.2.3 to v1.3.0\n✓ Refreshed the edka skill in "+parent+" for edka v1.3.0\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if want := [][]string{installArgs(parent, "edka")}; !slices.EqualFunc(*runs, want, slices.Equal) {
		t.Fatal(*runs)
	}
	if _, err := os.Stat(filepath.Join(parent, "edka-app")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestUpgradeKeepsASkillWithChangedFiles(t *testing.T) {
	parent := skillHome(t)
	target := filepath.Join(parent, "edka")
	installOldSkills(t, parent)
	for name, content := range map[string]string{"SKILL.md": "old skill\nMy own rule.\n", "notes.md": "mine\n"} {
		if err := os.WriteFile(filepath.Join(target, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	path, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade")
	want := "✓ Upgraded edka v1.2.3 to v1.3.0\n✓ Refreshed the edka-app skill in " + parent + " for edka v1.3.0\nKept the edka skill in " + target + ", since files in it changed after the install (SKILL.md, notes.md). Run `edka skill install` to replace it.\n"
	if err != nil || out != "" || errOut != want || installedBinary(t, path) != "new binary" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	_, out, _, err = asRelease(t, "v1.2.3", release, "upgrade", "--json")
	if result := upgradeJSON(t, out); err != nil || len(result.Skills) != 2 || result.Skills[0] != (skillRefresh{Name: "edka", Path: target, Reason: "files in it changed after the install (SKILL.md, notes.md)"}) || !result.Skills[1].Refreshed {
		t.Fatalf("%+v %v", result, err)
	}
	// The upgrade asked the release to write edka-app alone.
	if want := [][]string{installArgs(parent, "edka-app"), installArgs(parent, "edka-app")}; !slices.EqualFunc(*runs, want, slices.Equal) {
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
	parent := skillHome(t)
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	upgrade := func() {
		t.Helper()
		_, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--json")
		if err != nil || errOut != "✓ Upgraded edka v1.2.3 to v1.3.0\n" || !strings.Contains(out, `"skills": null`) {
			t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
		}
	}
	upgrade()
	// Another tool installed these, such as `npx skills add`.
	for _, name := range skills.Names {
		if err := os.MkdirAll(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, name, "SKILL.md"), []byte("copied\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upgrade()
	if got, _ := os.ReadFile(filepath.Join(parent, "edka", "SKILL.md")); string(got) != "copied\n" {
		t.Fatal("SKILL.md was replaced")
	}
	if len(*runs) != 0 {
		t.Fatal(*runs)
	}
}

// A link at ~/.claude/skills/edka is the user's, even to a skill the CLI wrote.
func TestUpgradeKeepsALinkToASkill(t *testing.T) {
	parent := skillHome(t)
	target := filepath.Join(parent, "edka")
	shared := filepath.Join(t.TempDir(), "skills")
	root := New("v1.2.3", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	root.SetArgs([]string{"skill", "install", "--dir", shared})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "edka"), target); err != nil {
		t.Skip("symbolic links need privileges here:", err)
	}
	runs := releaseRuns(t, "v1.3.0")
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, _, errOut, err := asRelease(t, "v1.2.3", release, "upgrade")
	if err != nil || !strings.Contains(errOut, "Kept the edka skill in "+target+", since it is a link.") || len(*runs) != 0 {
		t.Fatalf("stderr=%q err=%v runs=%v", errOut, err, *runs)
	}
	if record := skillRecordOf(t, target); record.CLI != "v1.2.3" {
		t.Fatalf("%+v", record)
	}
}

// The binary was upgraded, so the upgrade succeeds when the release can't
// write the skills, and says what failed.
func TestUpgradeReportsSkillsTheReleaseCouldNotWrite(t *testing.T) {
	parent := skillHome(t)
	installOldSkills(t, parent)
	tool := runTool
	t.Cleanup(func() { runTool = tool })
	runTool = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return nil, errors.New(`{"error":"unknown command \"skill\" for \"edka\"","exit_code":1}`)
	}
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	path, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--json")
	var want strings.Builder
	want.WriteString("✓ Upgraded edka v1.2.3 to v1.3.0\n")
	for _, name := range skills.Names {
		want.WriteString("Kept the " + name + " skill in " + filepath.Join(parent, name) + ", since edka v1.3.0 could not write it: unknown command \"skill\" for \"edka\". Run `edka skill install` to replace it.\n")
	}
	if err != nil || errOut != want.String() || installedBinary(t, path) != "new binary" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if result := upgradeJSON(t, out); !result.Updated || len(result.Skills) != 2 || result.Skills[0].Refreshed || result.Skills[0].Path != filepath.Join(parent, "edka") {
		t.Fatalf("%+v", result)
	}
	if record := skillRecordOf(t, filepath.Join(parent, "edka")); record.CLI != "v1.2.3" {
		t.Fatalf("%+v", record)
	}
}

// This binary embeds the skills of the version an upgrade replaces, so the
// release writes the new ones. Here the release is a copy of this test binary
// that runs as edka v1.3.0.
func TestUpgradeRunsTheReleaseToRewriteTheSkills(t *testing.T) {
	parent := skillHome(t)
	installOldSkills(t, parent)
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
	if !strings.Contains(output, "✓ Refreshed the edka and edka-app skills in "+parent+" for edka v1.3.0\n") {
		t.Fatal(output)
	}
	if record := skillRecordOf(t, filepath.Join(parent, "edka")); record.CLI != "v1.3.0" || record.Files["references/gone.md"] != "" {
		t.Fatalf("%+v", record)
	}
}
