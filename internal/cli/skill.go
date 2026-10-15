package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/edkadigital/cli/skills"
	"github.com/spf13/cobra"
)

// skillName is the directory of the edka skill in a skills directory.
const skillName = "edka"

// skillManifest is the file in an installed skill that lists the files
// `edka skill install` wrote there, so the next install knows which files it
// may replace without asking.
const skillManifest = ".edka-skill.json"

type skillRecord struct {
	Version int    `json:"version"`
	CLI     string `json:"cli_version"`
	// Files maps each file's slash path to the SHA-256 of its content.
	Files map[string]string `json:"files"`
}

// skillInstall is what an install wrote and removed.
type skillInstall struct {
	Path    string   `json:"path"`
	Version string   `json:"version"`
	Files   []string `json:"files"`
	Removed []string `json:"removed"`
}

func (a *App) addSkill(root *cobra.Command) {
	skill := &cobra.Command{Use: "skill", Short: "Install the agent skill for coding agents", GroupID: "tools"}
	a.strictGroup(skill)
	var project bool
	var dir string
	install := &cobra.Command{Use: "install", Short: "Install the agent skill for Claude Code and other agents", Long: "Write the agent skill that matches this version of the CLI to\n`~/.claude/skills/edka`, where Claude Code finds it. The skill shows a coding agent\nhow to check which cluster and deployment a command acts on, diagnose a failure,\npreview a change and ask before a destructive command. Its references list every\ncommand with its flags.\n\n`--project` writes it to `.claude/skills/edka` at the root of the current\nrepository, to commit with the project. `--dir` writes it to another agent's skills\ndirectory. Run the install again after `edka upgrade` to match the new version.\n\nAn install replaces the files an earlier one wrote. It asks before it replaces a\nskill directory that holds other files.", Args: cobra.NoArgs, Example: "  edka skill install\n  edka skill install --project\n  edka skill install --dir ~/.agents/skills --json", RunE: func(_ *cobra.Command, _ []string) error {
		parent, err := skillsDir(project, dir)
		if err != nil {
			return err
		}
		files, err := fs.Sub(skills.Edka, skillName)
		if err != nil {
			return err
		}
		result, err := a.installSkill(files, filepath.Join(parent, skillName))
		if err != nil {
			return err
		}
		a.message("✓ Installed the skill for edka %s in %s", a.Version, result.Path)
		if len(result.Removed) > 0 {
			a.message("  Removed %s", strings.Join(result.Removed, ", "))
		}
		if a.output == "json" {
			return ui.Render(a.Out, jsonBody(result), "json", false)
		}
		return nil
	}}
	install.Flags().BoolVar(&project, "project", false, "Install in .claude/skills at the root of this repository")
	install.Flags().StringVar(&dir, "dir", "", "Install in this skills directory, such as ~/.agents/skills")
	install.MarkFlagsMutuallyExclusive("project", "dir")
	markStandalone(install)
	skill.AddCommand(install)
	root.AddCommand(skill)
}

// skillsDir is the skills directory an install writes the skill to: dir, the
// .claude/skills directory of the repository, or the user's.
func skillsDir(project bool, dir string) (string, error) {
	switch {
	case dir != "":
		return filepath.Abs(dir)
	case project:
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(repositoryRoot(cwd), ".claude", "skills"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

// repositoryRoot is the closest directory from start up that holds .git, or
// start outside a repository.
func repositoryRoot(start string) string {
	for dir := start; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// installSkill replaces target with the skill in files and a manifest of
// them. It asks first when target holds anything an earlier install did not
// write, unless that file already matches the skill's.
func (a *App) installSkill(files fs.FS, target string) (*skillInstall, error) {
	skill := map[string][]byte{}
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		skill[name], err = fs.ReadFile(files, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	existing, question, err := inspectSkill(target, skill)
	if err != nil {
		return nil, err
	}
	if question != "" {
		if err := a.confirm(question); err != nil {
			return nil, err
		}
	}
	record := skillRecord{Version: 1, CLI: a.Version, Files: map[string]string{}}
	result := &skillInstall{Path: target, Version: a.Version, Files: []string{}, Removed: []string{}}
	for name, content := range skill {
		record.Files[name] = skillHash(content)
		result.Files = append(result.Files, name)
	}
	manifest, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	skill[skillManifest] = append(manifest, '\n')
	result.Files = append(result.Files, skillManifest)
	if err := replaceDir(target, skill); err != nil {
		return nil, err
	}
	for _, name := range existing {
		if _, kept := skill[name]; !kept {
			result.Removed = append(result.Removed, name)
		}
	}
	slices.Sort(result.Files)
	return result, nil
}

// inspectSkill lists the files in target. When target holds a file that an
// earlier install did not write and that differs from the skill's, or is not a
// directory, it returns the question to ask before the install replaces it.
func inspectSkill(target string, skill map[string][]byte) (existing []string, question string, err error) {
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	switch info.Mode().Type() {
	case fs.ModeDir:
	case fs.ModeSymlink:
		return nil, fmt.Sprintf("Replace the link at %s with the edka skill", target), nil
	default:
		return nil, fmt.Sprintf("Replace %s, which is not a directory, with the edka skill", target), nil
	}
	var foreign []string
	var record skillRecord
	if data, err := os.ReadFile(filepath.Join(target, skillManifest)); err == nil {
		// A manifest that can't be read vouches for no file.
		_ = json.Unmarshal(data, &record)
	}
	err = filepath.WalkDir(target, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(target, file)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if name == skillManifest {
			return nil
		}
		existing = append(existing, name)
		if !entry.Type().IsRegular() {
			foreign = append(foreign, name)
			return nil
		}
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		hash := skillHash(content)
		if hash != record.Files[name] && (skill[name] == nil || hash != skillHash(skill[name])) {
			foreign = append(foreign, name)
		}
		return nil
	})
	if err != nil || len(foreign) == 0 {
		return existing, "", err
	}
	if len(foreign) > 5 {
		foreign = append(foreign[:5], fmt.Sprintf("and %d more", len(foreign)-5))
	}
	return existing, fmt.Sprintf("Replace %s and the files in it that an earlier install did not write (%s)", target, strings.Join(foreign, ", ")), nil
}

// skillHash is the SHA-256 of content with Windows line breaks read as Unix
// ones, so a checkout that converted them keeps the files it wrote its own.
func skillHash(content []byte) string {
	sum := sha256.Sum256(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

// replaceDir puts files at target, in place of whatever target held. It writes
// them to a directory beside target and swaps the two, so a failed write
// leaves target as it was.
func replaceDir(target string, files map[string][]byte) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".edka-skill-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	next, previous := filepath.Join(staging, "next"), filepath.Join(staging, "previous")
	for name, content := range files {
		file := filepath.Join(next, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, content, 0o644); err != nil {
			return err
		}
	}
	moved := false
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, previous); err != nil {
			return err
		}
		moved = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(next, target); err != nil {
		if moved {
			_ = os.Rename(previous, target)
		}
		return err
	}
	return nil
}
