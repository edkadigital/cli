package cli

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// appName is the name the console shows for an installed app.
func appName(m map[string]any) string {
	if name := text(m, "display_name"); name != "" {
		return name
	}
	if name := text(m, "instance_name"); name != "" && name != "default" {
		return name
	}
	return text(m, "app_name")
}

var appColumns = []ui.Column{
	{Header: "NAME", Value: appName},
	ui.Field("APP", "app_name"),
	ui.Field("VERSION", "version"),
	ui.Field("STATUS", "status"),
	ui.Field("CLUSTER", "cluster_name"),
	ui.Field("NAMESPACE", "namespace"),
}

// appNames are the names that select an installed app. Edka stores "default"
// for an instance without a name, which selects none.
func appNames(m map[string]any) []string {
	names := []string{appName(m), text(m, "instance_name"), text(m, "instance_slug"), text(m, "release_name")}
	return slices.DeleteFunc(names, func(name string) bool { return name == "default" })
}
func appDetail(m map[string]any) string {
	return strings.Trim(text(m, "app_name")+" "+text(m, "version")+" · "+text(m, "cluster_name"), " ·")
}

// appRows lists installed apps. The organization-wide route omits names,
// namespaces and versions, so each cluster's list is read instead.
func (a *App) appRows(ctx context.Context, all bool) ([]map[string]any, error) {
	return a.clusterScopedRows(ctx, all, "apps/instances")
}

// appChoices lists installed apps for a completion.
func (a *App) appChoices() lister { return a.scopedChoices("apps/instances", appNames, appDetail) }

// resolveApp selects an installed app by ID, name, instance or release name.
func (a *App) resolveApp(ctx context.Context, target string) (*candidate, error) {
	return a.resolveScoped(ctx, "app", "apps/instances", target, "no apps installed; browse them with `edka apps catalog`", appNames, appDetail)
}

// showApp prints an installed app's summary.
func (a *App) showApp(m map[string]any, cluster, id string) error {
	message := ""
	if progress, ok := m["progress"].(map[string]any); ok && text(m, "status") != "installed" {
		message = text(progress, "message")
	}
	return ui.Fields(a.Out, [][2]string{
		{"Name", appName(m)},
		{"App", text(m, "app_name")},
		{"Version", text(m, "version")},
		{"Status", text(m, "status")},
		{"Message", message},
		{"Cluster", cluster},
		{"Namespace", text(m, "namespace")},
		{"Release", text(m, "release_name")},
		{"Created", when(m["created_at"])},
		{"Updated", when(m["updated_at"])},
		{"ID", id},
	}, a.color)
}

// renderRows prints records as a curated table, or in the API's data envelope.
func (a *App) renderRows(rows []map[string]any, columns []ui.Column) error {
	if a.output != "table" {
		return ui.Render(a.Out, jsonBody(map[string]any{"data": rows}), a.output, false)
	}
	return ui.Table(a.Out, columns, rows, a.color)
}

func (a *App) addApps(root *cobra.Command) {
	apps := &cobra.Command{Use: "apps", Aliases: []string{"app"}, Short: "Install and manage apps, and publish and share your own", GroupID: "resources", Example: "  edka apps list\n  edka apps catalog\n  edka apps install excalidraw --wait\n  edka apps get strapi\n  edka apps logs strapi --follow\n  edka apps validate ./memos\n  edka apps publish ./memos\n  edka apps share ./memos"}
	a.strictGroup(apps)
	var all bool
	list := &cobra.Command{Use: "list", Short: "List installed apps", Long: "List installed apps in the linked or selected cluster, or in every cluster\nwhen none is linked. Use --all to include every cluster.", Args: cobra.NoArgs, Example: "  edka apps list\n  edka apps list --cluster production\n  edka apps list --all --json", RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.appRows(cmd.Context(), all)
		if err != nil {
			return err
		}
		return a.renderRows(rows, appColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List apps in every cluster, not only the linked one")
	get := &cobra.Command{Use: "get [app]", Short: "Show an installed app", Args: cobra.MaximumNArgs(1), Example: "  edka apps get strapi\n  edka apps get strapi --json", RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolveApp(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		path, err := clusterItemPath(c, "apps")
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "GET", path, nil, nil)
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
		return a.showApp(m, text(c.Record, "cluster_name"), c.ID)
	}}
	logs := a.logsCommand("logs [app]", "Read an app's logs; follow with --follow", func(ctx context.Context, target string) (string, error) {
		c, err := a.resolveApp(ctx, target)
		if err != nil {
			return "", err
		}
		path, err := clusterItemPath(c, "apps")
		return path + "/logs", err
	})
	uninstall := &cobra.Command{Use: "uninstall <app>", Short: "Uninstall an app from its cluster", Args: cobra.ExactArgs(1), Example: "  edka apps uninstall onlinetest\n  edka apps uninstall onlinetest --yes", RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolveApp(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		path, err := clusterItemPath(c, "apps")
		if err != nil {
			return err
		}
		if err := a.confirm(fmt.Sprintf("Uninstall %s (%s) from cluster %s", c.Name, text(c.Record, "app_name"), text(c.Record, "cluster_name"))); err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "DELETE", path, nil, nil)
		if err != nil {
			return err
		}
		return a.render(response)
	}}
	var category string
	catalog := &cobra.Command{Use: "catalog [app]", Short: "List apps you can install, or an app's settings", Long: "List the apps you can install. Name an app to list its settings, with their\ndefaults and whether they are required, for `edka apps install <app> --set key=value`.", Args: cobra.MaximumNArgs(1), Example: "  edka apps catalog\n  edka apps catalog --category databases\n  edka apps catalog umami", RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			cluster, err := a.clusterID(cmd.Context())
			if err != nil {
				return err
			}
			app, err := a.resolveCatalogApp(cmd.Context(), cluster, args[0])
			if err != nil {
				return err
			}
			rows := []map[string]any{}
			for _, f := range appFields(app.Record) {
				if f.kind() != "hidden" {
					rows = append(rows, catalogRow(f))
				}
			}
			return a.renderRows(rows, catalogFieldColumns)
		}
		query := url.Values{}
		if category != "" {
			query.Set("category", category)
		}
		rows, err := a.objectsQuery(cmd.Context(), "/api/apps/catalog", query)
		if err != nil {
			return err
		}
		return a.renderRows(rows, []ui.Column{ui.Field("NAME", "name"), ui.Field("SLUG", "slug"), ui.Field("CATEGORY", "category"), ui.Field("VERSION", "version"), ui.Field("DESCRIPTION", "description")})
	}}
	catalog.Flags().StringVar(&category, "category", "", "Only apps in this category")
	a.completesFlag(catalog, "category", a.offers(a.catalogChoices("/api/apps/catalog", "category", "")))
	install, options := a.installCommand(), a.optionsCommand()
	diagnose := a.appDiagnoseCommand()
	a.completes(a.appChoices(), get, diagnose, logs, uninstall)
	a.completes(a.catalogAppChoices, install, catalog, options)
	apps.AddCommand(list, get, diagnose, logs, install, uninstall, catalog, options)
	apps.AddCommand(a.customAppCommands()...)
	root.AddCommand(apps)
}
