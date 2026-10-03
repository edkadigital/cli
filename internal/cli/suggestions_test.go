package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestSuggestedCommandsExist checks every `edka …` command that help, examples,
// messages and documentation suggest: it must name a visible command that
// accepts its flags.
func TestSuggestedCommandsExist(t *testing.T) {
	files, err := filepath.Glob("../*/*.go")
	if err != nil {
		t.Fatal(err)
	}
	main, _ := filepath.Glob("../../cmd/*/*.go")
	// A command ends at a line break, backtick, pipe, redirect or comment.
	suggestion := regexp.MustCompile("\\bedka ((?:[^\n`&|<>#]|<[^>\\s]+>)+)")
	// Format verbs and <names> stand for values filled in at run time.
	placeholder := regexp.MustCompile(`^(%[a-z]\w*|<[^>]+>)$`)
	checked := 0
	check := func(file, text string) {
		for _, match := range suggestion.FindAllStringSubmatch(text, -1) {
			words := strings.Fields(match[1])
			if len(words) == 0 || placeholder.MatchString(words[0]) {
				continue
			}
			checked++
			if err := checkSuggestion(words, placeholder); err != nil {
				t.Errorf("%s suggests `edka %s`: %v", filepath.Base(file), strings.Join(words, " "), err)
			}
		}
	}
	// Commands in the documentation's code blocks, which people copy.
	docs, _ := filepath.Glob("../../docs/*.md")
	for _, file := range append(docs, "../../README.md") {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, block := range strings.Split(string(data), "```") {
			if i%2 == 1 {
				check(file, block)
			}
		}
	}
	for _, file := range append(files, main...) {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			literal, ok := n.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			check(file, value)
			return true
		})
	}
	if checked < 50 {
		t.Fatalf("found only %d suggested commands", checked)
	}
}

func checkSuggestion(words []string, placeholder *regexp.Regexp) error {
	root := New("test", strings.NewReader(""), io.Discard, io.Discard)
	target, rest, err := root.Find(words)
	if err != nil {
		return err
	}
	if target.Hidden {
		return fmt.Errorf("%s only points to another command", target.CommandPath())
	}
	// Groups parse no flags; a following word must be one of their commands.
	if target.HasSubCommands() {
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") && !placeholder.MatchString(rest[0]) {
			return fmt.Errorf("%s has no command %q", target.CommandPath(), rest[0])
		}
		return nil
	}
	target.InitDefaultHelpFlag()
	if err := target.ParseFlags(rest); err != nil {
		return err
	}
	// An endpoint command's argument is an ID or name. A command word there
	// is a renamed command, such as `deployments scale update` for `deployments scale`.
	if strings.HasPrefix(target.CommandPath(), "edka api ") {
		for _, arg := range target.Flags().Args() {
			switch arg {
			case "create", "update", "replace", "delete", "get", "list":
				return fmt.Errorf("%s takes an ID, not the command word %q", target.CommandPath(), arg)
			}
		}
	}
	return target.ValidateArgs(target.Flags().Args())
}
