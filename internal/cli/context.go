package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/edkadigital/cli/internal/config"
	"github.com/edkadigital/cli/internal/credential"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// chooseLinkDeployment offers the context cluster's deployments, with linking
// only the cluster as the first choice. It returns nil for no deployment.
func (a *App) chooseLinkDeployment(ctx context.Context) (*candidate, error) {
	rows, err := a.deploymentRows(ctx, false)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	const clusterOnly = "-"
	choices := []ui.Choice{{ID: clusterOnly, Name: "Cluster only", Detail: "Link no deployment; name one per command"}}
	for _, row := range rows {
		if id := text(row, "id"); id != "" {
			choices = append(choices, ui.Choice{ID: id, Name: first(text(row, "name"), id), Detail: details(text(row, "status"), sourceOf(row))})
		}
	}
	id, err := ui.Select(ctx, "Link a deployment", choices, a.In, a.Err)
	if err != nil || id == clusterOnly {
		return nil, err
	}
	for _, choice := range choices {
		if choice.ID == id {
			return &candidate{ID: id, Name: choice.Name}, nil
		}
	}
	return nil, fmt.Errorf("selection cancelled")
}

// saveLink links the working directory to a cluster, and to a deployment when
// one is given. It returns the link and a summary to print.
func (a *App) saveLink(ctx context.Context, c, deployment *candidate) (config.Link, string, error) {
	// Obtain the binding from the server rather than trusting local config or JWT contents.
	identity, err := a.request(ctx, "GET", "/api/cli/whoami", nil, nil)
	if err != nil {
		return config.Link{}, "", err
	}
	data, err := identityData(identity.Body)
	if err != nil {
		return config.Link{}, "", err
	}
	organization, ok := data["organization"].(map[string]any)
	if !ok {
		return config.Link{}, "", fmt.Errorf("identity has no organization")
	}
	orgID, _ := organization["id"].(string)
	if orgID == "" {
		return config.Link{}, "", fmt.Errorf("identity has no organization ID")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return config.Link{}, "", err
	}
	path := filepath.Join(cwd, config.LinkName)
	l := config.Link{Version: 1, Profile: a.profile, APIURL: a.current.APIURL, Organization: orgID, Cluster: c.ID}
	summary := "✓ Linked this directory\n  Cluster: " + ui.Clean(c.Name)
	if deployment != nil {
		l.Deployment = deployment.ID
		summary += "\n  Deployment: " + ui.Clean(deployment.Name)
	}
	if err := config.WriteJSON(path, l); err != nil {
		return config.Link{}, "", err
	}
	return l, summary + "\n  Context: " + path, nil
}

func (a *App) addContext(root *cobra.Command) {
	var withoutDeployment bool
	link := &cobra.Command{Use: "link", Short: "Choose a cluster and deployment for this directory", Long: "Save a default cluster, and optionally a deployment, for commands run in this\ndirectory and below it, up to the Git repository root. The file holds IDs only.\n\nWithout --cluster, a picker lists your clusters. In a terminal, a picker then\noffers the cluster's deployments; choose \"Cluster only\" to skip.", GroupID: "start", Args: cobra.NoArgs, Example: "  edka link\n  edka link --cluster production --deployment api\n  edka link --cluster staging --no-deployment", RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.resolveCluster(cmd.Context(), "")
		if err != nil {
			return err
		}
		// Keep an existing link's deployment only while its cluster stays the same.
		target := first(a.deploymentFlag, os.Getenv("EDKA_DEPLOYMENT"))
		if target == "" && a.link != nil && a.link.Cluster == c.ID && a.deployment == a.link.Deployment {
			target = a.deployment
		}
		var deployment *candidate
		switch {
		case withoutDeployment:
		case target != "":
			if deployment, err = a.resolveDeployment(cmd.Context(), target); err != nil {
				return err
			}
		case !a.noInput && ui.IsTerminal(a.Err):
			if deployment, err = a.chooseLinkDeployment(cmd.Context()); err != nil {
				return err
			}
		}
		l, summary, err := a.saveLink(cmd.Context(), c, deployment)
		if err != nil {
			return err
		}
		a.message("%s\n  Next: edka status", summary)
		if a.output == "json" {
			return ui.Render(a.Out, jsonBody(l), "json", false)
		}
		return nil
	}}
	link.Flags().BoolVar(&withoutDeployment, "no-deployment", false, "Link only the cluster")
	unlink := &cobra.Command{Use: "unlink", Short: "Remove this directory's project context", GroupID: "tools", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		path := filepath.Join(cwd, config.LinkName)
		err = os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		removed := err == nil
		if removed {
			a.message("✓ Directory unlinked")
		} else {
			a.message("No project link in this directory.")
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"project_file": path, "removed": removed}), a.output, false)
		}
		return nil
	}}
	contextCmd := &cobra.Command{Use: "context", Short: "Show effective profile and project context", GroupID: "tools", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		return ui.Render(a.Out, jsonBody(map[string]any{"profile": a.profile, "api_url": a.current.APIURL, "organization": a.organization, "cluster": a.cluster, "deployment": a.deployment, "project_file": a.linkPath}), a.output, a.color)
	}}
	profiles := &cobra.Command{Use: "profile", Short: "Manage separate accounts and API environments", GroupID: "tools"}
	a.strictGroup(profiles)
	// row is a profile as `profile list` and `profile use` print it.
	row := func(name string) map[string]any {
		p := a.cfg.Profiles[name]
		return map[string]any{"name": name, "status": map[bool]string{true: "active", false: ""}[name == a.cfg.Active], "api_url": p.APIURL, "organization": p.OrganizationName}
	}
	profiles.AddCommand(&cobra.Command{Use: "list", Short: "List profiles without secrets", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		rows := []any{}
		for _, name := range slices.Sorted(maps.Keys(a.cfg.Profiles)) {
			rows = append(rows, row(name))
		}
		return ui.Render(a.Out, jsonBody(rows), a.output, a.color)
	}}, &cobra.Command{Use: "use <name>", Short: "Set the default profile", Args: cobra.ExactArgs(1), ValidArgsFunction: firstArgument(a.profileNames), RunE: func(_ *cobra.Command, args []string) error {
		if _, exists := a.cfg.Profiles[args[0]]; !exists {
			return fmt.Errorf("profile %q does not exist; run `edka login --profile %s`", args[0], args[0])
		}
		a.cfg.Active = args[0]
		if err := config.Save(a.dir, a.cfg); err != nil {
			return err
		}
		a.message("✓ Default profile: %s", args[0])
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(row(args[0])), a.output, false)
		}
		return nil
	}}, &cobra.Command{Use: "remove <name>", Short: "Remove a signed-out profile", Long: "Remove a profile's saved API address, organization and cluster. Sign out of\nthe profile first, so its credentials don't outlive it.\n\nThe default profile always exists. Removing it resets it to\n" + config.DefaultAPI + ". When you remove the active profile, default becomes active.", Args: cobra.ExactArgs(1), ValidArgsFunction: firstArgument(a.profileNames), Example: "  edka logout --profile staging\n  edka profile remove staging", RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if _, exists := a.cfg.Profiles[name]; !exists {
			return fmt.Errorf("profile %q does not exist; run `edka profile list`", name)
		}
		if err := a.resolveStoreMode(cmd); err != nil {
			return err
		}
		_, err := a.store().Load(name)
		if err == nil {
			return fmt.Errorf("profile %q is signed in; run `edka logout --profile %s` first", name, name)
		}
		if !errors.Is(err, credential.ErrNotFound) {
			return err
		}
		delete(a.cfg.Profiles, name)
		wasActive := a.cfg.Active == name
		if wasActive {
			a.cfg.Active = "default"
		}
		if err := config.Save(a.dir, a.cfg); err != nil {
			return err
		}
		switch {
		case name == "default":
			a.message("✓ Reset profile default to %s", config.DefaultAPI)
		case wasActive:
			a.message("✓ Removed profile %s; default is now active", name)
		default:
			a.message("✓ Removed profile %s", name)
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"name": name, "removed": true, "active": a.cfg.Active}), a.output, false)
		}
		return nil
	}})
	markConfigOnly(profiles.Commands()...)
	markLocal(contextCmd)
	markStandalone(unlink)
	root.AddCommand(link, unlink, contextCmd, profiles)
}
