package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

func configuration(m map[string]any) map[string]any {
	c, _ := m["configuration"].(map[string]any)
	return c
}
func engineOf(m map[string]any) string {
	return strings.TrimSpace(text(m, "engine") + " " + ui.Text(m["version"]))
}
func storageOf(m map[string]any) string {
	if size := configuration(m)["storage_size"]; size != nil {
		return ui.Text(size) + " GiB"
	}
	return ""
}

var databaseColumns = []ui.Column{
	ui.Field("NAME", "database_name"),
	{Header: "ENGINE", Value: engineOf},
	ui.Field("STATUS", "status"),
	{Header: "INSTANCES", Value: func(m map[string]any) string { return ui.Text(configuration(m)["instances_count"]) }},
	{Header: "STORAGE", Value: storageOf},
	ui.Field("CLUSTER", "cluster_name"),
}

func databaseNames(m map[string]any) []string { return []string{text(m, "database_name")} }
func databaseDetail(m map[string]any) string {
	return strings.Trim(engineOf(m)+" · "+text(m, "cluster_name"), " ·")
}

func (a *App) resolveDatabase(ctx context.Context, target string) (*candidate, string, error) {
	c, err := a.resolveScoped(ctx, "database", "databases", target, "no databases; create one with `edka api clusters databases create --help`", databaseNames, databaseDetail)
	if err != nil {
		return nil, "", err
	}
	path, err := clusterItemPath(c, "databases")
	return c, path, err
}

// runtimeStatus reads a database's live status; nil when it is unavailable.
func (a *App) runtimeStatus(ctx context.Context, path string) map[string]any {
	response, err := a.request(ctx, "GET", path+"/status", nil, nil)
	if err != nil {
		return nil
	}
	status, err := record(response.Body, "status")
	if err != nil {
		return nil
	}
	return status
}

func (a *App) addDatabases(root *cobra.Command) {
	databases := &cobra.Command{Use: "databases", Aliases: []string{"database", "db"}, Short: "Inspect, back up and delete databases", GroupID: "resources", Example: "  edka databases list\n  edka databases get orders\n  edka databases backup orders\n  edka databases backups orders"}
	a.strictGroup(databases)
	var all bool
	list := &cobra.Command{Use: "list", Short: "List databases", Long: "List databases in the linked or selected cluster, or in every cluster when\nnone is linked. Use --all to include every cluster.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.clusterScopedRows(cmd.Context(), all, "databases")
		if err != nil {
			return err
		}
		return a.renderRows(rows, databaseColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List databases in every cluster, not only the linked one")
	get := &cobra.Command{Use: "get [database]", Short: "Show a database, its instances and recovery window", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveDatabase(cmd.Context(), argument(args))
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
		m, err := record(response.Body, "database")
		if err != nil {
			return err
		}
		metadata, _ := m["metadata"].(map[string]any)
		message := ""
		if text(m, "status") != "ready" && metadata != nil {
			message = first(text(metadata, "last_error"), text(metadata, "message"))
		}
		fields := [][2]string{
			{"Name", text(m, "database_name")},
			{"Engine", engineOf(m)},
			{"Status", text(m, "status")},
			{"Message", message},
			{"Instances", ui.Text(configuration(m)["instances_count"])},
			{"Storage", storageOf(m)},
			{"Backups", map[bool]string{true: "enabled", false: "disabled"}[configuration(m)["backup_enabled"] == true]},
		}
		if status := a.runtimeStatus(cmd.Context(), path); status != nil {
			ready := ""
			if status["instances"] != nil {
				ready = fmt.Sprintf("%d/%d", number(status["readyInstances"]), number(status["instances"]))
			}
			window := ""
			if w, ok := status["recoveryWindow"].(map[string]any); ok && w["earliest"] != nil {
				window = when(w["earliest"]) + " – " + first(when(w["latest"]), "now")
			}
			fields = append(fields,
				[2]string{"Ready", ready},
				[2]string{"Primary", text(status, "currentPrimary")},
				[2]string{"Last backup", when(status["lastSuccessfulBackup"])},
				[2]string{"Recovery window", window},
				[2]string{"External host", text(status, "externalHostname")},
			)
		}
		fields = append(fields,
			[2]string{"Cluster", text(c.Record, "cluster_name")},
			[2]string{"Namespace", ui.Text(configuration(m)["namespace"])},
			[2]string{"Created", when(m["created_at"])},
			[2]string{"ID", c.ID},
		)
		return ui.Fields(a.Out, fields, a.color)
	}}
	backups := &cobra.Command{Use: "backups [database]", Short: "List a database's backups, newest first", Long: "List a database's backups, newest first. PostgreSQL reports its ten most recent.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, path, err := a.resolveDatabase(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "GET", path+"/status", nil, nil)
		if err != nil {
			return err
		}
		status, err := record(response.Body, "status")
		if err != nil {
			return err
		}
		rows := []map[string]any{}
		if list, ok := status["backups"].([]any); ok {
			for _, backup := range list {
				if m, ok := backup.(map[string]any); ok {
					rows = append(rows, m)
				}
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return text(rows[i], "startedAt") > text(rows[j], "startedAt") })
		return a.renderRows(rows, []ui.Column{
			{Header: "BACKUP", Value: func(m map[string]any) string { return first(text(m, "backupName"), text(m, "name")) }},
			ui.Field("PHASE", "phase"),
			ui.Field("METHOD", "method"),
			{Header: "STARTED", Value: func(m map[string]any) string { return when(m["startedAt"]) }},
			{Header: "DURATION", Value: func(m map[string]any) string {
				return elapsed(m["startedAt"], m["stoppedAt"], text(m, "phase") == "running")
			}},
		})
	}}
	backup := &cobra.Command{Use: "backup <database>", Short: "Start a backup now", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveDatabase(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "POST", path+"/backups", nil, nil)
		if err != nil {
			return err
		}
		a.message("✓ Started a backup of %s\n  Next: edka databases backups %s", ui.Clean(c.Name), ui.Clean(c.Name))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	logs := a.logsCommand("logs [database]", "Read a database's logs; follow with --follow", func(ctx context.Context, target string) (string, error) {
		_, path, err := a.resolveDatabase(ctx, target)
		return path + "/logs", err
	})
	var force bool
	remove := &cobra.Command{Use: "delete <database>", Short: "Delete a database and its data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveDatabase(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := a.confirmTyped(fmt.Sprintf("Delete %s database %s in cluster %s and its data", text(c.Record, "engine"), c.Name, text(c.Record, "cluster_name")), c.Name); err != nil {
			return err
		}
		if force {
			path += "?" + url.Values{"force": {"true"}}.Encode()
		}
		response, err := a.request(cmd.Context(), "DELETE", path, nil, nil)
		if err != nil {
			return err
		}
		a.message("✓ Deleting %s in the background", ui.Clean(c.Name))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	remove.Flags().BoolVar(&force, "force", false, "Delete even while apps use it or it is still provisioning")
	diagnose := a.databaseDiagnoseCommand()
	a.completes(a.scopedChoices("databases", databaseNames, databaseDetail), get, diagnose, backups, backup, logs, remove)
	databases.AddCommand(list, get, diagnose, backups, backup, logs, remove)
	root.AddCommand(databases)
}
