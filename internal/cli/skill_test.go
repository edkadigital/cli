package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The agent skill in skills/edka teaches coding agents to use the CLI. Its
// SKILL.md is written by hand, apart from the section between the generated
// markers; that section and the files in references/ come from the command
// tree. `make skill` rewrites them after a change to a command's help.
var updateSkill = flag.Bool("update-skill", false, "Rewrite the generated parts of skills/edka")

const (
	skillDir            = "../../skills/edka"
	skillGeneratedStart = "<!-- BEGIN GENERATED: make skill -->"
	skillGeneratedEnd   = "<!-- END GENERATED -->"
)

func TestAgentSkillMatchesCommands(t *testing.T) {
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	topics := skillTopics(root)
	want := map[string]string{}
	for _, topic := range topics {
		want[filepath.Join("references", topic.file)] = skillReference(topic, topics)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, rest, ok := strings.Cut(string(data), skillGeneratedStart)
	_, after, ok2 := strings.Cut(rest, skillGeneratedEnd)
	if !ok || !ok2 {
		t.Fatalf("SKILL.md has no %s … %s section", skillGeneratedStart, skillGeneratedEnd)
	}
	want["SKILL.md"] = before + skillGeneratedStart + "\n" + skillIndex(root, topics) + skillGeneratedEnd + after
	old, _ := filepath.Glob(filepath.Join(skillDir, "references", "*.md"))
	if *updateSkill {
		for _, file := range old {
			if _, keep := want[filepath.Join("references", filepath.Base(file))]; !keep {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
		}
		for name, content := range want {
			if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for _, file := range old {
		if _, ok := want[filepath.Join("references", filepath.Base(file))]; !ok {
			t.Errorf("skills/edka/references/%s documents no command group; run make skill", filepath.Base(file))
		}
	}
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(skillDir, name))
		if err != nil || string(got) != content {
			t.Errorf("skills/edka/%s does not match the commands; run make skill", filepath.ToSlash(name))
		}
	}
}

// A skillTopic is one reference file: a command group, or the top-level
// commands of one help group.
type skillTopic struct {
	file, title, summary string
	commands             []*cobra.Command
}

// skillTopics lists the reference files in the order of `edka --help`, with the
// API last. The endpoint commands under `edka api` come from the catalog, so
// the skill points to `edka api operations` instead of listing them.
func skillTopics(root *cobra.Command) []skillTopic {
	shared := map[string]*skillTopic{
		"start": {file: "setup.md", title: "Sign-in and project link", summary: "Sign in, link a directory to a cluster and deployment, and confirm your identity."},
		"work":  {file: "workflows.md", title: "Everyday workflows", summary: "Deploy, build, read logs, diagnose, change variables, scale, restart and roll back a deployment."},
		"tools": {file: "tools.md", title: "Tools", summary: "Context, profiles, health checks, shell completion and upgrades of the CLI."},
	}
	var topics []skillTopic
	var api *cobra.Command
	for _, group := range []string{"start", "work", "resources", "tools"} {
		for _, c := range root.Commands() {
			if c.GroupID != group || !c.IsAvailableCommand() || c.Name() == "help" {
				continue
			}
			if c.Name() == "api" {
				api = c
				continue
			}
			if topic := shared[group]; topic != nil {
				topic.commands = append(topic.commands, skillCommands(c)...)
				continue
			}
			topics = append(topics, skillTopic{file: c.Name() + ".md", title: "`" + c.CommandPath() + "`", summary: c.Short + ".", commands: skillCommands(c)})
		}
		if topic := shared[group]; topic != nil {
			topics = append(topics, *topic)
		}
	}
	apiTopic := skillTopic{file: "api.md", title: "`edka api`", summary: api.Short + ".", commands: []*cobra.Command{api}}
	for _, c := range api.Commands() {
		if c.GroupID != "endpoints" && c.IsAvailableCommand() {
			apiTopic.commands = append(apiTopic.commands, skillCommands(c)...)
		}
	}
	return append(topics, apiTopic)
}

// skillCommands returns c and the commands below it that run something; a
// group that only prints its help is left out, its commands are not.
func skillCommands(c *cobra.Command) []*cobra.Command {
	var commands []*cobra.Command
	if c.Runnable() && !(c.DisableFlagParsing && c.HasAvailableSubCommands()) {
		commands = append(commands, c)
	}
	for _, child := range c.Commands() {
		if child.IsAvailableCommand() && child.Name() != "help" {
			commands = append(commands, skillCommands(child)...)
		}
	}
	return commands
}

// skillUsage is the usage line without the [flags] that every command has.
func skillUsage(c *cobra.Command) string {
	return strings.TrimSuffix(c.UseLine(), " [flags]")
}

// skillSignature identifies the help of a command that appears under two
// names, such as `edka logs` and `edka deployments logs`.
func skillSignature(c *cobra.Command) string {
	return c.Short + "\x00" + c.Long + "\x00" + strings.Join(skillFlags(c), "\n")
}

func skillIndex(root *cobra.Command, topics []skillTopic) string {
	var b strings.Builder
	b.WriteString("## Command reference\n\nEach reference file has the flags and examples of its commands. `edka <command> --help` prints the same help.\n")
	for _, topic := range topics {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n\n", topic.title, topic.summary)
		for _, c := range topic.commands {
			fmt.Fprintf(&b, "- `%s`: %s\n", skillUsage(c), c.Short)
		}
		fmt.Fprintf(&b, "\nFlags and examples: [references/%[1]s](references/%[1]s)\n", topic.file)
	}
	b.WriteString("\n## Global flags\n\nEvery command takes these flags. A variable in parentheses sets the same value.\n\n")
	b.WriteString(strings.Join(skillFlagList(root.PersistentFlags()), "\n"))
	b.WriteString("\n")
	return b.String()
}

func skillReference(topic skillTopic, topics []skillTopic) string {
	first := map[string]string{}
	for _, t := range topics {
		for _, c := range t.commands {
			if _, seen := first[skillSignature(c)]; !seen {
				first[skillSignature(c)] = c.CommandPath()
			}
		}
	}
	// Commands made in a loop, such as `edka api get` and `edka api post`,
	// share their examples; a file shows each set once.
	examples := map[string]bool{}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n", topic.title, topic.summary)
	for _, c := range topic.commands {
		fmt.Fprintf(&b, "\n## `%s`\n\n", skillUsage(c))
		if same := first[skillSignature(c)]; same != c.CommandPath() {
			fmt.Fprintf(&b, "Same as `%s`.\n", same)
			continue
		}
		// A long description that starts with the short one says it once.
		text, short := strings.TrimSpace(c.Long), strings.TrimSuffix(c.Short, ".")
		if !strings.HasPrefix(text, short) {
			text = strings.TrimSpace(short + ".\n\n" + text)
		}
		fmt.Fprintf(&b, "%s\n", text)
		if flags := skillFlags(c); len(flags) > 0 {
			fmt.Fprintf(&b, "\nFlags:\n\n%s\n", strings.Join(flags, "\n"))
		}
		if example := skillExample(c.Example); example != "" && !examples[example] {
			examples[example] = true
			fmt.Fprintf(&b, "\nExamples:\n\n```bash\n%s\n```\n", example)
		}
	}
	b.WriteString("\nEvery command also takes the global flags in SKILL.md.\n")
	return b.String()
}

func skillFlags(c *cobra.Command) []string {
	return skillFlagList(c.NonInheritedFlags())
}

func skillFlagList(flags *pflag.FlagSet) []string {
	var lines []string
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		name, usage := pflag.UnquoteUsage(f)
		spec := "--" + f.Name
		if f.Shorthand != "" {
			spec = "-" + f.Shorthand + ", " + spec
		}
		if name != "" {
			spec += " " + name
		}
		line := fmt.Sprintf("- `%s`: %s", spec, usage)
		switch {
		case slices.Contains([]string{"", "false", "0", "0s", "[]"}, f.DefValue):
		case f.Value.Type() == "string":
			line += fmt.Sprintf(" (default %q)", f.DefValue)
		default:
			line += fmt.Sprintf(" (default %s)", f.DefValue)
		}
		lines = append(lines, line)
	})
	return lines
}

// skillExample removes the indent Cobra prints examples with.
func skillExample(example string) string {
	lines := strings.Split(strings.Trim(example, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, "  ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
