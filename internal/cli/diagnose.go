package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// finding is one conclusion of a diagnosis, with the commands that follow from it.
type finding struct {
	// Problem is false for a note, such as a rollout that is still running.
	Problem bool     `json:"problem"`
	Summary string   `json:"summary"`
	Detail  string   `json:"detail,omitempty"`
	Next    []string `json:"next,omitempty"`
}

// unread is a source the diagnosis could not read, and the error it got.
type unread struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

// podLogs is the end of one pod's log.
type podLogs struct {
	Pod       string `json:"pod"`
	Container string `json:"container,omitempty"`
	// Previous is true for the log of the container before its last restart.
	Previous bool   `json:"previous"`
	Text     string `json:"text"`
}

// diagnosis is what `edka diagnose` read about a deployment and concluded.
type diagnosis struct {
	Healthy    bool             `json:"healthy"`
	Findings   []finding        `json:"findings"`
	Deployment map[string]any   `json:"deployment"`
	Runtime    map[string]any   `json:"runtime"`
	Revisions  []map[string]any `json:"revisions"`
	Events     []map[string]any `json:"events"`
	Build      map[string]any   `json:"build,omitempty"`
	Logs       *podLogs         `json:"logs"`
	Unread     []unread         `json:"unread"`
	// cluster names the deployment's cluster. Edka's record of a deployment
	// names it by ID, and the list of deployments by name.
	cluster string
}

// A container in one of these states has not started, so it has no log.
var notStartedReasons = []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ErrImageNeverPull", "CreateContainerConfigError", "CreateContainerError", "ContainerCreating", "PodInitializing", "Unschedulable"}

func (a *App) addDiagnose(root *cobra.Command) {
	cmd := a.diagnoseCommand(true)
	cmd.GroupID = "work"
	root.AddCommand(cmd)
}

// diagnoseCommand reports why a deployment is not running as configured. With
// fallback, a command that names no deployment and has none in context
// diagnoses the context cluster, as `edka status` shows it.
func (a *App) diagnoseCommand(fallback bool) *cobra.Command {
	var tail int
	short, long, example := "Find out why a deployment is not running", "", "  edka diagnose\n  edka diagnose api\n  edka diagnose api --tail 100\n  edka diagnose api --json"
	if fallback {
		short = "Find out why a deployment or cluster is not working"
		long = "\n\nWithout a deployment in the link, in --deployment or as an argument, the\ncommand diagnoses the cluster, as `edka clusters diagnose` does. The other\nkinds have their own: `edka apps diagnose`, `edka previews diagnose`,\n`edka databases diagnose` and `edka cronjobs diagnose`."
		example += "\n  edka diagnose --cluster production"
	}
	cmd := &cobra.Command{Use: "diagnose [deployment]", Short: short, Long: "Read a deployment's rollout, pods, Kubernetes events, last revisions and the log\nof its failing pod, and report what is wrong with the commands to run next. A\nGit deployment's last build is read too. The command changes nothing.\n\nThe findings come first, then what they were read from. A source that can't be\nread is named and the rest is still reported. The command exits unsuccessfully\nwhen it finds a problem. With --json, stdout has `healthy`, `findings` and\neverything that was read." + long, Args: cobra.MaximumNArgs(1), Example: example, RunE: func(cmd *cobra.Command, args []string) error {
		if tail < 1 || tail > 2000 {
			return fmt.Errorf("tail must be between 1 and 2000")
		}
		if fallback && len(args) == 0 && a.deployment == "" {
			return a.runClusterDiagnosis(cmd.Context(), "")
		}
		c, err := a.resolveDeployment(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		d, err := a.diagnose(cmd.Context(), c, tail)
		if err != nil {
			return err
		}
		return a.finishDiagnosis(c.Name, d, d.Findings, func() error { return a.renderDiagnosis(d) })
	}}
	cmd.Flags().IntVarP(&tail, "tail", "n", 30, "Number of log lines of the failing pod (at most 2000)")
	a.completes(a.deploymentChoices, cmd)
	return cmd
}

// missed names a source a diagnosis could not read, with the first line of
// the error it got.
func missed(list *[]unread, source string, err error) {
	reason, _, _ := strings.Cut(err.Error(), "\n")
	*list = append(*list, unread{Source: source, Error: ui.Clean(reason)})
}

// problems counts the findings that are problems.
func problems(found []finding) int {
	count := 0
	for _, f := range found {
		if f.Problem {
			count++
		}
	}
	return count
}

// finishDiagnosis prints a diagnosis, as JSON or with render, and fails when
// it found a problem. name is what was diagnosed.
func (a *App) finishDiagnosis(name string, report any, found []finding, render func() error) error {
	if a.output != "table" {
		if err := ui.Render(a.Out, jsonBody(report), a.output, a.color); err != nil {
			return err
		}
	} else if err := render(); err != nil {
		return err
	}
	switch count := problems(found); count {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%s has a problem; see the finding above", ui.Clean(name))
	default:
		return fmt.Errorf("%s has %d problems; see the findings above", ui.Clean(name), count)
	}
}

// diagnose reads what Edka and the cluster know about a deployment. Only the
// deployment itself has to be readable: every other source that fails is named
// in Unread, since a diagnosis is for a deployment that does not work.
func (a *App) diagnose(ctx context.Context, c *candidate, tail int) (*diagnosis, error) {
	id, err := safeID(c.ID)
	if err != nil {
		return nil, err
	}
	response, err := a.request(ctx, "GET", "/api/deployments/"+id, nil, nil)
	if err != nil {
		return nil, err
	}
	record, err := identityData(response.Body)
	if err != nil {
		return nil, err
	}
	d := &diagnosis{Deployment: record, Findings: []finding{}, Revisions: []map[string]any{}, Events: []map[string]any{}, Unread: []unread{}, cluster: first(text(record, "cluster_name"), text(c.Record, "cluster_name"), text(record, "cluster_id"))}
	name := first(text(record, "name"), c.Name)

	var runtimeErr error
	if response, err := a.request(ctx, "GET", "/api/deployments/"+id+"/status", nil, nil); err != nil {
		runtimeErr = err
		missed(&d.Unread, "runtime status", err)
	} else if d.Runtime, err = identityData(response.Body); err != nil {
		runtimeErr = err
		missed(&d.Unread, "runtime status", err)
	}
	if rows, err := a.objectsQuery(ctx, "/api/deployments/"+id+"/revisions", url.Values{"limit": {"5"}}); err != nil {
		missed(&d.Unread, "revisions", err)
	} else {
		d.Revisions = rows
	}
	if events, err := a.workloadEvents(ctx, text(record, "cluster_id"), "deployments", name, text(record, "namespace")); err != nil {
		missed(&d.Unread, "events", err)
	} else {
		d.Events = events
	}
	if git, _ := gitSource(record); git {
		if rows, err := a.objectsQuery(ctx, "/api/deployments/"+id+"/builds", url.Values{"limit": {"1"}}); err != nil {
			missed(&d.Unread, "builds", err)
		} else if len(rows) > 0 {
			d.Build = rows[0]
		}
	}
	if pod := failingPod(d.Runtime); pod != nil && !slices.Contains(notStartedReasons, text(pod, "reason")) {
		if logs, err := a.podLogs(ctx, "/api/deployments/"+id+"/logs", pod, tail); err != nil {
			missed(&d.Unread, "logs of "+text(pod, "name"), err)
		} else {
			d.Logs = logs
		}
	}

	d.Findings = findings(d, name, runtimeErr)
	d.Healthy = problems(d.Findings) == 0
	return d, nil
}

// workloadEvents reads the Kubernetes events of a workload and of what it
// owns, newest first: the ReplicaSets and pods of a Deployment, the Jobs and
// pods of a CronJob. kind is the workload's kind as the explorer names it.
func (a *App) workloadEvents(ctx context.Context, cluster, kind, name, namespace string) ([]map[string]any, error) {
	cluster, err := safeID(cluster)
	if err != nil {
		return nil, fmt.Errorf("its record names no cluster")
	}
	workload, err := safeID(name)
	if err != nil {
		return nil, err
	}
	response, err := a.request(ctx, "GET", "/api/clusters/"+cluster+"/explorer/resources/"+kind+"/"+workload+"/events", url.Values{"namespace": {first(namespace, "default")}}, nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return nil, err
	}
	if body.Events == nil {
		body.Events = []map[string]any{}
	}
	return body.Events, nil
}

// objectsOf lists the records of a list as Edka sends it.
func objectsOf(v any) []map[string]any {
	records := []map[string]any{}
	list, _ := v.([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			records = append(records, m)
		}
	}
	return records
}

// podsOf lists the pods of a runtime status.
func podsOf(runtime map[string]any) []map[string]any { return objectsOf(runtime["pods"]) }

// failingPod is the pod a diagnosis looks at: the failed pod with the most
// restarts, else a pod that is not ready, else nil.
func failingPod(runtime map[string]any) map[string]any {
	var failed, waiting map[string]any
	for _, pod := range podsOf(runtime) {
		ready, _ := pod["ready"].(bool)
		switch {
		case text(pod, "status") == "failed":
			if failed == nil || number(pod["restartCount"]) > number(failed["restartCount"]) {
				failed = pod
			}
		case !ready && waiting == nil:
			waiting = pod
		}
	}
	if failed != nil {
		return failed
	}
	return waiting
}

// podLogs reads the end of a pod's log from the logs route at path. A container
// that restarted wrote what stopped it to the log of the container before, so
// that one is read first.
func (a *App) podLogs(ctx context.Context, path string, pod map[string]any, tail int) (*podLogs, error) {
	read := func(previous bool) (map[string]any, error) {
		query := url.Values{"podName": {text(pod, "name")}, "tailLines": {fmt.Sprint(tail)}}
		// The container Edka names for the pod's state, when it names one.
		if container := text(pod, "container"); container != "" {
			query.Set("container", container)
		}
		if previous {
			query.Set("previous", "true")
		}
		response, err := a.request(ctx, "GET", path, query, nil)
		if err != nil {
			return nil, err
		}
		return identityData(response.Body)
	}
	previous := number(pod["restartCount"]) > 0
	body, err := read(previous)
	if err != nil {
		return nil, err
	}
	// Edka answers with a sentence in place of a log when no container ran before.
	if parameters, _ := body["parameters"].(map[string]any); previous && parameters["noPreviousLogs"] == true {
		previous = false
		if body, err = read(false); err != nil {
			return nil, err
		}
	}
	logs, _ := body["logs"].(string)
	return &podLogs{Pod: first(text(body, "podName"), text(pod, "name")), Container: text(body, "containerName"), Previous: previous, Text: strings.TrimRight(ui.CleanText(logs), "\n")}, nil
}

// workload is what the pods of a diagnosis belong to, with the commands its
// findings name. A kind that has no command for a cause leaves that advice empty.
type workload struct {
	// image is safe to print, and empty when Edka names none.
	image string
	// logs is the command that reads its logs; a finding adds the pod to it.
	logs string
	// memoryLimit is the limit a killed pod exceeded, when Edka names it.
	memoryLimit string
	// Advice by cause: an image name Kubernetes refuses, a pod killed for its
	// memory, a Secret or ConfigMap the cluster lacks, a container that can't
	// start, a failed health check, a pod no node can run, a volume that can't
	// be mounted, and the way back from a change.
	rename, memory, missing, start, health, capacity, volumes, rollback []string
}

// deploymentWorkload is a deployment with the commands that change it.
func deploymentWorkload(name string, record map[string]any) workload {
	config, _ := record["config"].(map[string]any)
	inspect := "edka deployments get " + name + " --json"
	rollback := []string{"edka deployments revisions " + name}
	if healthy := number(record["healthy_generation"]); healthy > 0 && healthy < number(record["spec_generation"]) {
		rollback = []string{"edka rollback " + name + " --generation " + fmt.Sprint(healthy)}
	}
	return workload{
		image:       ui.Clean(imageOf(record)),
		logs:        "edka logs " + name,
		memoryLimit: ui.Clean(first(text(config, "memory_limit"), "512Mi")),
		rename:      []string{"edka up " + name + " --field config.image_repository=<repository> --field config.image_tag=<tag> --wait"},
		memory:      []string{"edka up " + name + " --field config.memory_limit=<limit> --wait"},
		missing:     []string{fmt.Sprintf("edka env --deployment %s", name), fmt.Sprintf("edka env set --secret <NAME> --deployment %s", name)},
		start:       []string{"Check `config.command` and `config.args`: " + inspect},
		health:      []string{"Check `config.health_checks` and `port`: " + inspect},
		capacity:    []string{"edka nodepools list", "Add servers with `edka nodepools scale <pool>`, or lower `config.cpu_request` and `config.memory_request`."},
		volumes:     []string{"Check `config.volumes`: " + inspect},
		rollback:    rollback,
	}
}

// findings concludes from what a diagnosis read. Each rule names what was
// observed, quotes Kubernetes or Edka for the cause, and lists commands.
func findings(d *diagnosis, name string, runtimeErr error) []finding {
	name = ui.Clean(name)
	record := d.Deployment
	w := deploymentWorkload(name, record)
	generation := number(record["spec_generation"])
	found := []finding{}
	add := func(problem bool, summary, detail string, next ...string) {
		found = append(found, finding{Problem: problem, Summary: summary, Detail: ui.Clean(detail), Next: next})
	}

	// The last change, when Edka gave it up.
	failedRollout := false
	if len(d.Revisions) > 0 {
		latest := d.Revisions[0]
		if text(latest, "source") == "auto-rollback" {
			failedRollout = true
			failed, target := number(latest["generation"])-1, number(latest["rollback_of_generation"])
			reason := ""
			for _, revision := range d.Revisions {
				if number(revision["generation"]) == failed {
					reason = text(revision, "status_message")
				}
			}
			next := []string{"edka deployments revisions " + name, "Fix the cause, then deploy the change again."}
			// The rollback is a revision too: it rolls out, and can fail as the change did.
			switch text(latest, "status") {
			case "failed":
				add(true, fmt.Sprintf("Generation %d failed.", failed), reason)
				add(true, fmt.Sprintf("Edka could not roll back to generation %d.", target), first(text(latest, "status_message"), text(record, "status_message")), next...)
			case "pending", "applying":
				add(true, fmt.Sprintf("Generation %d failed, and Edka is rolling back to generation %d.", failed, target), reason, next...)
			default:
				add(true, fmt.Sprintf("Generation %d failed, and Edka rolled back to generation %d.", failed, target), reason, next...)
			}
		}
	}
	if status := text(record, "status"); !failedRollout && (status == "failed" || status == "error") {
		add(true, fmt.Sprintf("Generation %d failed.", generation), text(record, "status_message"))
	}

	// What runs in the cluster.
	explained := map[string]bool{}
	if runtimeErr != nil {
		reason, _, _ := strings.Cut(runtimeErr.Error(), "\n")
		add(true, "Edka could not read the deployment from its cluster.", reason,
			"edka clusters diagnose "+ui.Clean(d.cluster))
	} else {
		for _, pod := range podsOf(d.Runtime) {
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
	}

	// Events explain a pod that Kubernetes gives no reason for.
	if d.Runtime != nil && text(d.Runtime, "status") != "deployed" {
		eventFindings(add, w, d.Events, d.Runtime, explained)
	}

	if d.Runtime != nil {
		replicas, _ := d.Runtime["replicas"].(map[string]any)
		status := text(d.Runtime, "status")
		switch {
		case number(replicas["desired"]) == 0 && replicas != nil:
			add(false, "The deployment is scaled to zero replicas.", "", "edka scale "+name+" --replicas 1")
		case status == "failed" && len(found) == 0:
			add(true, "The rollout failed.", text(d.Runtime, "message"), "edka logs "+name)
		case status == "pending" && !slices.ContainsFunc(found, func(f finding) bool { return f.Problem }):
			add(false, "The rollout is still running.", text(d.Runtime, "message"), "edka deployments status "+name)
		}
		if mismatch, _ := d.Runtime["version_mismatch"].(bool); mismatch && status == "deployed" {
			add(false, fmt.Sprintf("The pods run %s, and the deployment is configured with %s.", text(d.Runtime, "running_image"), text(d.Runtime, "configured_image")), "")
		}
	}
	if applied := number(record["applied_generation"]); generation > applied && record["applied_generation"] != nil && !failedRollout {
		add(false, fmt.Sprintf("Generation %d is not applied to the cluster yet; generation %d is.", generation, applied), text(record, "status_message"))
	}

	if status := text(d.Build, "status"); status == "failed" || status == "cancelled" {
		add(true, fmt.Sprintf("The last build %s.", map[string]string{"failed": "failed", "cancelled": "was cancelled"}[status]), text(d.Build, "error_message"),
			"edka build "+name+" --wait")
	}

	if len(found) == 0 {
		healthy := "No problem found."
		if replicas, ok := d.Runtime["replicas"].(map[string]any); ok {
			healthy = fmt.Sprintf("No problem found. %d of %d pods are ready on generation %d.", number(replicas["ready"]), number(replicas["desired"]), generation)
		}
		add(false, healthy, "")
	}
	return found
}

// podFinding explains a pod that is not ready from the reason Kubernetes gives.
func podFinding(add func(bool, string, string, ...string), w workload, pod map[string]any) {
	podName, reason, message := ui.Clean(text(pod, "name")), text(pod, "reason"), text(pod, "message")
	restarts := number(pod["restartCount"])
	// The pod's log is the one of the container Edka names for its state, when
	// it names one.
	logs := w.logs + " --pod " + podName
	if container := ui.Clean(text(pod, "container")); container != "" {
		logs += " --container " + shellJoin([]string{container})
	}
	// named ends a summary with a value Edka may not name.
	named := func(summary, separator, value string) string {
		if value == "" {
			return summary + "."
		}
		return summary + separator + value + "."
	}
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "ErrImageNeverPull":
		add(true, named(fmt.Sprintf("Pod %s can't pull its image", podName), " ", w.image), message,
			"Check that the image and its tag exist in the registry.",
			"edka registries list",
			"edka registries apply <registry>")
	case "InvalidImageName":
		add(true, named(fmt.Sprintf("Pod %s has an image name that Kubernetes refuses", podName), ": ", w.image), message, w.rename...)
	case "OOMKilled":
		add(true, named(fmt.Sprintf("Pod %s was killed for using more memory than its limit", podName), " of ", w.memoryLimit), message,
			slices.Concat(w.memory, w.rollback)...)
	case "CreateContainerConfigError":
		add(true, fmt.Sprintf("Pod %s can't start, because its configuration names something the cluster lacks.", podName), message, w.missing...)
	case "Unschedulable":
		add(true, fmt.Sprintf("No node can run Pod %s.", podName), message, w.capacity...)
	case "CreateContainerError", "RunContainerError":
		add(true, fmt.Sprintf("Pod %s has a container that Kubernetes can't start.", podName), message, w.start...)
	case "CrashLoopBackOff", "Error", "Failed":
		summary := fmt.Sprintf("Pod %s starts and exits.", podName)
		if restarts > 0 {
			summary = fmt.Sprintf("Pod %s starts and exits, %d times so far.", podName, restarts)
		}
		add(true, summary, first(message, reason),
			slices.Concat([]string{"Read what it printed last, under Logs.", logs + " --previous --tail 200"}, w.rollback)...)
	default:
		if text(pod, "status") == "failed" {
			add(true, fmt.Sprintf("Pod %s failed: %s.", podName, ui.Clean(reason)), message, logs)
		}
	}
}

// eventFindings explains what the pods' reasons leave open, from the warnings
// Kubernetes recorded. One finding per reason, from its newest event. A health
// check that fails while a rollout still runs may pass once the container is
// up, so it is a problem only when the rollout failed.
//
// The events of a deployment include those of the ReplicaSets it keeps from
// earlier rollouts and of pods that are ready by now. A warning counts only
// while what it names still fails.
func eventFindings(add func(bool, string, string, ...string), w workload, events []map[string]any, runtime map[string]any, explained map[string]bool) {
	failed := text(runtime, "status") == "failed"
	waiting := map[string]bool{}
	for _, pod := range podsOf(runtime) {
		if ready, _ := pod["ready"].(bool); !ready {
			waiting[text(pod, "name")] = true
		}
	}
	for _, event := range events {
		reason := text(event, "reason")
		if text(event, "type") != "Warning" || explained[reason] {
			continue
		}
		object, _ := event["object"].(map[string]any)
		switch text(object, "kind") {
		case "Pod":
			if !waiting[text(object, "name")] {
				continue
			}
		case "ReplicaSet":
			if replicaFailure(runtime) == nil {
				continue
			}
		}
		subject := strings.TrimSpace(text(object, "kind") + " " + ui.Clean(text(object, "name")))
		message := text(event, "message")
		switch reason {
		case "FailedScheduling":
			add(true, fmt.Sprintf("No node can run %s.", subject), message, w.capacity...)
		case "Unhealthy":
			// A crash loop fails its probes too, and is explained already.
			if explained["CrashLoopBackOff"] || explained["Error"] {
				continue
			}
			add(failed, fmt.Sprintf("%s fails its health check.", subject), message, slices.Concat(w.health, []string{w.logs})...)
		case "FailedMount", "FailedAttachVolume":
			add(true, fmt.Sprintf("%s can't mount a volume.", subject), message, w.volumes...)
		case "FailedCreate":
			add(true, fmt.Sprintf("%s can't create its pods.", subject), message)
		default:
			continue
		}
		explained[reason] = true
	}
	// The condition says why when no event of the ReplicaSet was read.
	if condition := replicaFailure(runtime); condition != nil && !explained["FailedCreate"] {
		add(true, "The rollout can't create its pods.", strings.Trim(text(condition, "reason")+": "+text(condition, "message"), ": "))
	}
}

// replicaFailure is the condition of a deployment whose ReplicaSet can't create
// its pods now, or nil. Kubernetes sets the condition on the Deployment while
// that lasts, and removes it once the pods are created.
func replicaFailure(runtime map[string]any) map[string]any {
	for _, condition := range objectsOf(runtime["conditions"]) {
		if text(condition, "type") == "ReplicaFailure" && text(condition, "status") == "True" {
			return condition
		}
	}
	return nil
}

// renderFindings prints each finding with its detail and the commands to run next.
func (a *App) renderFindings(found []finding) error {
	for _, f := range found {
		mark := "✗"
		if !f.Problem {
			mark = "•"
		}
		if _, err := fmt.Fprintf(a.Out, "%s %s\n", mark, f.Summary); err != nil {
			return err
		}
		if f.Detail != "" {
			fmt.Fprintf(a.Out, "  %s\n", f.Detail)
		}
		for _, next := range f.Next {
			fmt.Fprintf(a.Out, "  Next: %s\n", next)
		}
	}
	_, err := fmt.Fprintln(a.Out)
	return err
}

// renderEvidence prints what a workload's findings were read from, below its
// fields: the pods, the ten newest warnings and the log of the failing pod.
func (a *App) renderEvidence(pods, events []map[string]any, logs *podLogs) error {
	if len(pods) > 0 {
		fmt.Fprintln(a.Out)
		if err := ui.Table(a.Out, []ui.Column{ui.Field("POD", "name"), ui.Field("STATUS", "status"), ui.Field("READY", "ready"), ui.Field("RESTARTS", "restartCount"), ui.Field("REASON", "reason")}, pods, a.color); err != nil {
			return err
		}
	}
	warnings := []map[string]any{}
	for _, event := range events {
		if text(event, "type") == "Warning" {
			warnings = append(warnings, event)
		}
	}
	if len(warnings) > 0 {
		fmt.Fprintln(a.Out)
		if err := ui.Table(a.Out, []ui.Column{
			{Header: "WARNING", Value: func(m map[string]any) string { return text(m, "reason") }},
			{Header: "LAST SEEN", Value: func(m map[string]any) string { return when(m["lastSeen"]) }},
			ui.Field("COUNT", "count"),
			{Header: "OBJECT", Value: func(m map[string]any) string {
				object, _ := m["object"].(map[string]any)
				return strings.TrimSpace(text(object, "kind") + " " + text(object, "name"))
			}},
			ui.Field("MESSAGE", "message"),
		}, warnings[:min(len(warnings), 10)], a.color); err != nil {
			return err
		}
	}
	if logs != nil && logs.Text != "" {
		heading := "Logs of " + ui.Clean(logs.Pod)
		if logs.Previous {
			heading += ", from the container before its last restart"
		}
		fmt.Fprintf(a.Out, "\n%s\n%s\n", ui.Paint(heading, a.color), logs.Text)
	}
	return nil
}

// reportUnread names on stderr the sources a diagnosis could not read.
func (a *App) reportUnread(list []unread) {
	for _, source := range list {
		a.message("Could not read the %s: %s", source.Source, source.Error)
	}
}

// renderDiagnosis prints the findings, then what they were read from.
func (a *App) renderDiagnosis(d *diagnosis) error {
	record := d.Deployment
	name := first(text(record, "name"), text(record, "id"))
	if err := a.renderFindings(d.Findings); err != nil {
		return err
	}
	status := text(record, "status")
	if message := text(record, "status_message"); message != "" {
		status += ": " + message
	}
	revision := ""
	if record["spec_generation"] != nil {
		revision = fmt.Sprintf("generation %d (applied %d, healthy %d)", number(record["spec_generation"]), number(record["applied_generation"]), number(record["healthy_generation"]))
	}
	fields := [][2]string{
		{"Deployment", name},
		{"Cluster", d.cluster},
		{"Namespace", text(record, "namespace")},
		{"Status", status},
		{"Image", imageOf(record)},
		{"Revision", revision},
	}
	if d.Runtime != nil {
		rollout := text(d.Runtime, "status")
		if message := text(d.Runtime, "message"); message != "" {
			rollout += ": " + message
		}
		fields = append(fields, [2]string{"Rollout", rollout}, [2]string{"Replicas", replicaCounts(d.Runtime)})
	}
	if d.Build != nil {
		fields = append(fields, [2]string{"Last build", strings.TrimSpace(text(d.Build, "status") + " " + shortSHA(text(d.Build, "commit_sha")))})
	}
	if err := ui.Fields(a.Out, fields, a.color); err != nil {
		return err
	}
	if err := a.renderEvidence(podsOf(d.Runtime), d.Events, d.Logs); err != nil {
		return err
	}
	a.reportUnread(d.Unread)
	return nil
}

// replicaCounts is the replicas line of a runtime status, or empty when it has none.
func replicaCounts(runtime map[string]any) string {
	replicas, ok := runtime["replicas"].(map[string]any)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d/%d ready, %d updated, %d available", number(replicas["ready"]), number(replicas["desired"]), number(replicas["updated"]), number(replicas["available"]))
}
