package cli

import (
	"bytes"
	"context"
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
	"time"

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
	install := &cobra.Command{Use: "install", Short: "Install the agent skill for Claude Code and other agents", Long: "Write the agent skill that matches this version of the CLI to\n`~/.claude/skills/edka`, where Claude Code finds it. The skill shows a coding agent\nhow to check which cluster and deployment a command acts on, diagnose a failure,\npreview a change and ask before a destructive command. Its references list every\ncommand with its flags.\n\n`--project` writes it to `.claude/skills/edka` at the root of the current\nrepository, to commit with the project. `--dir` writes it to another agent's skills\ndirectory. `edka upgrade` rewrites the skill in `~/.claude/skills/edka`; run the\ninstall again after it for the other directories.\n\nAn install replaces the files an earlier one wrote. It asks before it replaces a\nskill directory that holds other files.", Args: cobra.NoArgs, Example: "  edka skill install\n  edka skill install --project\n  edka skill install --dir ~/.agents/skills --json", RunE: func(_ *cobra.Command, _ []string) error {
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

// skillRefresh is what `edka upgrade` did with the skill an earlier `edka skill
// install` wrote to the user's skills directory.
type skillRefresh struct {
	Path      string `json:"path"`
	Refreshed bool   `json:"refreshed"`
	// Reason is why the skill was kept as it was.
	Reason string `json:"reason,omitempty"`
}

// refreshSkill has the release that an upgrade installed at executable write
// its skill over the one an earlier `edka skill install` wrote to
// ~/.claude/skills/edka. This binary embeds the skill of the version the
// release replaced, so it can't write the new one itself. A skill directory
// without a manifest is not one the CLI wrote, and refreshSkill leaves it alone
// and returns nil. It keeps a link, and a skill with files that changed after
// the install, since only the user can say whether to replace them.
func (a *App) refreshSkill(ctx context.Context, executable, version string) *skillRefresh {
	parent, err := skillsDir(false, "")
	if err != nil {
		return nil
	}
	target := filepath.Join(parent, skillName)
	info, err := os.Lstat(target)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(target, skillManifest)); err != nil {
		return nil
	}
	refresh := &skillRefresh{Path: target}
	if !info.IsDir() {
		refresh.Reason = "it is a link"
	} else if _, changed, err := skillChanges(target, nil); err != nil {
		refresh.Reason = "it can't be read: " + err.Error()
	} else if len(changed) > 0 {
		refresh.Reason = fmt.Sprintf("files in it changed after the install (%s)", fileList(changed))
	} else {
		install, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		out, err := runTool(install, nil, executable, "skill", "install", "--dir", parent, "--no-input", "--json")
		var installed skillInstall
		if err == nil {
			err = json.Unmarshal(out, &installed)
		}
		if err != nil {
			refresh.Reason = fmt.Sprintf("edka %s could not write it: %s", version, releaseError(err))
		} else {
			refresh.Path, refresh.Refreshed = installed.Path, true
			a.message("✓ Refreshed the agent skill in %s for edka %s", installed.Path, installed.Version)
			return refresh
		}
	}
	a.message("Kept the agent skill in %s, since %s. Run `edka skill install` to replace it.", target, refresh.Reason)
	return refresh
}

// releaseError is the message of a failure that a release ran with --json
// printed: a JSON object on the last line of its stderr.
func releaseError(err error) string {
	lines := strings.Split(err.Error(), "\n")
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(lines[len(lines)-1]), &failure) == nil && failure.Error != "" {
		return failure.Error
	}
	return err.Error()
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
	existing, foreign, err := skillChanges(target, skill)
	if err != nil || len(foreign) == 0 {
		return existing, "", err
	}
	return existing, fmt.Sprintf("Replace %s and the files in it that an earlier install did not write (%s)", target, fileList(foreign)), nil
}

// skillChanges lists the files in the skill directory target, and those of
// them that an earlier install did not write and that differ from the skill's.
func skillChanges(target string, skill map[string][]byte) (existing, foreign []string, err error) {
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
	return existing, foreign, err
}

// fileList names the first five files, and how many more there are.
func fileList(files []string) string {
	if len(files) > 5 {
		files = append(slices.Clip(files[:5]), fmt.Sprintf("and %d more", len(files)-5))
	}
	return strings.Join(files, ", ")
}

// skillHash is the SHA-256 of content with Windows line breaks read as Unix
// ones, so a checkout that converted them keeps the files it wrote its own.
func skillHash(content []byte) string {
	sum := sha256.Sum256(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

// renameSkill is os.Rename, which tests make fail.
var renameSkill = os.Rename

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
	// A swap that can't put the previous skill back leaves it here, so the
	// staging directory then stays.
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(staging)
		}
	}()
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
		if err := renameSkill(target, previous); err != nil {
			return err
		}
		moved = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := renameSkill(next, target); err != nil {
		if moved {
			if restore := renameSkill(previous, target); restore != nil {
				keep = true
				return fmt.Errorf("%w; the previous skill is in %s", err, previous)
			}
		}
		return err
	}
	return nil
}
