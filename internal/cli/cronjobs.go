package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// cronjobStatus shows a suspended schedule even while the row is deployed.
func cronjobStatus(m map[string]any) string {
	if suspended, _ := m["suspend"].(bool); suspended && text(m, "status") == "deployed" {
		return "suspended"
	}
	return text(m, "status")
}

// lastRun combines the Kubernetes fields Edka adds to deployed cronjobs.
func lastRun(m map[string]any) string {
	run := when(m["lastScheduleTime"])
	if result := strings.ToLower(text(m, "lastJobStatus")); result != "" {
		run = strings.TrimSpace(run + " (" + result + ")")
	}
	return run
}

// elapsed is how long something ran between two API timestamps, or until now
// while it is still running.
func elapsed(start, end any, running bool) string {
	from, err := time.Parse(time.RFC3339, fmt.Sprint(start))
	if err != nil {
		return ""
	}
	to, err := time.Parse(time.RFC3339, fmt.Sprint(end))
	if err != nil {
		if !running {
			return ""
		}
		to = time.Now()
	}
	return to.Sub(from).Round(time.Second).String()
}
func duration(m map[string]any) string {
	return elapsed(m["startTime"], m["completionTime"], text(m, "status") == "Running")
}

var cronjobColumns = []ui.Column{
	ui.Field("NAME", "name"),
	ui.Field("SCHEDULE", "schedule"),
	{Header: "STATUS", Value: cronjobStatus},
	{Header: "LAST RUN", Value: lastRun},
	{Header: "IMAGE", Value: imageOf},
	ui.Field("CLUSTER", "cluster_name"),
}

func cronjobNames(m map[string]any) []string { return []string{text(m, "name")} }
func cronjobDetail(m map[string]any) string {
	return strings.Trim(text(m, "schedule")+" · "+text(m, "cluster_name"), " ·")
}

func (a *App) resolveCronjob(ctx context.Context, target string) (*candidate, string, error) {
	c, err := a.resolveScoped(ctx, "cronjob", "cronjobs", target, "no cronjobs; create one with `edka api clusters cronjobs create --help`", cronjobNames, cronjobDetail)
	if err != nil {
		return nil, "", err
	}
	id, err := safeID(c.ID)
	return c, "/api/cronjobs/" + id, err
}

func (a *App) addCronjobs(root *cobra.Command) {
	cronjobs := &cobra.Command{Use: "cronjobs", Aliases: []string{"cronjob"}, Short: "Inspect, run and pause scheduled jobs", GroupID: "resources", Example: "  edka cronjobs list\n  edka cronjobs runs nightly-report\n  edka cronjobs trigger nightly-report\n  edka cronjobs logs nightly-report"}
	a.strictGroup(cronjobs)
	var all bool
	list := &cobra.Command{Use: "list", Short: "List cronjobs", Long: "List cronjobs in the linked or selected cluster, or in every cluster when none\nis linked. Use --all to include every cluster.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.clusterScopedRows(cmd.Context(), all, "cronjobs")
		if err != nil {
			return err
		}
		return a.renderRows(rows, cronjobColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List cronjobs in every cluster, not only the linked one")
	get := &cobra.Command{Use: "get [cronjob]", Short: "Show a cronjob and its last run", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveCronjob(cmd.Context(), argument(args))
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
		runtime := map[string]any{}
		if status, err := a.request(cmd.Context(), "GET", path+"/status", nil, nil); err == nil {
			if v, err := identityData(status.Body); err == nil {
				runtime = v
			}
		}
		suspended := "no"
		if s, _ := m["suspend"].(bool); s {
			suspended = "yes"
		}
		active := ""
		if runtime["activeJobs"] != nil {
			active = fmt.Sprint(number(runtime["activeJobs"]))
		}
		return ui.Fields(a.Out, [][2]string{
			{"Name", text(m, "name")},
			{"Status", cronjobStatus(m)},
			{"Schedule", text(m, "schedule")},
			{"Suspended", suspended},
			{"Image", imageOf(m)},
			{"Command", text(m, "run_command")},
			{"Concurrency", text(m, "concurrency_policy")},
			{"Last run", when(runtime["lastScheduleTime"])},
			{"Last success", when(runtime["lastSuccessfulTime"])},
			{"Active runs", active},
			{"Cluster", text(c.Record, "cluster_name")},
			{"Namespace", text(m, "namespace")},
			{"Updated", when(m["updated_at"])},
			{"ID", c.ID},
		}, a.color)
	}}
	var limit int
	runs := &cobra.Command{Use: "runs [cronjob]", Short: "List recent runs, newest first", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		// Edka rejects limits outside 1–100.
		if limit < 1 || limit > 100 {
			return fmt.Errorf("limit must be between 1 and 100")
		}
		_, path, err := a.resolveCronjob(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		rows, err := a.objectsQuery(cmd.Context(), path+"/jobs", url.Values{"limit": {fmt.Sprint(limit)}})
		if err != nil {
			return err
		}
		return a.renderRows(rows, []ui.Column{
			ui.Field("RUN", "name"),
			ui.Field("STATUS", "status"),
			{Header: "STARTED", Value: func(m map[string]any) string { return when(m["startTime"]) }},
			{Header: "DURATION", Value: duration},
		})
	}}
	runs.Flags().IntVar(&limit, "limit", 20, "Number of runs (1–100)")
	var job string
	logs := a.logsCommand("logs [cronjob]", "Read a run's logs; the newest run by default", func(ctx context.Context, target string) (string, error) {
		_, path, err := a.resolveCronjob(ctx, target)
		if err != nil {
			return "", err
		}
		path += "/logs"
		if job != "" {
			path += "?" + url.Values{"jobName": {job}}.Encode()
		}
		return path, nil
	})
	logs.Flags().StringVar(&job, "run", "", "Run name from `edka cronjobs runs` (default: the newest)")
	trigger := &cobra.Command{Use: "trigger <cronjob>", Short: "Start a run now", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveCronjob(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "POST", path+"/trigger", nil, nil)
		if err != nil {
			return err
		}
		a.message("✓ Queued a run of %s\n  Next: edka cronjobs runs %s", ui.Clean(c.Name), ui.Clean(c.Name))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	suspendCommand := func(use, short string, suspend bool) *cobra.Command {
		return &cobra.Command{Use: use + " <cronjob>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			c, path, err := a.resolveCronjob(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			response, err := a.request(cmd.Context(), "PATCH", path+"/suspend", nil, jsonBody(map[string]bool{"suspend": suspend}))
			if err != nil {
				return err
			}
			if suspend {
				a.message("✓ Suspending %s; scheduled runs stop until `edka cronjobs resume %s`", ui.Clean(c.Name), ui.Clean(c.Name))
			} else {
				a.message("✓ Resuming %s", ui.Clean(c.Name))
			}
			if a.output != "table" {
				return a.render(response)
			}
			return nil
		}}
	}
	remove := &cobra.Command{Use: "delete <cronjob>", Short: "Delete a cronjob and its Kubernetes resources", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.resolveCronjob(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := a.confirm(fmt.Sprintf("Delete cronjob %s in cluster %s", c.Name, text(c.Record, "cluster_name"))); err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "DELETE", path, nil, nil)
		if err != nil {
			return err
		}
		return a.render(response)
	}}
	suspend, resume := suspendCommand("suspend", "Stop scheduled runs", true), suspendCommand("resume", "Restart scheduled runs", false)
	diagnose := a.cronjobDiagnoseCommand()
	a.completes(a.scopedChoices("cronjobs", cronjobNames, cronjobDetail), get, diagnose, runs, logs, trigger, suspend, resume, remove)
	cronjobs.AddCommand(list, get, diagnose, runs, logs, trigger, suspend, resume, remove)
	root.AddCommand(cronjobs)
}
