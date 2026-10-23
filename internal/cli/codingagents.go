package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// A codingAgent is a coding agent that reads skills from a directory. The
// directories follow the agent table of vercel-labs/skills, which `npx skills
// add` installs by.
type codingAgent struct {
	// name is what --agent takes, the same name `npx skills add --agent` takes.
	name, title string
	// config is the directory the agent keeps its settings in. When it exists,
	// the agent is installed.
	config func(home string) string
	// global is the user's skills directory of the agent.
	global func(home string) string
	// project is the skills directory of the agent in a repository.
	project string
}

var codingAgents = []codingAgent{
	{name: "claude-code", title: "Claude Code", config: claudeHome, global: under(claudeHome, "skills"), project: ".claude/skills"},
	{name: "codex", title: "Codex", config: envHome("CODEX_HOME", ".codex"), global: sharedSkills, project: ".agents/skills"},
	{name: "cursor", title: "Cursor", config: homeDir(".cursor"), global: homeDir(".cursor/skills"), project: ".agents/skills"},
	{name: "gemini-cli", title: "Gemini CLI", config: homeDir(".gemini"), global: homeDir(".gemini/skills"), project: ".agents/skills"},
	{name: "github-copilot", title: "GitHub Copilot", config: homeDir(".copilot"), global: homeDir(".copilot/skills"), project: ".agents/skills"},
	{name: "opencode", title: "OpenCode", config: configDir("opencode"), global: configDir("opencode/skills"), project: ".agents/skills"},
	{name: "windsurf", title: "Windsurf", config: homeDir(".codeium/windsurf"), global: homeDir(".codeium/windsurf/skills"), project: ".windsurf/skills"},
	{name: "amp", title: "Amp", config: configDir("amp"), global: configDir("agents/skills"), project: ".agents/skills"},
	{name: "goose", title: "Goose", config: configDir("goose"), global: configDir("goose/skills"), project: ".goose/skills"},
	{name: "droid", title: "Droid", config: homeDir(".factory"), global: homeDir(".factory/skills"), project: ".agents/skills"},
	{name: "kiro-cli", title: "Kiro CLI", config: homeDir(".kiro"), global: homeDir(".kiro/skills"), project: ".kiro/skills"},
	{name: "cline", title: "Cline", config: homeDir(".cline"), global: sharedSkills, project: ".agents/skills"},
	{name: "roo", title: "Roo Code", config: homeDir(".roo"), global: homeDir(".roo/skills"), project: ".roo/skills"},
	{name: "junie", title: "Junie", config: homeDir(".junie"), global: homeDir(".junie/skills"), project: ".junie/skills"},
	{name: "qwen-code", title: "Qwen Code", config: homeDir(".qwen"), global: homeDir(".qwen/skills"), project: ".qwen/skills"},
	{name: "hermes-agent", title: "Hermes Agent", config: envHome("HERMES_HOME", ".hermes"), global: under(envHome("HERMES_HOME", ".hermes"), "skills"), project: ".hermes/skills"},
}

// fallbackAgents are the agents an install writes for when it finds none:
// Claude Code, and through ~/.agents/skills the agents that share it.
var fallbackAgents = []string{"claude-code", "codex"}

// codingAgentNames are the names --agent takes.
func codingAgentNames() []string {
	names := make([]string, len(codingAgents))
	for i, agent := range codingAgents {
		names[i] = agent.name
	}
	return names
}

func codingAgentNamed(name string) (codingAgent, bool) {
	for _, agent := range codingAgents {
		if agent.name == name {
			return agent, true
		}
	}
	return codingAgent{}, false
}

// detectedAgents are the agents whose settings directory exists in home.
func detectedAgents(home string) []codingAgent {
	var found []codingAgent
	for _, agent := range codingAgents {
		if info, err := os.Stat(agent.config(home)); err == nil && info.IsDir() {
			found = append(found, agent)
		}
	}
	return found
}

// claudeHome is $CLAUDE_CONFIG_DIR, or ~/.claude.
var claudeHome = envHome("CLAUDE_CONFIG_DIR", ".claude")

// sharedSkills is ~/.agents/skills, which several agents read.
var sharedSkills = homeDir(".agents/skills")

// envHome is the directory in the variable name, or dir in home.
func envHome(name, dir string) func(string) string {
	return func(home string) string {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
		return filepath.Join(home, filepath.FromSlash(dir))
	}
}

func homeDir(dir string) func(string) string {
	return func(home string) string { return filepath.Join(home, filepath.FromSlash(dir)) }
}

// configDir is dir in $XDG_CONFIG_HOME, or in ~/.config on every system, as
// OpenCode, Amp and Goose read it.
func configDir(dir string) func(string) string {
	return func(home string) string {
		base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, filepath.FromSlash(dir))
	}
}

// under is dir inside the directory base returns.
func under(base func(string) string, dir string) func(string) string {
	return func(home string) string { return filepath.Join(base(home), dir) }
}
