package cli

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

func imageOf(m map[string]any) string {
	repository := text(m, "image_repository")
	if repository == "" {
		return ""
	}
	return repository + ":" + first(text(m, "image_tag"), "latest")
}
func replicasOf(m map[string]any) string {
	if enabled, _ := m["autoscale_enabled"].(bool); enabled {
		return fmt.Sprintf("%d–%d auto", number(m["min_replicas"]), number(m["max_replicas"]))
	}
	if m["replicas"] == nil {
		return ""
	}
	return fmt.Sprint(number(m["replicas"]))
}
func hostsOf(m map[string]any) string {
	hosts := []string{}
	if list, ok := m["hostnames"].([]any); ok {
		for _, host := range list {
			if s, ok := host.(string); ok && s != "" {
				hosts = append(hosts, s)
			}
		}
	}
	if len(hosts) == 0 && text(m, "hostname") != "" {
		hosts = append(hosts, text(m, "hostname"))
	}
	return strings.Join(hosts, ", ")
}

var deploymentColumns = []ui.Column{
	ui.Field("NAME", "name"),
	ui.Field("STATUS", "status"),
	{Header: "IMAGE", Value: imageOf},
	{Header: "REPLICAS", Value: replicasOf},
	ui.Field("CLUSTER", "cluster_name"),
	{Header: "HOSTS", Value: hostsOf},
}

// renderDeploymentStatus shows runtime status and pods; JSON keeps the response.
func (a *App) renderDeploymentStatus(response *api.Response) error {
	if a.output != "table" {
		return a.render(response)
	}
	m, err := identityData(response.Body)
	if err != nil {
		return err
	}
	replicas, _ := m["replicas"].(map[string]any)
	image := text(m, "running_image")
	if mismatch, _ := m["version_mismatch"].(bool); mismatch {
		image += " (configured " + text(m, "configured_image") + ")"
	}
	fields := [][2]string{
		{"Name", text(m, "name")},
		{"Status", text(m, "status")},
		{"Message", text(m, "message")},
		{"Namespace", text(m, "namespace")},
		{"Image", image},
	}
	if replicas != nil {
		fields = append(fields, [2]string{"Replicas", fmt.Sprintf("%d/%d ready, %d updated, %d available", number(replicas["ready"]), number(replicas["desired"]), number(replicas["updated"]), number(replicas["available"]))})
	}
	if err := ui.Fields(a.Out, fields, a.color); err != nil {
		return err
	}
	pods := []map[string]any{}
	if list, ok := m["pods"].([]any); ok {
		for _, pod := range list {
			if p, ok := pod.(map[string]any); ok {
				pods = append(pods, p)
			}
		}
	}
	if len(pods) == 0 {
		return nil
	}
	fmt.Fprintln(a.Out)
	return ui.Table(a.Out, []ui.Column{ui.Field("POD", "name"), ui.Field("STATUS", "status"), ui.Field("READY", "ready"), ui.Field("RESTARTS", "restartCount"), ui.Field("REASON", "reason")}, pods, a.color)
}

func (a *App) addDeployments(root *cobra.Command) {
	deployments := &cobra.Command{Use: "deployments", Aliases: []string{"deployment"}, Short: "Inspect and manage deployments", GroupID: "resources", Example: "  edka deployments list\n  edka deployments create --data @deployment.json --wait\n  edka deployments status api\n  edka deployments restart api\n  edka deployments rollback api --generation 4"}
	a.strictGroup(deployments)
	var all bool
	list := &cobra.Command{Use: "list", Short: "List deployments", Long: "List deployments in the linked or selected cluster, or in every cluster when\nnone is linked. Use --all to include every cluster.", Args: cobra.NoArgs, Example: "  edka deployments list\n  edka deployments list --all --json", RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.deploymentRows(cmd.Context(), all)
		if err != nil {
			return err
		}
		return a.renderRows(rows, deploymentColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List deployments in every cluster, not only the linked one")
	get := &cobra.Command{Use: "get [deployment]", Short: "Show a deployment's configuration", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolveDeployment(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		id, err := safeID(c.ID)
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "GET", "/api/deployments/"+id, nil, nil)
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
		git := ""
		if text(m, "github_repository_full_name") != "" {
			git = sourceOf(m)
		}
		revision := ""
		if m["spec_generation"] != nil {
			revision = fmt.Sprintf("generation %d (applied %d, healthy %d)", number(m["spec_generation"]), number(m["applied_generation"]), number(m["healthy_generation"]))
		}
		return ui.Fields(a.Out, [][2]string{
			{"Name", text(m, "name")},
			{"Status", text(m, "status")},
			{"Message", text(m, "status_message")},
			{"Cluster", first(text(m, "cluster_name"), text(c.Record, "cluster_name"))},
			{"Namespace", text(m, "namespace")},
			{"Image", imageOf(m)},
			{"Git", git},
			{"Replicas", replicasOf(m)},
			{"Hosts", hostsOf(m)},
			{"Revision", revision},
			{"Updated", when(m["updated_at"])},
			{"ID", c.ID},
		}, a.color)
	}}
	status := &cobra.Command{Use: "status [deployment]", Short: "Show runtime status, replicas and pods", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := a.deploymentID(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "GET", "/api/deployments/"+id+"/status", nil, nil)
		if err != nil {
			return err
		}
		return a.renderDeploymentStatus(response)
	}}
	var limit int
	revisions := &cobra.Command{Use: "revisions [deployment]", Short: "List revisions to roll back to", Args: cobra.MaximumNArgs(1), Example: "  edka deployments revisions api\n  edka deployments rollback api --generation 4", RunE: func(cmd *cobra.Command, args []string) error {
		if limit < 1 {
			return fmt.Errorf("limit must be positive")
		}
		id, err := a.deploymentID(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "GET", "/api/deployments/"+id+"/revisions", url.Values{"limit": {fmt.Sprint(limit)}}, nil)
		if err != nil {
			return err
		}
		if a.output != "table" {
			return a.render(response)
		}
		data, err := api.Data(response.Body)
		if err != nil {
			return err
		}
		rows := []map[string]any{}
		if list, ok := data.([]any); ok {
			for _, row := range list {
				if m, ok := row.(map[string]any); ok {
					rows = append(rows, m)
				}
			}
		}
		return ui.Table(a.Out, []ui.Column{
			ui.Field("GENERATION", "generation"),
			ui.Field("STATUS", "status"),
			{Header: "IMAGE", Value: imageOf},
			ui.Field("SOURCE", "source"),
			{Header: "BY", Value: func(m map[string]any) string { return first(text(m, "actor_name"), text(m, "actor_label")) }},
			{Header: "CREATED", Value: func(m map[string]any) string { return when(m["created_at"]) }},
		}, rows, a.color)
	}}
	revisions.Flags().IntVar(&limit, "limit", 20, "Number of revisions")
	remove := &cobra.Command{Use: "delete <deployment>", Short: "Delete a deployment and its Kubernetes resources", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolveDeployment(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		id, err := safeID(c.ID)
		if err != nil {
			return err
		}
		if err := a.confirm(fmt.Sprintf("Delete deployment %s in cluster %s", c.Name, text(c.Record, "cluster_name"))); err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "DELETE", "/api/deployments/"+id, nil, nil)
		if err != nil {
			return err
		}
		return a.render(response)
	}}
	logs := a.logsCommand("logs [deployment]", "Read deployment logs; follow with --follow", a.deploymentLogs)
	a.completes(a.deploymentChoices, get, status, logs, revisions, remove)
	deployments.AddCommand(list, a.createCommand(), get, status, a.diagnoseCommand(false), logs, revisions, a.envCommand(), a.lifecycleCommand("restart"), a.lifecycleCommand("scale"), a.lifecycleCommand("rollback"), a.buildCommand(), remove)
	root.AddCommand(deployments)
}
