package cli

import (
	"fmt"
	"github.com/spf13/cobra"
)

func (a *App) addLifecycle(root *cobra.Command) {
	for _, action := range []string{"restart", "scale", "rollback"} {
		cmd := a.lifecycleCommand(action)
		cmd.GroupID = "work"
		root.AddCommand(cmd)
	}
}

// lifecycleCommand builds restart, scale or rollback for a deployment.
func (a *App) lifecycleCommand(action string) *cobra.Command {
	var replicas, generation int
	cmd := &cobra.Command{Use: action + " [deployment]", Short: map[string]string{"restart": "Restart a deployment's pods", "scale": "Set a deployment's replica count", "rollback": "Restore a previous deployment revision"}[action], Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if action == "scale" && (replicas < 0 || replicas > 100) {
			return fmt.Errorf("replicas must be between 0 and 100")
		}
		if action == "rollback" && generation < 1 {
			return fmt.Errorf("generation must be positive")
		}
		c, err := a.resolveDeployment(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		id, err := safeID(c.ID)
		if err != nil {
			return err
		}
		deployment := fmt.Sprintf("deployment %s in cluster %s", c.Name, text(c.Record, "cluster_name"))
		method := "POST"
		var body []byte
		switch action {
		case "scale":
			if replicas == 0 {
				if err := a.confirm("Scale " + deployment + " to zero replicas"); err != nil {
					return err
				}
			}
			method = "PATCH"
			body = jsonBody(map[string]int{"replicas": replicas})
		case "rollback":
			if err := a.confirm(fmt.Sprintf("Roll back %s to generation %d", deployment, generation)); err != nil {
				return err
			}
			body = jsonBody(map[string]int{"generation": generation})
		}
		response, err := a.request(cmd.Context(), method, "/api/deployments/"+id+"/"+action, nil, body)
		if err != nil {
			return err
		}
		return a.render(response)
	}}
	if action == "scale" {
		cmd.Flags().IntVar(&replicas, "replicas", -1, "Desired replica count (0–100)")
		_ = cmd.MarkFlagRequired("replicas")
	}
	if action == "rollback" {
		cmd.Flags().IntVar(&generation, "generation", 0, "Revision generation to restore")
		_ = cmd.MarkFlagRequired("generation")
	}
	a.completes(a.deploymentChoices, cmd)
	return cmd
}
