package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// appDiagnosis is what `edka apps diagnose` read about an installed app and
// concluded.
type appDiagnosis struct {
	Healthy  bool             `json:"healthy"`
	Findings []finding        `json:"findings"`
	App      map[string]any   `json:"app"`
	Pods     []map[string]any `json:"pods"`
	Events   []map[string]any `json:"events"`
	Logs     *podLogs         `json:"logs"`
	Unread   []unread         `json:"unread"`
}

// appDiagnoseCommand reports why an installed app is not running.
func (a *App) appDiagnoseCommand() *cobra.Command {
	var tail int
	cmd := &cobra.Command{Use: "diagnose [app]", Short: "Find out why an app is not running", Long: "Read an installed app as Edka records it, its pods, the Kubernetes events and the\nlog of its failing pod, and report what is wrong with the commands to run next.\nThe command changes nothing.\n\nThe findings come first, then what they were read from. A source that can't be\nread is named and the rest is still reported. The command exits unsuccessfully\nwhen it finds a problem. With --json, stdout has `healthy`, `findings` and\neverything that was read.", Args: cobra.MaximumNArgs(1), Example: "  edka apps diagnose strapi\n  edka apps diagnose strapi --tail 100\n  edka apps diagnose strapi --json", RunE: func(cmd *cobra.Command, args []string) error {
		if tail < 1 || tail > 2000 {
			return fmt.Errorf("tail must be between 1 and 2000")
		}
		c, err := a.resolveApp(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		d, err := a.diagnoseApp(cmd.Context(), c, tail)
		if err != nil {
			return err
		}
		return a.finishDiagnosis(c.Name, d, d.Findings, func() error { return a.renderAppDiagnosis(d, c) })
	}}
	cmd.Flags().IntVarP(&tail, "tail", "n", 30, "Number of log lines of the failing pod (at most 2000)")
	return cmd
}

// runningPods leaves out the pods of Jobs that finished, such as the one that
// installed the app: they are not ready and nothing is wrong with them.
func runningPods(pods []map[string]any) []map[string]any {
	return slices.DeleteFunc(slices.Clone(pods), func(pod map[string]any) bool { return text(pod, "status") == "succeeded" })
}

// diagnoseApp reads what Edka and the cluster know about an installed app.
// Only the app itself has to be readable: every other source that fails is
// named in Unread, since a diagnosis is for an app that does not work.
func (a *App) diagnoseApp(ctx context.Context, c *candidate, tail int) (*appDiagnosis, error) {
	path, err := clusterItemPath(c, "apps")
	if err != nil {
		return nil, err
	}
	response, err := a.request(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	app, err := identityData(response.Body)
	if err != nil {
		return nil, err
	}
	d := &appDiagnosis{App: app, Findings: []finding{}, Pods: []map[string]any{}, Events: []map[string]any{}, Unread: []unread{}}

	var podsErr error
	if response, err := a.request(ctx, "GET", path+"/pods", nil, nil); err != nil {
		podsErr = err
		missed(&d.Unread, "pods", err)
	} else if runtime, err := identityData(response.Body); err != nil {
		podsErr = err
		missed(&d.Unread, "pods", err)
	} else {
		d.Pods = podsOf(runtime)
	}
	// The events and the log are those of one pod: the failed pod with the most
	// restarts, else a pod that is not ready.
	if pod := failingPod(map[string]any{"pods": anyList(runningPods(d.Pods))}); pod != nil {
		if events, err := a.workloadEvents(ctx, text(c.Record, "cluster_id"), "pods", text(pod, "name"), first(text(pod, "namespace"), text(app, "namespace"))); err != nil {
			missed(&d.Unread, "events of "+text(pod, "name"), err)
		} else {
			d.Events = events
		}
		if !slices.Contains(notStartedReasons, text(pod, "reason")) {
			if logs, err := a.podLogs(ctx, path+"/logs", pod, tail); err != nil {
				missed(&d.Unread, "logs of "+text(pod, "name"), err)
			} else {
				d.Logs = logs
			}
		}
	}
	d.Findings = appFindings(d, c, podsErr)
	d.Healthy = problems(d.Findings) == 0
	return d, nil
}

// appFindings concludes from what an app diagnosis read, with the rules of a
// deployment's pods and events.
func appFindings(d *appDiagnosis, c *candidate, podsErr error) []finding {
	found := []finding{}
	add := func(problem bool, summary, detail string, next ...string) {
		found = append(found, finding{Problem: problem, Summary: summary, Detail: ui.Clean(detail), Next: next})
	}
	// The name the console shows may have spaces.
	logs := "edka apps logs " + shellJoin([]string{ui.Clean(c.Name)})
	w := workload{logs: logs, capacity: []string{"edka nodepools list", "Add servers with `edka nodepools scale <pool>`."}}

	progress, _ := d.App["progress"].(map[string]any)
	status, message := text(d.App, "status"), text(progress, "message")
	switch status {
	case "failed", "error":
		add(true, "Edka's last install or update of the app failed.", message, logs)
	case "uninstall_failed":
		add(true, "Edka could not uninstall the app.", message)
	case "installing", "installing_dependencies", "configuring", "deploying":
		add(false, "Edka is still installing or updating the app.", message)
	case "uninstalling":
		add(false, "Edka is uninstalling the app.", message)
	case "uninstalled":
		add(false, "Edka uninstalled the app.", "")
	}

	if podsErr != nil {
		reason, _, _ := strings.Cut(podsErr.Error(), "\n")
		add(true, "Edka could not read the app's pods from its cluster.", reason, "edka clusters diagnose "+ui.Clean(text(c.Record, "cluster_name")))
		return found
	}

	pods := runningPods(d.Pods)
	explained, ready, failed := map[string]bool{}, 0, false
	for _, pod := range pods {
		if isReady, _ := pod["ready"].(bool); isReady {
			ready++
			continue
		}
		failed = failed || text(pod, "status") == "failed"
		reason := text(pod, "reason")
		if reason == "" || explained[reason] {
			continue
		}
		explained[reason] = true
		podFinding(add, w, pod)
	}
	// The scheduler's reason on the pod and its event say the same.
	explained["FailedScheduling"] = explained["Unschedulable"]
	rollout := "pending"
	if failed {
		rollout = "failed"
	}
	eventFindings(add, w, d.Events, map[string]any{"status": rollout, "pods": anyList(pods)}, explained)

	switch {
	case len(pods) == 0 && status == "installed":
		add(false, "Edka found no pods of the app in the cluster.", "", "edka apps get "+shellJoin([]string{ui.Clean(c.Name)}))
	case ready < len(pods) && problems(found) == 0:
		add(false, fmt.Sprintf("%d of %d pods are ready.", ready, len(pods)), "", logs)
	}
	if len(found) == 0 {
		add(false, fmt.Sprintf("No problem found. %d of %d pods are ready.", ready, len(pods)), "")
	}
	return found
}

// renderAppDiagnosis prints the findings, then what they were read from.
func (a *App) renderAppDiagnosis(d *appDiagnosis, c *candidate) error {
	if err := a.renderFindings(d.Findings); err != nil {
		return err
	}
	m := d.App
	status := text(m, "status")
	if progress, ok := m["progress"].(map[string]any); ok && status != "installed" && text(progress, "message") != "" {
		status += ": " + text(progress, "message")
	}
	if err := ui.Fields(a.Out, [][2]string{
		{"Name", first(appName(m), c.Name)},
		{"App", text(m, "app_name")},
		{"Version", text(m, "version")},
		{"Cluster", text(c.Record, "cluster_name")},
		{"Namespace", text(m, "namespace")},
		{"Status", status},
		{"Release", text(m, "release_name")},
	}, a.color); err != nil {
		return err
	}
	if err := a.renderEvidence(d.Pods, d.Events, d.Logs); err != nil {
		return err
	}
	a.reportUnread(d.Unread)
	return nil
}
