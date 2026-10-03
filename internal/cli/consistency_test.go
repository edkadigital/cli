package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// A short flag means one thing wherever it appears, so -n is not the tail of
// one command and the namespace of another.
func TestAShortFlagMeansOneThing(t *testing.T) {
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	meaning := map[string]string{}
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Shorthand == "" {
				return
			}
			if name, taken := meaning[flag.Shorthand]; taken && name != flag.Name {
				t.Errorf("-%s is --%s on `%s` and --%s elsewhere", flag.Shorthand, flag.Name, cmd.CommandPath(), name)
			}
			meaning[flag.Shorthand] = flag.Name
		})
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
	if meaning["n"] != "tail" || meaning["y"] != "yes" {
		t.Fatal(meaning)
	}
}

// `edka --help` lists each command with its description on one line. A long
// description wraps in a terminal of 100 columns.
func TestCommandDescriptionsFitOneLine(t *testing.T) {
	root := New("test", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	for _, cmd := range root.Commands() {
		if cmd.IsAvailableCommand() && len(cmd.Short) > 60 {
			t.Errorf("%s: %d characters: %s", cmd.Name(), len(cmd.Short), cmd.Short)
		}
	}
}
