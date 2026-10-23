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

// skillInstall is what an install wrote.
type skillInstall struct {
	Version     string         `json:"version"`
	Directories []installedDir `json:"directories"`
}

// installedDir is what an install wrote to one skills directory, and the
// agents that read it.
type installedDir struct {
	Path   string           `json:"path"`
	Agents []string         `json:"agents"`
	Skills []installedSkill `json:"skills"`
}

// installedSkill is what an install wrote to the directory of one skill and
// removed from it.
type installedSkill struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Files   []string `json:"files"`
	Removed []string `json:"removed"`
}

func (a *App) addSkill(root *cobra.Command) {
	skill := &cobra.Command{Use: "skill", Short: "Install the agent skills for coding agents", GroupID: "tools"}
	a.strictGroup(skill)
	var project bool
	var dir string
	var agents []string
	install := &cobra.Command{Use: "install [skill...]", Short: "Install the agent skills for Claude Code and other agents", Long: "Write the agent skills that match this version of the CLI for each coding agent\ninstalled here, to the skills directory it reads, each skill in a directory of its\nname:\n\n- `edka` shows a coding agent how to check which cluster and deployment a command\n  acts on, diagnose a failure, preview a change and ask before a destructive\n  command. Its references list every command with its flags.\n- `edka-app` shows it how to package an application as an Edka app, validate and\n  publish it, and install it on a cluster.\n\nAn agent is installed when its settings directory exists, such as `~/.claude` or\n`~/.cursor`. `--agent` names the agents to install for instead, found or not:\nclaude-code, codex, cursor, gemini-cli, github-copilot, opencode, windsurf, amp,\ngoose, droid, kiro-cli, cline, roo, junie, qwen-code and hermes-agent. With no\nagent found, the install is for Claude Code and Codex. Claude Code reads\n`$CLAUDE_CONFIG_DIR/skills` when the variable is set, and Codex, Cline and other\nagents share `~/.agents/skills`.\n\nName skills to install only those. `--project` writes them to the agents' skills\ndirectories at the root of the current repository, such as `.claude/skills` and\n`.agents/skills`, to commit with the project. `--dir` writes them to one directory\ninstead. `edka upgrade` rewrites the skills in the agents' directories in your home;\nrun the install again after it for the other directories.\n\nAn install replaces the files an earlier one wrote. It asks before it replaces a\nskill directory that holds other files.", Args: skillArgs, ValidArgs: skills.Names, Example: "  edka skill install\n  edka skill install edka-app\n  edka skill install --agent claude-code,cursor\n  edka skill install --project\n  edka skill install --dir ./skills --json", RunE: func(_ *cobra.Command, args []string) error {
		targets, fallback, err := skillTargets(project, dir, agents)
		if err != nil {
			return err
		}
		if fallback {
			a.message("Found no coding agent, so the install is for Claude Code and Codex.")
		}
		names := skills.Names
		if len(args) > 0 {
			names = args
		}
		result, err := a.installSkills(targets, names)
		for _, installed := range result.Directories {
			var written, titles []string
			for _, skill := range installed.Skills {
				written = append(written, skill.Name)
			}
			for _, name := range installed.Agents {
				agent, _ := codingAgentNamed(name)
				titles = append(titles, agent.title)
			}
			readers := ""
			if len(titles) > 0 {
				readers = " for " + andList(titles)
			}
			a.message("✓ Installed the %s of edka %s in %s%s", skillList(written), a.Version, installed.Path, readers)
			for _, skill := range installed.Skills {
				if len(skill.Removed) > 0 {
					a.message("  Removed %s", strings.Join(prefixed(skill.Name, skill.Removed), ", "))
				}
			}
		}
		if err != nil {
			return err
		}
		if a.output == "json" {
			return ui.Render(a.Out, jsonBody(result), "json", false)
		}
		return nil
	}}
	install.Flags().StringSliceVar(&agents, "agent", nil, "Install for these agents, found or not; repeatable")
	install.Flags().BoolVar(&project, "project", false, "Install in the agents' skills directories at the root of this repository")
	install.Flags().StringVar(&dir, "dir", "", "Install in this skills directory alone")
	install.MarkFlagsMutuallyExclusive("project", "dir")
	install.MarkFlagsMutuallyExclusive("agent", "dir")
	a.completesFlag(install, "agent", values(codingAgentNames()...))
	markStandalone(install)
	skill.AddCommand(install)
	root.AddCommand(skill)
}

// skillArgs accepts the names of the skills this binary holds, each once.
func skillArgs(_ *cobra.Command, args []string) error {
	for i, name := range args {
		if !slices.Contains(skills.Names, name) {
			return fmt.Errorf("unknown skill %q; the skills are %s", name, strings.Join(skills.Names, ", "))
		}
		if slices.Contains(args[:i], name) {
			return fmt.Errorf("skill %q is named twice", name)
		}
	}
	return nil
}

// andList is "a", "a and b", or "a, b and c".
func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// skillList is "edka skill" or "edka and edka-app skills".
func skillList(names []string) string {
	if len(names) == 1 {
		return names[0] + " skill"
	}
	return andList(names) + " skills"
}

// prefixed puts dir/ before each file.
func prefixed(dir string, files []string) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = dir + "/" + file
	}
	return paths
}

// skillTarget is a skills directory an install writes to, and the agents that
// read it.
type skillTarget struct {
	dir    string
	agents []string
}

// skillTargets are the skills directories an install writes to: dir alone, or
// the directories the named agents read, or the agents installed here, in the
// user's home or at the root of the repository. Agents that share a directory
// share a target. fallback reports that no agent was found, and the targets
// are those of fallbackAgents.
func skillTargets(project bool, dir string, names []string) (targets []skillTarget, fallback bool, err error) {
	if dir != "" {
		path, err := filepath.Abs(dir)
		return []skillTarget{{dir: path}}, false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, err
	}
	var agents []codingAgent
	for _, name := range names {
		agent, ok := codingAgentNamed(name)
		if !ok {
			return nil, false, fmt.Errorf("unknown agent %q; the agents are %s", name, strings.Join(codingAgentNames(), ", "))
		}
		agents = append(agents, agent)
	}
	if len(names) == 0 {
		agents = detectedAgents(home)
	}
	if len(agents) == 0 {
		fallback = true
		for _, name := range fallbackAgents {
			agent, _ := codingAgentNamed(name)
			agents = append(agents, agent)
		}
	}
	root := ""
	if project {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, false, err
		}
		root = repositoryRoot(cwd)
	}
	for _, agent := range agents {
		path := agent.global(home)
		if project {
			path = filepath.Join(root, filepath.FromSlash(agent.project))
		}
		i := slices.IndexFunc(targets, func(target skillTarget) bool { return target.dir == path })
		if i < 0 {
			targets = append(targets, skillTarget{dir: path})
			i = len(targets) - 1
		}
		if !slices.Contains(targets[i].agents, agent.name) {
			targets[i].agents = append(targets[i].agents, agent.name)
		}
	}
	return targets, fallback, nil
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

// skillFiles reads the files of the skill name from the binary, by their
// slash paths in the skill.
func skillFiles(name string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(skills.FS, name, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		files[strings.TrimPrefix(file, name+"/")], err = fs.ReadFile(skills.FS, file)
		return err
	})
	return files, err
}

// installSkills replaces the directory of each named skill in each target
// with the skill and a manifest of its files. It asks first when a directory
// holds anything an earlier install did not write, unless that file already
// matches the skill's, and asks every question before it writes a skill. The
// result lists the skills it wrote, also when a later one failed.
func (a *App) installSkills(targets []skillTarget, names []string) (*skillInstall, error) {
	result := &skillInstall{Version: a.Version, Directories: []installedDir{}}
	type pending struct {
		name, target string
		files        map[string][]byte
		existing     []string
	}
	writes := make([][]pending, len(targets))
	for i, target := range targets {
		for _, name := range names {
			files, err := skillFiles(name)
			if err != nil {
				return result, err
			}
			path := filepath.Join(target.dir, name)
			existing, question, err := inspectSkill(name, path, files)
			if err != nil {
				return result, err
			}
			if question != "" {
				if err := a.confirm(question); err != nil {
					return result, err
				}
			}
			writes[i] = append(writes[i], pending{name, path, files, existing})
		}
	}
	for i, target := range targets {
		installed := installedDir{Path: target.dir, Agents: append([]string{}, target.agents...), Skills: []installedSkill{}}
		for _, w := range writes[i] {
			skill, err := a.writeSkill(w.name, w.target, w.files, w.existing)
			if err != nil {
				if len(installed.Skills) > 0 {
					result.Directories = append(result.Directories, installed)
				}
				return result, fmt.Errorf("write the %s skill: %w", w.name, err)
			}
			installed.Skills = append(installed.Skills, *skill)
		}
		result.Directories = append(result.Directories, installed)
	}
	return result, nil
}

// writeSkill puts files and a manifest of them, which it adds to files, at
// target in place of existing.
func (a *App) writeSkill(name, target string, files map[string][]byte, existing []string) (*installedSkill, error) {
	record := skillRecord{Version: 1, CLI: a.Version, Files: map[string]string{}}
	installed := &installedSkill{Name: name, Path: target, Files: []string{}, Removed: []string{}}
	for file, content := range files {
		record.Files[file] = skillHash(content)
		installed.Files = append(installed.Files, file)
	}
	manifest, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	files[skillManifest] = append(manifest, '\n')
	installed.Files = append(installed.Files, skillManifest)
	if err := replaceDir(target, files); err != nil {
		return nil, err
	}
	for _, file := range existing {
		if _, kept := files[file]; !kept {
			installed.Removed = append(installed.Removed, file)
		}
	}
	slices.Sort(installed.Files)
	return installed, nil
}

// skillRefresh is what `edka upgrade` did with a skill an earlier `edka skill
// install` wrote to the user's skills directory.
type skillRefresh struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Refreshed bool   `json:"refreshed"`
	// Reason is why the skill was kept as it was.
	Reason string `json:"reason,omitempty"`
}

// refreshSkills has the release that an upgrade installed at executable write
// its skills over those an earlier `edka skill install` wrote to the skills
// directories of the coding agents in the user's home. This binary embeds the
// skills of the version the release replaced, so it can't write the new ones
// itself. A skill directory without a manifest is not one the CLI wrote, and
// refreshSkills leaves it alone; nil means there was no skill the CLI wrote.
func (a *App) refreshSkills(ctx context.Context, executable, version string) []skillRefresh {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, agent := range codingAgents {
		if dir := agent.global(home); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	var refreshes []skillRefresh
	for _, dir := range dirs {
		refreshes = append(refreshes, a.refreshSkillsIn(ctx, executable, version, dir)...)
	}
	return refreshes
}

// refreshSkillsIn has the release rewrite the skills in the skills directory
// parent. It keeps a link, and a skill with files that changed after the
// install, since only the user can say whether to replace them. A skill the
// user did not install stays uninstalled.
func (a *App) refreshSkillsIn(ctx context.Context, executable, version, parent string) []skillRefresh {
	var refreshes []skillRefresh
	var names []string
	for _, name := range skills.Names {
		target := filepath.Join(parent, name)
		info, err := os.Lstat(target)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(target, skillManifest)); err != nil {
			continue
		}
		refresh := skillRefresh{Name: name, Path: target}
		if !info.IsDir() {
			refresh.Reason = "it is a link"
		} else if _, changed, err := skillChanges(target, nil); err != nil {
			refresh.Reason = "it can't be read: " + err.Error()
		} else if len(changed) > 0 {
			refresh.Reason = fmt.Sprintf("files in it changed after the install (%s)", fileList(changed))
		} else {
			names = append(names, name)
		}
		refreshes = append(refreshes, refresh)
	}
	if len(names) > 0 {
		install, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		out, err := runTool(install, nil, executable, append([]string{"skill", "install", "--dir", parent, "--no-input", "--json"}, names...)...)
		var installed skillInstall
		if err == nil {
			err = json.Unmarshal(out, &installed)
		}
		var written []installedSkill
		for _, dir := range installed.Directories {
			written = append(written, dir.Skills...)
		}
		var refreshed []string
		for i := range refreshes {
			refresh := &refreshes[i]
			if !slices.Contains(names, refresh.Name) {
				continue
			}
			if err != nil {
				refresh.Reason = fmt.Sprintf("edka %s could not write it: %s", version, releaseError(err))
				continue
			}
			for _, skill := range written {
				if skill.Name == refresh.Name {
					refresh.Path, refresh.Refreshed = skill.Path, true
				}
			}
			if !refresh.Refreshed {
				refresh.Reason = fmt.Sprintf("edka %s did not write it", version)
				continue
			}
			refreshed = append(refreshed, refresh.Name)
		}
		if len(refreshed) > 0 {
			a.message("✓ Refreshed the %s in %s for edka %s", skillList(refreshed), parent, installed.Version)
		}
	}
	for _, refresh := range refreshes {
		if !refresh.Refreshed {
			a.message("Kept the %s skill in %s, since %s. Run `edka skill install` to replace it.", refresh.Name, refresh.Path, refresh.Reason)
		}
	}
	return refreshes
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

// inspectSkill lists the files in target, the directory of the skill name.
// When target holds a file that an earlier install did not write and that
// differs from the skill's, or is not a directory, it returns the question to
// ask before the install replaces it.
func inspectSkill(name, target string, skill map[string][]byte) (existing []string, question string, err error) {
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
		return nil, fmt.Sprintf("Replace the link at %s with the %s skill", target, name), nil
	default:
		return nil, fmt.Sprintf("Replace %s, which is not a directory, with the %s skill", target, name), nil
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
