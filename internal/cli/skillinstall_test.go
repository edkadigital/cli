package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// skillSource is skills/edka, found before a test changes directory.
var skillSource, _ = filepath.Abs(skillDir)

// skillHome points the user's home directory at a new directory, and returns
// the directory a user install writes the skill to.
func skillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(home, ".claude", "skills", "edka")
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

// checkSkill fails unless dir holds the skill in skills/edka and a manifest of
// it, and nothing else.
func checkSkill(t *testing.T, dir string) {
	t.Helper()
	want := map[string][]byte{}
	err := filepath.WalkDir(skillSource, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(skillSource, file)
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
		name := filepath.ToSlash(rel)
		if name == skillManifest {
			return nil
		}
		got++
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if string(content) != string(want[name]) || record.Files[name] != skillHash(want[name]) {
			t.Errorf("%s differs from skills/edka/%s", file, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != len(want) || want["SKILL.md"] == nil || want["references/tools.md"] == nil {
		t.Fatalf("%d files, want %d", got, len(want))
	}
}

func TestSkillInstallWritesTheSkillOfThisVersion(t *testing.T) {
	target := skillHome(t)
	result, err := installSkillJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != target || result.Version != "test" || len(result.Removed) != 0 || !slices.Contains(result.Files, "SKILL.md") || !slices.Contains(result.Files, skillManifest) {
		t.Fatalf("%+v", result)
	}
	checkSkill(t, target)
	// Progress goes to stderr, and a table run prints nothing on stdout.
	out, errOut, err := execute(t, "http://127.0.0.1:1", "skill", "install")
	if err != nil || out != "" || !strings.Contains(errOut, "✓ Installed the skill for edka test in "+target) {
		t.Fatal(out, errOut, err)
	}
}

func TestSkillInstallReplacesWhatAnEarlierInstallWrote(t *testing.T) {
	target := skillHome(t)
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
	result, err := installSkillJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Removed, []string{"references/gone.md"}) {
		t.Fatalf("%+v", result)
	}
	checkSkill(t, target)
}

func TestSkillInstallAsksBeforeReplacingOtherFiles(t *testing.T) {
	target := skillHome(t)
	if _, err := installSkillJSON(t); err != nil {
		t.Fatal(err)
	}
	skill, _ := os.ReadFile(filepath.Join(target, "SKILL.md"))
	edited := append(slices.Clip(skill), "My own rule.\n"...)
	for name, content := range map[string][]byte{"notes.md": []byte("mine\n"), "SKILL.md": edited} {
		if err := os.WriteFile(filepath.Join(target, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := installSkillJSON(t)
	if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "(SKILL.md, notes.md)") {
		t.Fatal(err)
	}
	// Without confirmation nothing changes.
	if got, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); string(got) != string(edited) {
		t.Fatal("SKILL.md was replaced")
	}
	if _, err := os.Stat(filepath.Join(target, "notes.md")); err != nil {
		t.Fatal(err)
	}
	result, err := installSkillJSON(t, "--yes")
	if err != nil || !slices.Equal(result.Removed, []string{"notes.md"}) {
		t.Fatalf("%+v %v", result, err)
	}
	checkSkill(t, target)
	// The temporary directories of the install are gone.
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Fatal(entries)
	}
}

func TestSkillInstallTakesOverACopyOfTheSameSkill(t *testing.T) {
	target := skillHome(t)
	// A copy of skills/edka, made by hand on Windows with its line breaks.
	err := filepath.WalkDir(skillSource, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(skillSource, file)
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		copied := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(copied), 0o755); err != nil {
			return err
		}
		return os.WriteFile(copied, []byte(strings.ReplaceAll(string(content), "\n", "\r\n")), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installSkillJSON(t); err != nil {
		t.Fatal(err)
	}
	checkSkill(t, target)
}

func TestSkillInstallAsksBeforeReplacingALink(t *testing.T) {
	target := skillHome(t)
	shared := filepath.Join(t.TempDir(), "edka")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "SKILL.md"), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, target); err != nil {
		t.Skip("symbolic links need privileges here:", err)
	}
	if _, err := installSkillJSON(t); err == nil || !strings.Contains(err.Error(), "Replace the link at "+target) {
		t.Fatal(err)
	}
	if _, err := installSkillJSON(t, "--yes"); err != nil {
		t.Fatal(err)
	}
	checkSkill(t, target)
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
	want := filepath.Join(repository, ".claude", "skills", "edka")
	sameDir(t, result.Path, want)
	checkSkill(t, want)

	// Outside a repository the project is the current directory.
	plain := t.TempDir()
	t.Chdir(plain)
	if result, err = installSkillJSON(t, "--project"); err != nil {
		t.Fatal(err)
	}
	sameDir(t, result.Path, filepath.Join(plain, ".claude", "skills", "edka"))

	agents := filepath.Join(t.TempDir(), "agents", "skills")
	if result, err = installSkillJSON(t, "--dir", agents); err != nil || result.Path != filepath.Join(agents, "edka") {
		t.Fatalf("%+v %v", result, err)
	}
	checkSkill(t, result.Path)

	if _, err := installSkillJSON(t, "--project", "--dir", agents); err == nil {
		t.Fatal("--project and --dir together installed the skill")
	}
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
		target := skillHome(t)
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
		staging, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".edka-skill-*"))
		if len(staging) != map[bool]int{true: 0, false: 1}[restores] {
			t.Fatalf("restores %v: %v", restores, staging)
		}
	}
}
