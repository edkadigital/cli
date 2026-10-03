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

// previewDiagnosis is what `edka previews diagnose` read about a preview and
// concluded.
type previewDiagnosis struct {
	Healthy  bool           `json:"healthy"`
	Findings []finding      `json:"findings"`
	Preview  map[string]any `json:"preview"`
	Runtime  map[string]any `json:"runtime"`
	// Pods are the failing pods of the preview, as its cluster reports them.
	Pods   []map[string]any `json:"pods"`
	Events []map[string]any `json:"events"`
	Logs   *podLogs         `json:"logs"`
	Unread []unread         `json:"unread"`
}

// previewDiagnoseCommand reports why a preview is not running.
func (a *App) previewDiagnoseCommand() *cobra.Command {
	var tail int
	cmd := &cobra.Command{Use: "diagnose [pr]", Short: "Find out why a preview is not running", Long: "Read a preview as Edka records it, its rollout, pods, Kubernetes events and the\nlog of its failing pod, and report what is wrong with the commands to run next.\nThe command changes nothing.\n\nThe findings come first, then what they were read from. A source that can't be\nread is named and the rest is still reported. The command exits unsuccessfully\nwhen it finds a problem. With --json, stdout has `healthy`, `findings` and\neverything that was read.", Args: cobra.MaximumNArgs(1), Example: "  edka previews diagnose 12\n  edka previews diagnose 12 --deployment api --tail 100\n  edka previews diagnose 12 --json", RunE: func(cmd *cobra.Command, args []string) error {
		if tail < 1 || tail > 2000 {
			return fmt.Errorf("tail must be between 1 and 2000")
		}
		c, preview, path, err := a.resolvePreview(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		d, pods := a.diagnosePreview(cmd.Context(), c, preview, path, tail)
		return a.finishDiagnosis("preview #"+previewNumber(preview), d, d.Findings, func() error { return a.renderPreviewDiagnosis(d, c, pods) })
	}}
	cmd.Flags().IntVarP(&tail, "tail", "n", 30, "Number of log lines of the failing pod (at most 2000)")
	return cmd
}

// diagnosePreview reads what Edka and the cluster know about a preview of the
// deployment c, which is at path. Every source that fails is named in Unread.
// It also returns the preview's pods with the reasons its findings are from.
func (a *App) diagnosePreview(ctx context.Context, c *candidate, preview map[string]any, path string, tail int) (*previewDiagnosis, []map[string]any) {
	d := &previewDiagnosis{Preview: preview, Findings: []finding{}, Pods: []map[string]any{}, Events: []map[string]any{}, Unread: []unread{}}
	cluster := first(text(c.Record, "cluster_id"), a.cluster)

	var runtimeErr error
	if response, err := a.request(ctx, "GET", path+"/status", nil, nil); err != nil {
		runtimeErr = err
		missed(&d.Unread, "runtime status", err)
	} else if d.Runtime, err = identityData(response.Body); err != nil {
		runtimeErr = err
		missed(&d.Unread, "runtime status", err)
	}
	// A preview that runs nothing, such as one whose build failed, has no events
	// and no pods to read.
	if d.Runtime != nil {
		namespace := first(text(d.Runtime, "namespace"), text(preview, "namespace"))
		if events, err := a.workloadEvents(ctx, cluster, "deployments", first(text(d.Runtime, "name"), text(preview, "resource_name")), namespace); err != nil {
			missed(&d.Unread, "events", err)
		} else {
			d.Events = events
		}
		// The status of a preview gives no reason for a pod that is not ready.
		// The cluster's list of failing pods does.
		if waiting := slices.ContainsFunc(podsOf(d.Runtime), func(pod map[string]any) bool { ready, _ := pod["ready"].(bool); return !ready }); waiting {
			if failing, err := a.failingPods(ctx, cluster, namespace); err != nil {
				missed(&d.Unread, "failing pods", err)
			} else {
				for _, pod := range failing {
					if slices.ContainsFunc(podsOf(d.Runtime), func(own map[string]any) bool { return text(own, "name") == text(pod, "name") }) {
						d.Pods = append(d.Pods, pod)
					}
				}
			}
		}
	}
	pods := previewPods(d.Runtime, d.Pods)
	if pod := failingPod(map[string]any{"pods": anyList(pods)}); pod != nil && !slices.Contains(notStartedReasons, text(pod, "reason")) {
		if logs, err := a.podLogs(ctx, path+"/logs", pod, tail); err != nil {
			missed(&d.Unread, "logs of "+text(pod, "name"), err)
		} else {
			d.Logs = logs
		}
	}
	d.Findings = previewFindings(d, pods, c, runtimeErr)
	d.Healthy = problems(d.Findings) == 0
	return d, pods
}

// failingPods lists the pods of a namespace that the cluster reports as
// failing, each with the reason and message of its containers.
func (a *App) failingPods(ctx context.Context, cluster, namespace string) ([]map[string]any, error) {
	cluster, err := safeID(cluster)
	if err != nil {
		return nil, fmt.Errorf("its record names no cluster")
	}
	response, err := a.request(ctx, "GET", "/api/clusters/"+cluster+"/pods/problematic", url.Values{"namespace": {first(namespace, "default")}}, nil)
	if err != nil {
		return nil, err
	}
	return objectsAt(response.Body, "pods")
}

// anyList is a list of records as a decoded response holds it.
func anyList(records []map[string]any) []any {
	list := make([]any, len(records))
	for i, m := range records {
		list[i] = m
	}
	return list
}

// previewPods gives a preview's pods the status, reason and message that a
// deployment's pods have. The status of a preview has a pod's phase only, so
// the rest is from the failing pods the cluster reports.
func previewPods(runtime map[string]any, failing []map[string]any) []map[string]any {
	pods := []map[string]any{}
	for _, own := range podsOf(runtime) {
		pod := map[string]any{"name": own["name"], "ready": own["ready"], "restartCount": own["restartCount"], "status": strings.ToLower(text(own, "status"))}
		for _, problem := range failing {
			if text(problem, "name") != text(own, "name") {
				continue
			}
			if kind := text(problem, "problemType"); kind != "pending" {
				pod["status"] = "failed"
			}
			pod["restartCount"] = max(number(own["restartCount"]), number(problem["restartCount"]))
			for _, container := range objectsOf(problem["containers"]) {
				if reason := text(container, "reason"); reason != "" {
					pod["reason"], pod["message"] = reason, text(container, "message")
					break
				}
			}
		}
		pods = append(pods, pod)
	}
	return pods
}

// previewRollout is the rollout of a preview in a deployment's terms: failed
// when a pod failed or Kubernetes gave the rollout up, deployed when every
// replica runs the preview's last change and is ready, and pending otherwise.
// The replicas that are ready may be those of the change before, while the
// ReplicaSet of the last one can't create its pods.
func previewRollout(runtime map[string]any, pods []map[string]any) string {
	if slices.ContainsFunc(pods, func(pod map[string]any) bool { return text(pod, "status") == "failed" }) {
		return "failed"
	}
	for _, condition := range objectsOf(runtime["conditions"]) {
		if text(condition, "type") == "Progressing" && text(condition, "status") == "False" {
			return "failed"
		}
	}
	replicas, _ := runtime["replicas"].(map[string]any)
	desired := number(replicas["desired"])
	if number(replicas["ready"]) >= desired && number(replicas["updated"]) >= desired && replicaFailure(runtime) == nil {
		return "deployed"
	}
	return "pending"
}

// previewFindings concludes from what a preview diagnosis read, with the
// rules of a deployment's pods and events.
func previewFindings(d *previewDiagnosis, pods []map[string]any, c *candidate, runtimeErr error) []finding {
	pr, deployment := previewNumber(d.Preview), ui.Clean(c.Name)
	found := []finding{}
	add := func(problem bool, summary, detail string, next ...string) {
		found = append(found, finding{Problem: problem, Summary: summary, Detail: ui.Clean(detail), Next: next})
	}
	w := workload{
		image:    ui.Clean(first(text(d.Runtime, "running_image"), text(d.Runtime, "configured_image"))),
		logs:     fmt.Sprintf("edka previews logs %s --deployment %s", pr, deployment),
		capacity: []string{"edka nodepools list", "Add servers with `edka nodepools scale <pool>`."},
	}

	// A preview that is still built, that failed or that is deleted may run
	// nothing, so its runtime status may be missing.
	status, idle := text(d.Preview, "status"), true
	switch status {
	case "failed":
		add(true, fmt.Sprintf("Preview #%s failed.", pr), text(d.Preview, "error_message"))
	case "pending", "building", "deploying":
		add(false, fmt.Sprintf("The preview is still %s.", status), "")
	case "deleting":
		add(false, "Edka is deleting the preview.", "")
	case "deleted":
		add(false, "Edka deleted the preview.", "")
	case "updating":
		idle = false
		add(false, "The preview is still updating.", "")
	default:
		idle = false
	}
	if runtimeErr != nil && !idle {
		reason, _, _ := strings.Cut(runtimeErr.Error(), "\n")
		add(true, "Edka could not read the preview from its cluster.", reason,
			"edka clusters diagnose "+ui.Clean(first(text(c.Record, "cluster_name"), text(c.Record, "cluster_id"))))
	}
	if d.Runtime == nil {
		return found
	}

	explained := map[string]bool{}
	for _, pod := range pods {
		if ready, _ := pod["ready"].(bool); ready {
			continue
		}
		reason := text(pod, "reason")
		if reason == "" || explained[reason] {
			continue
		}
		explained[reason] = true
		podFinding(add, w, pod)
	}
	rollout := previewRollout(d.Runtime, pods)
	runtime := map[string]any{"status": rollout, "pods": anyList(pods), "conditions": d.Runtime["conditions"]}
	if rollout != "deployed" {
		eventFindings(add, w, d.Events, runtime, explained)
	}
	switch {
	case rollout == "failed" && problems(found) == 0:
		add(true, "The rollout failed.", "", w.logs)
	case rollout == "pending" && problems(found) == 0:
		add(false, "The rollout is still running.", "", fmt.Sprintf("edka previews status %s --deployment %s", pr, deployment))
	}
	if len(found) == 0 {
		replicas, _ := d.Runtime["replicas"].(map[string]any)
		add(false, fmt.Sprintf("No problem found. %d of %d pods are ready.", number(replicas["ready"]), number(replicas["desired"])), "")
	}
	return found
}

// renderPreviewDiagnosis prints the findings, then what they were read from.
// pods are the preview's pods with their reasons.
func (a *App) renderPreviewDiagnosis(d *previewDiagnosis, c *candidate, pods []map[string]any) error {
	if err := a.renderFindings(d.Findings); err != nil {
		return err
	}
	preview := d.Preview
	state := text(preview, "status")
	if message := text(preview, "error_message"); message != "" {
		state += ": " + message
	}
	branch := text(preview, "head_ref")
	if base := text(preview, "base_ref"); branch != "" && base != "" {
		branch += " → " + base
	}
	fields := [][2]string{
		{"Preview", strings.TrimSpace("#" + previewNumber(preview) + " " + text(preview, "pr_title"))},
		{"Deployment", c.Name},
		{"Cluster", text(c.Record, "cluster_name")},
		{"Status", state},
		{"URL", previewAddress(preview)},
		{"Branch", branch},
		{"Commit", shortSHA(text(preview, "head_sha"))},
		{"Namespace", first(text(d.Runtime, "namespace"), text(preview, "namespace"))},
		{"Image", text(d.Runtime, "running_image")},
	}
	if d.Runtime != nil {
		fields = append(fields, [2]string{"Rollout", previewRollout(d.Runtime, pods)}, [2]string{"Replicas", replicaCounts(d.Runtime)})
	}
	if err := ui.Fields(a.Out, fields, a.color); err != nil {
		return err
	}
	if err := a.renderEvidence(pods, d.Events, d.Logs); err != nil {
		return err
	}
	a.reportUnread(d.Unread)
	return nil
}
