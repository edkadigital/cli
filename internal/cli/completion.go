package cli

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// completionTimeout bounds the reads of one completion, because the shell
// waits for them.
var completionTimeout = 3 * time.Second

// lister reads the resources a completion offers.
type lister func(context.Context) ([]candidate, error)

// offers completes a value from what list returns: each name, with its detail
// as the description. A completion skips initialize, so it loads the context
// itself. It never prompts, and offers nothing when a read fails.
func (a *App) offers(list lister) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		a.noInput, a.polling = true, true
		ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
		defer cancel()
		err := a.loadConfig()
		if err == nil {
			err = a.loadLink()
		}
		if err == nil {
			err = a.resolveContext(cmd)
		}
		var candidates []candidate
		if err == nil {
			candidates, err = list(ctx)
		}
		if err != nil {
			cobra.CompDebugln(err.Error(), false)
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		completions := []cobra.Completion{}
		offered := map[string]bool{}
		for _, c := range candidates {
			// The shell reads one completion per line, with a tab before the
			// description. A name with a control character has its ID offered.
			name := c.Name
			if name == "" || ui.Clean(name) != name {
				name = c.ID
			}
			if name == "" || ui.Clean(name) != name || offered[name] || !strings.HasPrefix(name, toComplete) {
				continue
			}
			offered[name] = true
			if detail := ui.Clean(c.Detail); detail != "" {
				name = cobra.CompletionWithDesc(name, detail)
			}
			completions = append(completions, name)
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// firstArgument completes a command's first argument with fn, and offers
// nothing for the arguments after it.
func firstArgument(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fn(cmd, args, toComplete)
	}
}

// completes offers what list returns for the first argument of each command.
func (a *App) completes(list lister, commands ...*cobra.Command) {
	for _, c := range commands {
		c.ValidArgsFunction = firstArgument(a.offers(list))
	}
}

// completesFlag completes a flag's value with fn. Cobra keeps a flag's
// completion until the process exits, and the function holds the whole command
// tree, so flags register only when the shell asks for a completion.
func (a *App) completesFlag(cmd *cobra.Command, flag string, fn cobra.CompletionFunc) {
	a.flagCompletions = append(a.flagCompletions, func() { _ = cmd.RegisterFlagCompletionFunc(flag, fn) })
}

// values completes a flag that takes one of a fixed list of values.
func values(list ...string) cobra.CompletionFunc {
	return cobra.FixedCompletions(list, cobra.ShellCompDirectiveNoFileComp)
}

// directories has the shell complete an argument with directory names.
func directories(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// files has the shell complete an argument with file names.
func files(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveDefault
}

// fileFlags take a path, so the shell completes their values with file names.
var fileFlags = []string{"data", "dir", "output-file"}

// registerCompletions runs when the shell asks for a completion. It registers
// the flag completions. Every other argument and flag value, except a
// fileFlags one, then offers nothing, where Cobra would offer file names.
func (a *App) registerCompletions(root *cobra.Command) {
	for _, register := range a.flagCompletions {
		register()
	}
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if cmd.ValidArgsFunction == nil && len(cmd.ValidArgs) == 0 && !cmd.HasSubCommands() {
			cmd.ValidArgsFunction = cobra.NoFileCompletions
		}
		cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Value.Type() == "bool" || slices.Contains(fileFlags, flag.Name) {
				return
			}
			if _, registered := cmd.GetFlagCompletionFunc(flag.Name); !registered {
				_ = cmd.RegisterFlagCompletionFunc(flag.Name, cobra.NoFileCompletions)
			}
		})
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}

// profileNames completes a saved profile, with its organization. It reads the
// config and nothing else, so it works while the profile in context is missing.
func (a *App) profileNames(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	completions := []cobra.Completion{}
	if a.loadConfig() != nil {
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
	for _, name := range slices.Sorted(maps.Keys(a.cfg.Profiles)) {
		if !strings.HasPrefix(name, toComplete) {
			continue
		}
		if organization := ui.Clean(a.cfg.Profiles[name].OrganizationName); organization != "" {
			name = cobra.CompletionWithDesc(name, organization)
		}
		completions = append(completions, name)
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

// commandNames completes the commands `edka help` explains.
func commandNames(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	completions := []cobra.Completion{}
	target, _, err := cmd.Root().Find(args)
	if err != nil {
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
	for _, child := range target.Commands() {
		if child.IsAvailableCommand() && strings.HasPrefix(child.Name(), toComplete) {
			completions = append(completions, cobra.CompletionWithDesc(child.Name(), child.Short))
		}
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

func (a *App) clusterChoices(ctx context.Context) ([]candidate, error) {
	rows, err := a.clusterRows(ctx)
	return clusterCandidates(rows), err
}

// deploymentChoices lists the deployments of the context cluster, or of every
// cluster when there is none.
func (a *App) deploymentChoices(ctx context.Context) ([]candidate, error) {
	rows, err := a.deploymentRows(ctx, false)
	return deploymentCandidates(rows, false), err
}

// gitDeploymentChoices is deploymentChoices without the deployments that have
// no GitHub repository.
func (a *App) gitDeploymentChoices(ctx context.Context) ([]candidate, error) {
	rows, err := a.deploymentRows(ctx, false)
	return deploymentCandidates(rows, true), err
}

// scopedChoices lists the records that resolveScoped selects from.
func (a *App) scopedChoices(suffix string, names func(map[string]any) []string, detail func(map[string]any) string) lister {
	return func(ctx context.Context) ([]candidate, error) {
		rows, err := a.clusterScopedRows(ctx, false, suffix)
		return scopedCandidates(rows, names, detail), err
	}
}

// catalogChoices lists the entries of the catalog at path by their key field,
// each with its detail field.
func (a *App) catalogChoices(path, key, detail string) lister {
	return func(ctx context.Context) ([]candidate, error) {
		rows, err := a.objects(ctx, path)
		candidates := []candidate{}
		for _, row := range rows {
			candidates = append(candidates, candidate{Name: text(row, key), Detail: text(row, detail)})
		}
		return candidates, err
	}
}

// catalogAppChoices lists the apps to install by slug: the catalog of the
// context cluster, or the one `edka apps catalog` lists when there is none.
func (a *App) catalogAppChoices(ctx context.Context) ([]candidate, error) {
	path := "/api/apps/catalog"
	if a.cluster != "" {
		cluster, err := a.clusterID(ctx)
		if err != nil {
			return nil, err
		}
		path = "/api/clusters/" + cluster + "/apps/catalog"
	}
	return a.catalogChoices(path, "slug", "name")(ctx)
}
