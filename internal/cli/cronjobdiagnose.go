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

// cronjobDiagnosis is what `edka cronjobs diagnose` read about a cronjob and
// concluded.
type cronjobDiagnosis struct {
	Healthy  bool             `json:"healthy"`
	Findings []finding        `json:"findings"`
	Cronjob  map[string]any   `json:"cronjob"`
	Runtime  map[string]any   `json:"runtime"`
	Runs     []map[string]any `json:"runs"`
	Events   []map[string]any `json:"events"`
	Logs     *podLogs         `json:"logs"`
	Unread   []unread         `json:"unread"`
}

// cronjobDiagnoseCommand reports why a cronjob does not run or why its runs fail.
func (a *App) cronjobDiagnoseCommand() *cobra.Command {
	var tail int
	cmd := &cobra.Command{Use: "diagnose [cronjob]", Short: "Find out why a cronjob's runs fail or don't start", Long: "Read a cronjob as Edka records it, its schedule in the cluster, its last five\nruns, Kubernetes events and the log of the last run when it failed, and report\nwhat is wrong with the commands to run next. The command changes nothing.\n\nThe findings come first, then what they were read from. A source that can't be\nread is named and the rest is still reported. The command exits unsuccessfully\nwhen it finds a problem. With --json, stdout has `healthy`, `findings` and\neverything that was read.", Args: cobra.MaximumNArgs(1), Example: "  edka cronjobs diagnose nightly-report\n  edka cronjobs diagnose nightly-report --tail 100\n  edka cronjobs diagnose nightly-report --json", RunE: func(cmd *cobra.Command, args []string) error {
		if tail < 1 || tail > 2000 {
			return fmt.Errorf("tail must be between 1 and 2000")
		}
		c, path, err := a.resolveCronjob(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		d, err := a.diagnoseCronjob(cmd.Context(), c, path, tail)
		if err != nil {
			return err
		}
		return a.finishDiagnosis(c.Name, d, d.Findings, func() error { return a.renderCronjobDiagnosis(d, c) })
	}}
	cmd.Flags().IntVarP(&tail, "tail", "n", 30, "Number of log lines of the failed run (at most 2000)")
	return cmd
}

// lastFinished is the newest run that ended, or nil. Edka lists runs newest first.
func lastFinished(runs []map[string]any) map[string]any {
	for _, run := range runs {
		if status := text(run, "status"); status == "Succeeded" || status == "Failed" {
			return run
		}
	}
	return nil
}

// diagnoseCronjob reads what Edka and the cluster know about the cronjob at
// path. Only the cronjob itself has to be readable: every other source that
// fails is named in Unread.
func (a *App) diagnoseCronjob(ctx context.Context, c *candidate, path string, tail int) (*cronjobDiagnosis, error) {
	response, err := a.request(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	cronjob, err := identityData(response.Body)
	if err != nil {
		return nil, err
	}
	d := &cronjobDiagnosis{Cronjob: cronjob, Findings: []finding{}, Runs: []map[string]any{}, Events: []map[string]any{}, Unread: []unread{}}
	name := first(text(cronjob, "name"), c.Name)
	if response, err := a.request(ctx, "GET", path+"/status", nil, nil); err != nil {
		missed(&d.Unread, "runtime status", err)
	} else if d.Runtime, err = identityData(response.Body); err != nil {
		missed(&d.Unread, "runtime status", err)
	}
	rows, runsErr := a.objectsQuery(ctx, path+"/jobs", url.Values{"limit": {"5"}})
	if runsErr != nil {
		missed(&d.Unread, "runs", runsErr)
	} else {
		d.Runs = rows
	}
	if events, err := a.workloadEvents(ctx, first(text(cronjob, "cluster_id"), text(c.Record, "cluster_id")), "cronjobs", name, text(cronjob, "namespace")); err != nil {
		missed(&d.Unread, "events", err)
	} else {
		d.Events = events
	}
	if run := lastFinished(d.Runs); run != nil && text(run, "status") == "Failed" {
		if logs, err := a.runLogs(ctx, path, text(run, "name"), tail); err != nil {
			missed(&d.Unread, "logs of "+text(run, "name"), err)
		} else {
			d.Logs = logs
		}
	}
	d.Findings = cronjobFindings(d, name, runsErr == nil)
	d.Healthy = problems(d.Findings) == 0
	return d, nil
}

// runLogs reads the end of the log of one run of the cronjob at path.
func (a *App) runLogs(ctx context.Context, path, run string, tail int) (*podLogs, error) {
	response, err := a.request(ctx, "GET", path+"/logs", url.Values{"jobName": {run}, "tailLines": {fmt.Sprint(tail)}}, nil)
	if err != nil {
		return nil, err
	}
	body, err := identityData(response.Body)
	if err != nil {
		return nil, err
	}
	logs, _ := body["logs"].(string)
	return &podLogs{Pod: first(text(body, "podName"), run), Container: text(body, "containerName"), Text: strings.TrimRight(ui.CleanText(logs), "\n")}, nil
}

// runWarning is the newest warning Kubernetes recorded about a run: about its
// Job or one of its pods, which Kubernetes names after the Job. With reasons,
// it is the newest warning that has one of them.
func runWarning(events []map[string]any, run string, reasons ...string) map[string]any {
	for _, event := range events {
		if text(event, "type") != "Warning" || len(reasons) > 0 && !slices.Contains(reasons, text(event, "reason")) {
			continue
		}
		object, _ := event["object"].(map[string]any)
		if name := text(object, "name"); name == run || strings.HasPrefix(name, run+"-") {
			return event
		}
	}
	return nil
}

// cronjobFindings concludes from what a cronjob diagnosis read. Each rule
// names what was observed, quotes Kubernetes for the cause, and lists commands.
// listed is false when the runs could not be read.
func cronjobFindings(d *cronjobDiagnosis, name string, listed bool) []finding {
	name = ui.Clean(name)
	found := []finding{}
	add := func(problem bool, summary, detail string, next ...string) {
		found = append(found, finding{Problem: problem, Summary: summary, Detail: ui.Clean(detail), Next: next})
	}
	quote := func(event map[string]any) string {
		return strings.Trim(text(event, "reason")+": "+text(event, "message"), ": ")
	}

	status := cronjobStatus(d.Cronjob)
	switch status {
	case "failed":
		add(true, "Edka's last change to the cronjob failed.", "", "edka cronjobs get "+name)
	case "pending", "creating", "updating", "suspending":
		add(false, fmt.Sprintf("Edka is still applying the cronjob; its status is %s.", status), "")
	case "deleting":
		add(false, "Edka is deleting the cronjob.", "")
	case "deleted":
		add(false, "Edka deleted the cronjob.", "")
	case "suspended":
		add(false, "The schedule is suspended, so no run starts.", "", fmt.Sprintf("edka cronjobs resume %s", name))
	}

	// The newest warning about the schedule itself. Kubernetes gives these two
	// reasons when it could not start a run; another warning is a note.
	for _, event := range d.Events {
		object, _ := event["object"].(map[string]any)
		if text(event, "type") != "Warning" || text(object, "kind") != "CronJob" {
			continue
		}
		if reason := text(event, "reason"); reason == "FailedCreate" || reason == "FailedNeedsStart" {
			add(true, "Kubernetes could not start a run.", quote(event))
		} else {
			add(false, "Kubernetes warns about the cronjob's schedule.", quote(event))
		}
		break
	}

	last := lastFinished(d.Runs)
	if last != nil && text(last, "status") == "Failed" {
		run := ui.Clean(text(last, "name"))
		add(true, fmt.Sprintf("The last run, %s, failed.", run), quote(runWarning(d.Events, text(last, "name"))),
			"Read what it printed last, under Logs.",
			fmt.Sprintf("edka cronjobs logs %s --run %s", name, run))
	}
	// A run that waits has not failed yet. Edka reports a run as Unknown while
	// its Job has no pod, which lasts when Kubernetes can't create the pod, and
	// as Running once it has one, which may wait for a node.
	for _, run := range d.Runs {
		waiting := ui.Clean(text(run, "name"))
		switch text(run, "status") {
		case "Unknown":
			if event := runWarning(d.Events, text(run, "name"), "FailedCreate"); event != nil {
				add(true, fmt.Sprintf("Kubernetes can't create the pod of %s.", waiting), text(event, "message"))
			}
		case "Running":
			if event := runWarning(d.Events, text(run, "name")); text(event, "reason") == "FailedScheduling" {
				add(true, fmt.Sprintf("No node can run %s.", waiting), text(event, "message"), "edka nodepools list", "Add servers with `edka nodepools scale <pool>`.")
			}
		default:
			continue
		}
		break
	}
	if listed && len(d.Runs) == 0 && status == "deployed" {
		add(false, "No run has started yet.", "", fmt.Sprintf("edka cronjobs trigger %s", name))
	}

	if len(found) == 0 {
		healthy := "No problem found."
		switch {
		case last != nil:
			healthy = fmt.Sprintf("No problem found. The last run, %s, succeeded.", ui.Clean(text(last, "name")))
		case len(d.Unread) > 0:
			// A source that was not read may hold a problem.
			healthy = "No problem found in what could be read."
		}
		add(false, healthy, "")
	}
	return found
}

// renderCronjobDiagnosis prints the findings, then what they were read from.
func (a *App) renderCronjobDiagnosis(d *cronjobDiagnosis, c *candidate) error {
	if err := a.renderFindings(d.Findings); err != nil {
		return err
	}
	m := d.Cronjob
	active := ""
	if d.Runtime["activeJobs"] != nil {
		active = fmt.Sprint(number(d.Runtime["activeJobs"]))
	}
	if err := ui.Fields(a.Out, [][2]string{
		{"Cronjob", first(text(m, "name"), c.Name)},
		{"Cluster", first(text(m, "cluster_name"), text(c.Record, "cluster_name"))},
		{"Namespace", text(m, "namespace")},
		{"Status", cronjobStatus(m)},
		{"Schedule", text(m, "schedule")},
		{"Image", imageOf(m)},
		{"Last run", when(d.Runtime["lastScheduleTime"])},
		{"Last success", when(d.Runtime["lastSuccessfulTime"])},
		{"Active runs", active},
	}, a.color); err != nil {
		return err
	}
	if len(d.Runs) > 0 {
		fmt.Fprintln(a.Out)
		if err := ui.Table(a.Out, []ui.Column{
			ui.Field("RUN", "name"),
			ui.Field("STATUS", "status"),
			{Header: "STARTED", Value: func(m map[string]any) string { return when(m["startTime"]) }},
			{Header: "DURATION", Value: duration},
		}, d.Runs, a.color); err != nil {
			return err
		}
	}
	if err := a.renderEvidence(nil, d.Events, d.Logs); err != nil {
		return err
	}
	a.reportUnread(d.Unread)
	return nil
}
