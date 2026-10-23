package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/edkadigital/cli/internal/auth"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// trustActions are what a trust can let a workflow do with its app.
var trustActions = []string{"publish", "update"}

// trustGrants describes a trust's grants in a line, with cluster names.
func trustGrants(m map[string]any, clusterNames map[string]string) string {
	grants, _ := m["grants"].([]any)
	parts := []string{}
	for _, item := range grants {
		grant, _ := item.(map[string]any)
		app := text(grant, "app")
		switch text(grant, "action") {
		case "publish":
			parts = append(parts, "publish "+app)
		case "update":
			clusters, _ := grant["clusters"].([]any)
			if len(clusters) == 0 {
				parts = append(parts, "update "+app)
				continue
			}
			names := []string{}
			for _, id := range clusters {
				s, _ := id.(string)
				names = append(names, first(clusterNames[s], s))
			}
			parts = append(parts, "update "+app+" on "+strings.Join(names, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

// trustRunsIn lists the refs and environments a trust accepts jobs in.
func trustRunsIn(m map[string]any) string {
	list := []string{}
	for _, key := range []string{"refs", "environments"} {
		values, _ := m[key].([]any)
		for _, v := range values {
			if s, ok := v.(string); ok {
				if key == "environments" {
					s = "environment " + s
				}
				list = append(list, s)
			}
		}
	}
	return strings.Join(list, ", ")
}

// clusterNames maps each cluster ID of the organization to its name.
func (a *App) clusterNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	rows, err := a.clusterRows(ctx)
	if err != nil {
		return names
	}
	for _, row := range rows {
		names[text(row, "id")] = text(row, "name")
	}
	return names
}

// resolveTrust selects a trust by ID or name.
func (a *App) resolveTrust(ctx context.Context, target string) (map[string]any, error) {
	rows, err := a.objects(ctx, "/api/trusts")
	if err != nil {
		return nil, err
	}
	matches := []map[string]any{}
	for _, row := range rows {
		if text(row, "id") == target || text(row, "name") == target {
			matches = append(matches, row)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no trust named %s; list them with `edka trusts list`", ui.Clean(target))
	case 1:
		return matches[0], nil
	}
	ids := []string{}
	for _, m := range matches {
		ids = append(ids, text(m, "id"))
	}
	return nil, fmt.Errorf("%d trusts are named %s; pass an ID: %s", len(matches), ui.Clean(target), strings.Join(ids, ", "))
}

// trustJob is the part of a workflow job that signs in with the trust.
func trustJob(m map[string]any) string {
	lines := []string{"    permissions:", "      contents: read", "      id-token: write", "    env:", "      EDKA_TRUST: " + text(m, "id")}
	if audience, _ := auth.Audience(text(m, "audience")); audience != "" && audience != "https://api.edka.io" {
		lines = append(lines, "      EDKA_API_URL: "+audience)
	}
	return strings.Join(lines, "\n")
}

func (a *App) addTrusts(root *cobra.Command) {
	trusts := &cobra.Command{Use: "trusts", Aliases: []string{"trust"}, Short: "Let GitHub Actions workflows sign in without a secret", Long: "A trust lets a GitHub Actions workflow sign in to Edka with the OIDC token\nGitHub gives its job, in place of a stored secret. It names a repository, the\nbranches, tags or environments its jobs run in, and one custom app the\nworkflow may publish and update. Edka refuses everything else.\n\nIn the workflow, give the job `permissions: id-token: write` and set\nEDKA_TRUST to the trust's ID. Each `edka` command in the job then signs in\nwith the trust, and exchanges a new token before the last one expires.\n\nManaging trusts needs the admin role. In an organization with passkey checks,\ncreating or deleting one asks for a check.", GroupID: "resources", Example: "  edka trusts create releases --repository acme/api --ref refs/heads/main --app api\n  edka trusts list\n  edka trusts delete releases"}
	a.strictGroup(trusts)

	list := &cobra.Command{Use: "list", Short: "List the workflows that may sign in", Args: cobra.NoArgs, Example: "  edka trusts list\n  edka trusts list --json", RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		rows, err := a.objects(ctx, "/api/trusts")
		if err != nil {
			return err
		}
		names := map[string]string{}
		if a.output == "table" {
			names = a.clusterNames(ctx)
		}
		return a.renderRows(rows, []ui.Column{ui.Field("NAME", "name"), ui.Field("REPOSITORY", "repository"), {Header: "RUNS IN", Value: trustRunsIn}, {Header: "ALLOWS", Value: func(m map[string]any) string { return trustGrants(m, names) }}, {Header: "LAST USED", Value: func(m map[string]any) string { return first(when(m["last_used_at"]), "never") }}, ui.Field("ID", "id")})
	}}

	get := &cobra.Command{Use: "get <trust>", Short: "Show a trust and the job settings that use it", Args: cobra.ExactArgs(1), Example: "  edka trusts get releases\n  edka trusts get 7d1e4c62-3c4b-4b8e-9b1f-0a4f2c1d9e01 --json", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		m, err := a.resolveTrust(ctx, args[0])
		if err != nil {
			return err
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": m}), a.output, false)
		}
		if err := ui.Fields(a.Out, [][2]string{
			{"Name", text(m, "name")},
			{"Repository", fmt.Sprintf("%s (ID %s, owner ID %s)", text(m, "repository"), text(m, "repository_id"), text(m, "repository_owner_id"))},
			{"Runs in", trustRunsIn(m)},
			{"Workflow", first(text(m, "workflow"), "any")},
			{"Allows", trustGrants(m, a.clusterNames(ctx))},
			{"Audience", text(m, "audience")},
			{"Last used", first(when(m["last_used_at"]), "never")},
			{"Created", when(m["created_at"])},
			{"ID", text(m, "id")},
		}, a.color); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "\nJob settings:\n%s\n", ui.CleanText(trustJob(m)))
		return nil
	}}

	var repository, repositoryID, ownerID, workflow, app string
	var refs, environments, allow, clusters []string
	create := &cobra.Command{Use: "create <name>", Short: "Let a GitHub Actions workflow publish and update one app", Long: "Create a trust: jobs of --repository that run on one of the --ref branches\nor tags, and in one of the --environment environments when you name any, may\nsign in with GitHub's OIDC token. A ref ending in * matches every ref that\nstarts with it, such as refs/tags/v*. --workflow limits the trust to the jobs\nof one workflow file.\n\nThe workflow may publish new versions of the custom app --app names, and\nupdate its installed instances: on every cluster, or on the --clusters you\nlist. --allow keeps one of the two.\n\nEdka reads the repository's ID and owner ID from GitHub, through the GitHub\nintegration or as a public repository, and accepts tokens of that repository\nonly, even after a rename. For a repository Edka can't see, pass\n--repository-id and --owner-id; `gh api repos/<owner>/<name> --jq '.id, .owner.id'`\nprints them.", Args: cobra.ExactArgs(1), Example: "  edka trusts create releases --repository acme/api --ref refs/heads/main --app api\n  edka trusts create tagged --repository acme/api --ref 'refs/tags/v*' --environment production --app api --clusters production\n  edka trusts create publish-only --repository acme/api --ref refs/heads/main --app api --allow publish --workflow .github/workflows/release.yml", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		switch {
		case repository == "":
			return fmt.Errorf("pass --repository, such as acme/api")
		case app == "":
			return fmt.Errorf("pass --app with the slug of the custom app")
		case len(refs) == 0 && len(environments) == 0:
			return fmt.Errorf("pass --ref or --environment with where the workflow's jobs run, such as --ref refs/heads/main")
		case (repositoryID == "") != (ownerID == ""):
			return fmt.Errorf("pass --repository-id and --owner-id together")
		case len(allow) == 0:
			return fmt.Errorf("pass --allow with publish, update or both")
		}
		for _, action := range allow {
			if !slices.Contains(trustActions, action) {
				return fmt.Errorf("unknown action %q; choose publish, update or both", action)
			}
		}
		if len(clusters) > 0 && !slices.Contains(allow, "update") {
			return fmt.Errorf("--clusters limits updates; pass it with --allow update")
		}
		grants := []map[string]any{}
		for _, action := range trustActions {
			if !slices.Contains(allow, action) {
				continue
			}
			grant := map[string]any{"action": action, "app": app}
			if action == "update" {
				ids := []string{}
				for _, name := range clusters {
					c, err := a.resolveCluster(ctx, name)
					if err != nil {
						return err
					}
					ids = append(ids, c.ID)
				}
				if len(ids) > 0 {
					grant["clusters"] = ids
				}
			}
			grants = append(grants, grant)
		}
		body := map[string]any{"name": args[0], "repository": repository, "refs": nonNil(refs), "environments": nonNil(environments), "grants": grants}
		if workflow != "" {
			body["workflow"] = workflow
		}
		if repositoryID != "" {
			body["repository_id"], body["repository_owner_id"] = repositoryID, ownerID
		}
		response, err := a.request(ctx, "POST", "/api/trusts", nil, jsonBody(body))
		if err != nil {
			return err
		}
		if a.output != "table" {
			return a.render(response)
		}
		m, err := identityData(response.Body)
		if err != nil {
			return err
		}
		a.message("✓ Created trust %s for %s\n  Give the workflow's job these settings:\n%s", ui.Clean(text(m, "name")), ui.Clean(text(m, "repository")), ui.CleanText(trustJob(m)))
		return nil
	}}
	create.Flags().StringVar(&repository, "repository", "", "GitHub repository as owner/name")
	create.Flags().StringSliceVar(&refs, "ref", nil, "Branch or tag ref the jobs run on, such as refs/heads/main; repeatable")
	create.Flags().StringSliceVar(&environments, "environment", nil, "GitHub environment the jobs run in; repeatable")
	create.Flags().StringVar(&workflow, "workflow", "", "Workflow file whose jobs may sign in, such as .github/workflows/release.yml")
	create.Flags().StringVar(&app, "app", "", "Slug of the custom app the workflow publishes and updates")
	create.Flags().StringSliceVar(&allow, "allow", trustActions, "What the workflow may do: publish, update")
	create.Flags().StringSliceVar(&clusters, "clusters", nil, "Clusters whose instances the workflow may update; every cluster when left out")
	create.Flags().StringVar(&repositoryID, "repository-id", "", "Numeric ID of a repository Edka can't see")
	create.Flags().StringVar(&ownerID, "owner-id", "", "Numeric ID of the owner of a repository Edka can't see")
	a.completesFlag(create, "allow", values(trustActions...))
	a.completesFlag(create, "clusters", a.offers(a.clusterChoices))

	remove := &cobra.Command{Use: "delete <trust>", Short: "Stop a workflow from signing in", Long: "Delete a trust. Workflows that use it can no longer sign in, and the access\ntokens they hold stop working at once.", Args: cobra.ExactArgs(1), Example: "  edka trusts delete releases\n  edka trusts delete releases --yes", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		m, err := a.resolveTrust(ctx, args[0])
		if err != nil {
			return err
		}
		id, err := safeID(text(m, "id"))
		if err != nil {
			return err
		}
		if err := a.confirm("Delete trust " + text(m, "name") + "; workflows that use it can no longer sign in"); err != nil {
			return err
		}
		response, err := a.request(ctx, "DELETE", "/api/trusts/"+id, nil, nil)
		if err != nil {
			return err
		}
		a.message("✓ Deleted trust %s", ui.Clean(text(m, "name")))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	trusts.AddCommand(list, get, create, remove)
	root.AddCommand(trusts)
}

// nonNil is list, or an empty list in place of nil, for a JSON array.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
