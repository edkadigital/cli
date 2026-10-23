package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/edkadigital/cli/skills"
)

// skillsSource is the skills directory of the repository, found before a test
// changes directory.
var skillsSource, _ = filepath.Abs("../../skills")

// skillHome points the user's home directory at a new directory where Claude
// Code is the only coding agent, and returns the skills directory it reads.
func skillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "HERMES_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(name, "")
	}
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".claude", "skills")
}

// homeOf is the home directory of the skills directory skillHome returns.
func homeOf(parent string) string {
	return filepath.Dir(filepath.Dir(parent))
}

func installSkillJSON(t *testing.T, args ...string) (skillInstall, error) {
	t.Helper()
	out, _, err := execute(t, "http://127.0.0.1:1", append([]string{"skill", "install", "--json"}, args...)...)
	var result skillInstall
	if err == nil {
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(out, err)
		}
	}
	return result, err
}

// onlyDir is the one directory an install wrote to.
func onlyDir(t *testing.T, result skillInstall) installedDir {
	t.Helper()
	if len(result.Directories) != 1 {
		t.Fatalf("%+v", result)
	}
	return result.Directories[0]
}

// skillNamesOf lists the names of the skills an install wrote to dir.
func skillNamesOf(dir installedDir) []string {
	var names []string
	for _, installed := range dir.Skills {
		names = append(names, installed.Name)
	}
	return names
}

// checkSkill fails unless dir holds the skill in skills/<name> and a manifest
// of it, and nothing else.
func checkSkill(t *testing.T, dir, name string) {
	t.Helper()
	source := filepath.Join(skillsSource, name)
	want := map[string][]byte{}
	err := filepath.WalkDir(source, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(source, file)
		want[filepath.ToSlash(rel)], err = os.ReadFile(file)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var record skillRecord
	data, err := os.ReadFile(filepath.Join(dir, skillManifest))
	if err != nil || json.Unmarshal(data, &record) != nil {
		t.Fatal(string(data), err)
	}
	if record.Version != 1 || record.CLI != "test" || len(record.Files) != len(want) {
		t.Fatalf("%+v", record)
	}
	got := 0
	err = filepath.WalkDir(dir, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, file)
		file = filepath.ToSlash(rel)
		if file == skillManifest {
			return nil
		}
		got++
		content, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return err
		}
		if string(content) != string(want[file]) || record.Files[file] != skillHash(want[file]) {
			t.Errorf("%s/%s differs from skills/%s/%s", dir, file, name, file)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != len(want) || want["SKILL.md"] == nil {
		t.Fatalf("%s: %d files, want %d", name, got, len(want))
	}
}

// checkSkills fails unless parent holds each skill of the binary, and nothing
// else.
func checkSkills(t *testing.T, parent string) {
	t.Helper()
	for _, name := range skills.Names {
		checkSkill(t, filepath.Join(parent, name), name)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != len(skills.Names) {
		t.Fatal(entries)
	}
}

func TestSkillInstallWritesTheSkillsOfThisVersion(t *testing.T) {
	parent := skillHome(t)
	result, err := installSkillJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	dir := onlyDir(t, result)
	if dir.Path != parent || result.Version != "test" || !slices.Equal(dir.Agents, []string{"claude-code"}) || !slices.Equal(skillNamesOf(dir), []string{"edka", "edka-app"}) {
		t.Fatalf("%+v", result)
	}
	for _, installed := range dir.Skills {
		if installed.Path != filepath.Join(parent, installed.Name) || len(installed.Removed) != 0 || !slices.Contains(installed.Files, "SKILL.md") || !slices.Contains(installed.Files, skillManifest) {
			t.Fatalf("%+v", installed)
		}
	}
	checkSkills(t, parent)
	// Progress goes to stderr, and a table run prints nothing on stdout.
	out, errOut, err := execute(t, "http://127.0.0.1:1", "skill", "install")
	if err != nil || out != "" || errOut != "✓ Installed the edka and edka-app skills of edka test in "+parent+" for Claude Code\n" {
		t.Fatal(out, errOut, err)
	}
}

func TestSkillInstallWritesTheNamedSkills(t *testing.T) {
	parent := skillHome(t)
	out, errOut, err := execute(t, "http://127.0.0.1:1", "skill", "install", "edka-app")
	if err != nil || out != "" || errOut != "✓ Installed the edka-app skill of edka test in "+parent+" for Claude Code\n" {
		t.Fatal(out, errOut, err)
	}
	checkSkill(t, filepath.Join(parent, "edka-app"), "edka-app")
	if _, err := os.Stat(filepath.Join(parent, "edka")); !os.IsNotExist(err) {
		t.Fatal("installed the edka skill", err)
	}
	for args, want := range map[string]string{
		"edka nope": `unknown skill "nope"; the skills are edka, edka-app`,
		"edka edka": `skill "edka" is named twice`,
	} {
		if _, err := installSkillJSON(t, strings.Fields(args)...); err == nil || err.Error() != want {
			t.Errorf("%s: %v", args, err)
		}
	}
}

func TestSkillInstallReplacesWhatAnEarlierInstallWrote(t *testing.T) {
	parent := skillHome(t)
	target := filepath.Join(parent, "edka")
	if _, err := installSkillJSON(t); err != nil {
		t.Fatal(err)
	}
	// An earlier version wrote another SKILL.md and a reference this one has no
	// command group for.
	record := skillRecord{Version: 1, CLI: "v0.1.0", Files: map[string]string{}}
	data, _ := os.ReadFile(filepath.Join(target, skillManifest))
	_ = json.Unmarshal(data, &record)
	for name, content := range map[string]string{"SKILL.md": "old skill\n", "references/gone.md": "old reference\n"} {
		if err := os.WriteFile(filepath.Join(target, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		record.Files[name] = skillHash([]byte(content))
	}
	data, _ = json.Marshal(record)
	if err := os.WriteFile(filepath.Join(target, skillManifest), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := execute(t, "http://127.0.0.1:1", "skill", "install")
	if err != nil || !strings.HasSuffix(errOut, "\n  Removed edka/references/gone.md\n") {
		t.Fatal(errOut, err)
	}
	checkSkills(t, parent)
}

func TestSkillInstallAsksBeforeReplacingOtherFiles(t *testing.T) {
	parent := skillHome(t)
	if _, err := installSkillJSON(t); err != nil {
		t.Fatal(err)
	}
	// The manifest of edka shows whether the next install rewrote it.
	manifest := filepath.Join(parent, "edka", skillManifest)
	data, _ := os.ReadFile(manifest)
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(data), `"cli_version": "test"`, `"cli_version": "v0.1.0"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "edka-app")
	skill, _ := os.ReadFile(filepath.Join(target, "SKILL.md"))
	edited := append(slices.Clip(skill), "My own rule.\n"...)
	for name, content := range map[string][]byte{"notes.md": []byte("mine\n"), "SKILL.md": edited} {
		if err := os.WriteFile(filepath.Join(target, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := installSkillJSON(t)
	if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "Replace "+target+" and the files in it that an earlier install did not write (SKILL.md, notes.md)") {
		t.Fatal(err)
	}
	// Without confirmation nothing changes, in edka-app nor in the edka skill
	// the install would have written first.
	if got, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); string(got) != string(edited) {
		t.Fatal("SKILL.md was replaced")
	}
	if _, err := os.Stat(filepath.Join(target, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(manifest); !strings.Contains(string(got), "v0.1.0") {
		t.Fatal("the edka skill was replaced")
	}
	result, err := installSkillJSON(t, "--yes")
	if err != nil || len(result.Directories) != 1 || !slices.Equal(result.Directories[0].Skills[1].Removed, []string{"notes.md"}) {
		t.Fatalf("%+v %v", result, err)
	}
	// checkSkills also finds no temporary directory of the install.
	checkSkills(t, parent)
}

func TestSkillInstallTakesOverACopyOfTheSameSkills(t *testing.T) {
	parent := skillHome(t)
	// A copy of skills/edka and skills/edka-app, made by hand on Windows with
	// its line breaks.
	for _, name := range skills.Names {
		source := filepath.Join(skillsSource, name)
		err := filepath.WalkDir(source, func(file string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(source, file)
			content, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			copied := filepath.Join(parent, name, rel)
			if err := os.MkdirAll(filepath.Dir(copied), 0o755); err != nil {
				return err
			}
			return os.WriteFile(copied, []byte(strings.ReplaceAll(string(content), "\n", "\r\n")), 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := installSkillJSON(t); err != nil {
		t.Fatal(err)
	}
	checkSkills(t, parent)
}

func TestSkillInstallAsksBeforeReplacingALink(t *testing.T) {
	parent := skillHome(t)
	target := filepath.Join(parent, "edka")
	shared := filepath.Join(t.TempDir(), "edka")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "SKILL.md"), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, target); err != nil {
		t.Skip("symbolic links need privileges here:", err)
	}
	if _, err := installSkillJSON(t); err == nil || !strings.Contains(err.Error(), "Replace the link at "+target+" with the edka skill") {
		t.Fatal(err)
	}
	if _, err := installSkillJSON(t, "--yes"); err != nil {
		t.Fatal(err)
	}
	checkSkills(t, parent)
	// The directory the link pointed to stays as it was.
	if got, err := os.ReadFile(filepath.Join(shared, "SKILL.md")); err != nil || string(got) != "shared\n" {
		t.Fatal(string(got), err)
	}
}

func TestSkillInstallInTheProjectOrADirectory(t *testing.T) {
	skillHome(t)
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repository, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	result, err := installSkillJSON(t, "--project")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repository, ".claude", "skills")
	sameDir(t, onlyDir(t, result).Path, want)
	checkSkills(t, want)

	// Outside a repository the project is the current directory.
	plain := t.TempDir()
	t.Chdir(plain)
	if result, err = installSkillJSON(t, "--project"); err != nil {
		t.Fatal(err)
	}
	sameDir(t, onlyDir(t, result).Path, filepath.Join(plain, ".claude", "skills"))

	// A directory is for no agent in particular.
	agents := filepath.Join(t.TempDir(), "agents", "skills")
	if result, err = installSkillJSON(t, "--dir", agents); err != nil || onlyDir(t, result).Path != agents || len(result.Directories[0].Agents) != 0 || result.Directories[0].Skills[0].Path != filepath.Join(agents, "edka") {
		t.Fatalf("%+v %v", result, err)
	}
	checkSkills(t, agents)

	for _, args := range [][]string{{"--project", "--dir", agents}, {"--agent", "cursor", "--dir", agents}} {
		if _, err := installSkillJSON(t, args...); err == nil {
			t.Fatalf("%v installed the skills", args)
		}
	}
}

// Each coding agent installed here gets the skills in the directory it reads.
// Agents that read the same directory share one copy.
func TestSkillInstallWritesForEveryAgentFound(t *testing.T) {
	parent := skillHome(t)
	home := homeOf(parent)
	claude := filepath.Join(t.TempDir(), "claude")
	codex := filepath.Join(t.TempDir(), "codex")
	xdg := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, dir := range []string{claude, codex, filepath.Join(home, ".cursor"), filepath.Join(home, ".cline"), filepath.Join(xdg, "opencode")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, errOut, err := execute(t, "http://127.0.0.1:1", "skill", "install")
	shared := filepath.Join(home, ".agents", "skills")
	want := []struct{ dir, agents string }{
		{filepath.Join(claude, "skills"), "Claude Code"},
		{shared, "Codex and Cline"},
		{filepath.Join(home, ".cursor", "skills"), "Cursor"},
		{filepath.Join(xdg, "opencode", "skills"), "OpenCode"},
	}
	var lines strings.Builder
	for _, target := range want {
		lines.WriteString("✓ Installed the edka and edka-app skills of edka test in " + target.dir + " for " + target.agents + "\n")
	}
	if err != nil || errOut != lines.String() {
		t.Fatalf("stderr=%q err=%v", errOut, err)
	}
	for _, target := range want {
		checkSkills(t, target.dir)
	}
	// ~/.claude holds no skills, since Claude Code reads $CLAUDE_CONFIG_DIR.
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	result, err := installSkillJSON(t)
	if err != nil || len(result.Directories) != 4 || result.Directories[1].Path != shared || !slices.Equal(result.Directories[1].Agents, []string{"codex", "cline"}) {
		t.Fatalf("%+v %v", result, err)
	}

	// --project writes to the agents' directories in the repository.
	repository := t.TempDir()
	t.Chdir(repository)
	if result, err = installSkillJSON(t, "--project"); err != nil || len(result.Directories) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	sameDir(t, result.Directories[0].Path, filepath.Join(repository, ".claude", "skills"))
	sameDir(t, result.Directories[1].Path, filepath.Join(repository, ".agents", "skills"))
	if !slices.Equal(result.Directories[1].Agents, []string{"codex", "cursor", "opencode", "cline"}) {
		t.Fatalf("%+v", result.Directories[1])
	}
}

func TestSkillInstallForTheNamedAgents(t *testing.T) {
	parent := skillHome(t)
	home := homeOf(parent)
	// Neither agent is installed, and Claude Code, which is, gets nothing.
	result, err := installSkillJSON(t, "--agent", "windsurf,cursor", "--agent", "cursor")
	if err != nil || len(result.Directories) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	for i, want := range []installedDir{
		{Path: filepath.Join(home, ".codeium", "windsurf", "skills"), Agents: []string{"windsurf"}},
		{Path: filepath.Join(home, ".cursor", "skills"), Agents: []string{"cursor"}},
	} {
		if got := result.Directories[i]; got.Path != want.Path || !slices.Equal(got.Agents, want.Agents) {
			t.Fatalf("%+v", got)
		}
		checkSkills(t, want.Path)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := installSkillJSON(t, "--agent", "nope"); err == nil || !strings.HasPrefix(err.Error(), `unknown agent "nope"; the agents are claude-code, codex, cursor`) {
		t.Fatal(err)
	}
	// The help lists the agents --agent takes.
	help, _, err := execute(t, "http://127.0.0.1:1", "skill", "install", "--help")
	if err != nil || !strings.Contains(strings.Join(strings.Fields(help), " "), "found or not: "+andList(codingAgentNames())+".") {
		t.Fatal(help, err)
	}
}

func TestSkillInstallWithNoAgentFound(t *testing.T) {
	parent := skillHome(t)
	home := homeOf(parent)
	if err := os.Remove(filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := execute(t, "http://127.0.0.1:1", "skill", "install")
	shared := filepath.Join(home, ".agents", "skills")
	want := "Found no coding agent, so the install is for Claude Code and Codex.\n" +
		"✓ Installed the edka and edka-app skills of edka test in " + parent + " for Claude Code\n" +
		"✓ Installed the edka and edka-app skills of edka test in " + shared + " for Codex\n"
	if err != nil || errOut != want {
		t.Fatalf("stderr=%q err=%v", errOut, err)
	}
	checkSkills(t, parent)
	checkSkills(t, shared)
}

// sameDir fails unless got and want name the same directory, through links such
// as macOS's /var to /private/var.
func sameDir(t *testing.T, got, want string) {
	t.Helper()
	gotReal, err1 := filepath.EvalSymlinks(got)
	wantReal, err2 := filepath.EvalSymlinks(want)
	if err1 != nil || err2 != nil || gotReal != wantReal {
		t.Fatalf("installed in %s, want %s", got, want)
	}
}

// A swap that fails puts the previous skill back, and when it can't, keeps it
// where the error says.
func TestSkillInstallKeepsThePreviousSkillWhenTheSwapFails(t *testing.T) {
	for _, restores := range []bool{true, false} {
		parent := skillHome(t)
		target := filepath.Join(parent, "edka")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("my skill\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rename := renameSkill
		renameSkill = func(from, to string) error {
			if filepath.Base(from) == "next" || (!restores && filepath.Base(from) == "previous") {
				return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
			}
			return os.Rename(from, to)
		}
		_, err := installSkillJSON(t, "--yes")
		renameSkill = rename
		if err == nil {
			t.Fatal("the install succeeded")
		}
		kept := target
		if !restores {
			_, path, found := strings.Cut(err.Error(), "the previous skill is in ")
			if !found {
				t.Fatal(err)
			}
			kept = path
		}
		if data, err := os.ReadFile(filepath.Join(kept, "SKILL.md")); err != nil || string(data) != "my skill\n" {
			t.Fatalf("restores %v: %q, %v", restores, data, err)
		}
		// Only a staging directory that holds the previous skill is left.
		staging, _ := filepath.Glob(filepath.Join(parent, ".edka-skill-*"))
		if len(staging) != map[bool]int{true: 0, false: 1}[restores] {
			t.Fatalf("restores %v: %v", restores, staging)
		}
	}
}

// An install that fails after it wrote a skill says which one it wrote.
func TestSkillInstallReportsTheSkillsItWroteBeforeAFailure(t *testing.T) {
	parent := skillHome(t)
	rename := renameSkill
	t.Cleanup(func() { renameSkill = rename })
	renameSkill = func(from, to string) error {
		if filepath.Base(to) == "edka-app" {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
		}
		return os.Rename(from, to)
	}
	_, errOut, err := execute(t, "http://127.0.0.1:1", "skill", "install")
	if err == nil || !strings.HasPrefix(err.Error(), "write the edka-app skill: ") || errOut != "✓ Installed the edka skill of edka test in "+parent+" for Claude Code\n" {
		t.Fatal(errOut, err)
	}
	checkSkill(t, filepath.Join(parent, "edka"), "edka")
	if _, err := os.Stat(filepath.Join(parent, "edka-app")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
