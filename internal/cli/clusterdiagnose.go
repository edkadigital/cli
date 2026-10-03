package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// clusterDiagnosis is what `edka clusters diagnose` read about a cluster and
// concluded.
type clusterDiagnosis struct {
	Healthy  bool           `json:"healthy"`
	Findings []finding      `json:"findings"`
	Cluster  map[string]any `json:"cluster"`
	// Activity is the end of Edka's record of a cluster that is not active.
	Activity     []map[string]any `json:"activity"`
	Connectivity map[string]any   `json:"connectivity"`
	// Drift is what Edka's last check of the node pools found.
	Drift       map[string]any   `json:"drift"`
	Deployments []map[string]any `json:"deployments"`
	Pods        []map[string]any `json:"pods"`
	Events      []map[string]any `json:"events"`
	Unread      []unread         `json:"unread"`
}

// podFindings is how many failing pods of a cluster get a finding of their own.
const podFindings = 5

// clusterDiagnoseCommand reports what is wrong with a cluster.
func (a *App) clusterDiagnoseCommand() *cobra.Command {
	return &cobra.Command{Use: "diagnose [cluster]", Short: "Find out what is wrong with a cluster", Long: "Read whether Edka reaches a cluster, what its last check of the node pools\nfound, the status of its deployments, the pods that fail and the warnings\nKubernetes recorded, and report what is wrong with the commands to run next.\nThe command changes nothing.\n\nThe findings come first, then what they were read from. A source that can't be\nread is named and the rest is still reported. The command exits unsuccessfully\nwhen it finds a problem. With --json, stdout has `healthy`, `findings` and\neverything that was read.", Args: cobra.MaximumNArgs(1), Example: "  edka clusters diagnose\n  edka clusters diagnose production\n  edka clusters diagnose production --json", RunE: func(cmd *cobra.Command, args []string) error {
		return a.runClusterDiagnosis(cmd.Context(), argument(args))
	}}
}

// runClusterDiagnosis diagnoses the cluster target names, or the one in context.
func (a *App) runClusterDiagnosis(ctx context.Context, target string) error {
	c, err := a.resolveCluster(ctx, target)
	if err != nil {
		return err
	}
	d, err := a.diagnoseCluster(ctx, c)
	if err != nil {
		return err
	}
	return a.finishDiagnosis(c.Name, d, d.Findings, func() error { return a.renderClusterDiagnosis(d) })
}

// objectsAt returns the records a response holds in the list under key.
func objectsAt(body []byte, key string) ([]map[string]any, error) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	return objectsOf(envelope[key]), nil
}

// diagnoseCluster reads what Edka knows about a cluster and what runs in it.
// Only the cluster itself has to be readable: every other source that fails is
// named in Unread, since a diagnosis is for a cluster that does not work.
func (a *App) diagnoseCluster(ctx context.Context, c *candidate) (*clusterDiagnosis, error) {
	id, err := safeID(c.ID)
	if err != nil {
		return nil, err
	}
	base := "/api/clusters/" + id
	response, err := a.request(ctx, "GET", base, nil, nil)
	if err != nil {
		return nil, err
	}
	cluster, err := record(response.Body, "data")
	if err != nil {
		return nil, err
	}
	d := &clusterDiagnosis{Cluster: withoutSecrets(cluster), Findings: []finding{}, Activity: []map[string]any{}, Deployments: []map[string]any{}, Pods: []map[string]any{}, Events: []map[string]any{}, Unread: []unread{}}
	read := func(source, path string, query url.Values, keep func(body []byte) error) {
		response, err := a.request(ctx, "GET", base+path, query, nil)
		if err == nil {
			err = keep(response.Body)
		}
		if err != nil {
			missed(&d.Unread, source, err)
		}
	}
	if status := text(cluster, "status"); status != "active" && status != "running" {
		read("cluster's activity", "/events", url.Values{"limit": {"5"}}, func(body []byte) (err error) {
			d.Activity, err = rowsOf(base+"/events", body)
			return err
		})
	}
	read("connectivity", "/connectivity", nil, func(body []byte) (err error) {
		d.Connectivity, err = identityData(body)
		return err
	})
	read("check of the node pools", "/drift", nil, func(body []byte) (err error) {
		d.Drift, err = identityData(body)
		return err
	})
	read("deployments", "/deployments/status", nil, func(body []byte) (err error) {
		d.Deployments, err = rowsOf(base+"/deployments/status", body)
		return err
	})
	read("failing pods", "/pods/problematic", nil, func(body []byte) (err error) {
		d.Pods, err = objectsAt(body, "pods")
		return err
	})
	read("events", "/kubernetes-events", nil, func(body []byte) (err error) {
		d.Events, err = objectsAt(body, "events")
		return err
	})
	d.Findings = clusterFindings(d, first(text(cluster, "name"), c.Name))
	d.Healthy = problems(d.Findings) == 0
	return d, nil
}

// clusterChanges says what Edka is doing to a cluster in a status that passes.
var clusterChanges = map[string]string{
	"pending":           "Edka has not started to create the cluster yet.",
	"creating":          "Edka is still creating the cluster.",
	"installing_addons": "Edka is still installing the cluster's add-ons.",
	"updating":          "Edka is still updating the cluster.",
	"upgrading":         "Edka is still upgrading the cluster.",
	"deleting":          "Edka is deleting the cluster.",
}

// clusterFindings concludes from what a cluster diagnosis read. Each rule
// names what was observed, quotes Edka or Kubernetes for the cause, and lists
// commands.
func clusterFindings(d *clusterDiagnosis, name string) []finding {
	name = ui.Clean(name)
	found := []finding{}
	add := func(problem bool, summary, detail string, next ...string) {
		found = append(found, finding{Problem: problem, Summary: summary, Detail: ui.Clean(detail), Next: next})
	}

	// What Edka last did to the cluster, newest first.
	activity := ""
	if len(d.Activity) > 0 {
		activity = text(d.Activity[0], "message")
	}
	switch status := text(d.Cluster, "status"); status {
	case "active", "running", "":
	case "failed", "error":
		add(true, "The cluster failed.", activity, "edka clusters get "+name)
	case "suspended", "archived", "deleted":
		add(true, fmt.Sprintf("The cluster is %s.", status), activity)
	default:
		if change := clusterChanges[status]; change != "" {
			add(false, change, activity)
		} else {
			add(false, fmt.Sprintf("The cluster's status is %s.", ui.Clean(status)), activity)
		}
	}

	// Whether Edka reaches the Kubernetes API, and what it found when it lost it.
	if state := first(text(d.Connectivity, "connectivity_status"), text(d.Cluster, "connectivity_status")); state != "" && state != "connected" {
		incident, _ := d.Connectivity["open_incident"].(map[string]any)
		investigation, _ := incident["investigation"].(map[string]any)
		next := []string{}
		if action := ui.Clean(text(investigation, "suggested_action")); action != "" {
			next = append(next, action)
		}
		summary := "Edka can't reach the cluster's Kubernetes API."
		if state == "disconnected" {
			summary = "Edka can't reach the cluster's Kubernetes API, and marked the cluster disconnected."
		}
		add(true, summary, first(text(investigation, "finding"), text(d.Connectivity, "connectivity_probe_error")), next...)
	}

	pools := []string{fmt.Sprintf("edka nodepools list --cluster %s", name), fmt.Sprintf("edka api clusters drift repair-plan get %s", name)}
	for _, drift := range objectsOf(d.Drift["records"]) {
		if drift["resolved_at"] != nil {
			continue
		}
		summary, detail := driftFinding(drift)
		add(text(drift, "severity") != "info", summary, detail, pools...)
	}

	// A deployment that fails has a diagnosis of its own, which reads its pods.
	failing := map[string]bool{}
	for _, deployment := range d.Deployments {
		deploymentName := ui.Clean(text(deployment, "name"))
		diagnose := fmt.Sprintf("edka diagnose %s --cluster %s", deploymentName, name)
		switch text(deployment, "status") {
		case "failed":
			add(true, fmt.Sprintf("Deployment %s is failing.", deploymentName), text(deployment, "message"), diagnose)
		case "not_found":
			add(true, fmt.Sprintf("Deployment %s is not in the cluster.", deploymentName), "", diagnose)
		default:
			continue
		}
		failing[text(deployment, "namespace")+"/"+text(deployment, "name")] = true
	}

	shown, more := 0, 0
	for _, pod := range d.Pods {
		owner := podDeployment(pod, d.Deployments)
		if owner != nil && failing[text(owner, "namespace")+"/"+text(owner, "name")] {
			continue
		}
		if shown == podFindings {
			more++
			continue
		}
		shown++
		subject := ui.Clean(strings.Trim(text(pod, "namespace")+"/"+text(pod, "name"), "/"))
		summary := fmt.Sprintf("Pod %s is failing.", subject)
		switch restarts := number(pod["restartCount"]); text(pod, "problemType") {
		case "crashing":
			summary = fmt.Sprintf("Pod %s starts and exits.", subject)
			if restarts > 0 {
				summary = fmt.Sprintf("Pod %s starts and exits, %d times so far.", subject, restarts)
			}
		case "pending":
			summary = fmt.Sprintf("Pod %s is pending.", subject)
		case "failed":
			summary = fmt.Sprintf("Pod %s failed.", subject)
		}
		next := []string{}
		if owner != nil {
			next = append(next, fmt.Sprintf("edka diagnose %s --cluster %s", ui.Clean(text(owner, "name")), name))
		}
		add(true, summary, text(pod, "message"), next...)
	}
	switch more {
	case 0:
	case 1:
		add(true, "1 more pod fails; the table below lists it.", "")
	default:
		add(true, fmt.Sprintf("%d more pods fail; the table below lists them.", more), "")
	}

	if len(found) == 0 {
		// A source that was not read may hold a problem.
		if len(d.Unread) > 0 {
			add(false, "No problem found in what could be read.", "")
		} else {
			add(false, "No problem found.", "")
		}
	}
	return found
}

// podDeployment is the deployment a failing pod belongs to, or nil. Kubernetes
// names a Deployment's pods after it, so the pod belongs to the deployment of
// its namespace with the longest name that the pod's name starts with.
func podDeployment(pod map[string]any, deployments []map[string]any) map[string]any {
	var owner map[string]any
	for _, deployment := range deployments {
		name := text(deployment, "name")
		if name == "" || text(deployment, "namespace") != text(pod, "namespace") || !strings.HasPrefix(text(pod, "name"), name+"-") {
			continue
		}
		if owner == nil || len(name) > len(text(owner, "name")) {
			owner = deployment
		}
	}
	return owner
}

// driftSources names what Edka reads to check the node pools.
var driftSources = map[string]string{
	"kubernetes":      "the cluster's nodes",
	"hetzner":         "the servers at Hetzner",
	"edk3s_inventory": "its record of the servers",
}

// driftFinding explains one difference Edka found between a cluster's node
// pools as they are set and the nodes and servers that exist.
func driftFinding(drift map[string]any) (summary, detail string) {
	details, _ := drift["details"].(map[string]any)
	pool := ui.Clean(first(text(details, "poolName"), text(drift, "pool_name")))
	node, server := ui.Clean(text(details, "nodeName")), ui.Clean(first(text(details, "serverName"), text(details, "serverId")))
	live, desired := number(details["liveCount"]), number(details["desiredCount"])
	reported := ""
	if status := text(details, "status"); status != "" {
		reported = "Hetzner reports it " + status
	}
	switch kind := text(drift, "drift_type"); kind {
	case "node_not_ready":
		return fmt.Sprintf("Node %s is not ready.", node), ""
	case "cordoned_stuck_node":
		return fmt.Sprintf("Node %s is cordoned and was not removed.", node), ""
	case "kubernetes_only_node":
		return fmt.Sprintf("Node %s has no server at Hetzner.", node), ""
	case "hetzner_only_server":
		return fmt.Sprintf("Server %s exists at Hetzner and is no node of the cluster.", server), reported
	case "failed_to_join":
		return fmt.Sprintf("Server %s did not join the cluster.", server), reported
	case "partial_delete":
		if node != "" {
			return fmt.Sprintf("Node %s belongs to pool %s, which Edka no longer has.", node, pool), ""
		}
		return fmt.Sprintf("Server %s belongs to pool %s, which Edka no longer has.", server, pool), ""
	case "db_only_pool":
		return fmt.Sprintf("Pool %s is set to %d nodes and has none.", pool, desired), ""
	case "quota_blocked_scale":
		return fmt.Sprintf("Pool %s has %d of %d nodes.", pool, live, desired), first(text(details, "latestOperationError"), text(details, "reason"))
	case "fixed_pool_overprovisioned":
		return fmt.Sprintf("Pool %s has %d nodes and is set to %d.", pool, live, desired), ""
	case "autoscaler_outside_bounds":
		return fmt.Sprintf("Pool %s has %d nodes, outside its autoscaling range of %d to %d.", pool, live, number(details["min"]), number(details["max"])), ""
	case "source_unavailable":
		source := text(details, "source")
		return fmt.Sprintf("Edka could not read %s when it checked the node pools.", first(driftSources[source], ui.Clean(source))), first(text(details, "error"), text(details, "status"))
	default:
		return fmt.Sprintf("The node pools differ from how they are set: %s.", ui.Clean(strings.ReplaceAll(kind, "_", " "))), text(details, "reason")
	}
}

// renderClusterDiagnosis prints the findings, then what they were read from.
func (a *App) renderClusterDiagnosis(d *clusterDiagnosis) error {
	if err := a.renderFindings(d.Findings); err != nil {
		return err
	}
	m := d.Cluster
	connectivity := first(text(d.Connectivity, "connectivity_status"), text(m, "connectivity_status"))
	if since := when(first(text(d.Connectivity, "unreachable_since"), text(m, "unreachable_since"))); since != "" && connectivity != "connected" {
		connectivity += " since " + since
	}
	check := ""
	if summary, ok := d.Drift["summary"].(map[string]any); ok {
		check = text(summary, "status")
		if checked := when(summary["checkedAt"]); checked != "" {
			check += ", checked " + checked
		}
	}
	// Deployments by status, in the order Edka lists the statuses.
	counts, deployments := map[string]int{}, []string{}
	for _, deployment := range d.Deployments {
		counts[text(deployment, "status")]++
	}
	for _, status := range []string{"deployed", "pending", "failed", "not_found"} {
		if counts[status] > 0 {
			deployments = append(deployments, fmt.Sprintf("%d %s", counts[status], strings.ReplaceAll(status, "_", " ")))
		}
	}
	if err := ui.Fields(a.Out, [][2]string{
		{"Cluster", text(m, "name")},
		{"Status", text(m, "status")},
		{"Provider", text(m, "provider")},
		{"Location", text(m, "location")},
		{"Kubernetes", text(m, "k3s_version")},
		{"Connectivity", connectivity},
		{"Workers", workers(m)},
		{"Node pools", check},
		{"Deployments", strings.Join(deployments, ", ")},
	}, a.color); err != nil {
		return err
	}
	if len(d.Pods) > 0 {
		fmt.Fprintln(a.Out)
		if err := ui.Table(a.Out, []ui.Column{ui.Field("NAMESPACE", "namespace"), ui.Field("POD", "name"), ui.Field("PROBLEM", "problemType"), ui.Field("RESTARTS", "restartCount"), ui.Field("MESSAGE", "message")}, d.Pods[:min(len(d.Pods), 20)], a.color); err != nil {
			return err
		}
	}
	warnings := []map[string]any{}
	for _, event := range d.Events {
		if text(event, "type") == "Warning" {
			warnings = append(warnings, event)
		}
	}
	if len(warnings) > 0 {
		fmt.Fprintln(a.Out)
		if err := ui.Table(a.Out, []ui.Column{
			ui.Field("WARNING", "reason"),
			{Header: "LAST SEEN", Value: func(m map[string]any) string { return when(m["lastSeen"]) }},
			ui.Field("COUNT", "occurrenceCount"),
			ui.Field("NAMESPACE", "namespace"),
			{Header: "OBJECT", Value: func(m map[string]any) string {
				return strings.TrimSpace(text(m, "involvedObjectKind") + " " + text(m, "involvedObjectName"))
			}},
			ui.Field("MESSAGE", "message"),
		}, warnings[:min(len(warnings), 10)], a.color); err != nil {
			return err
		}
	}
	a.reportUnread(d.Unread)
	return nil
}
