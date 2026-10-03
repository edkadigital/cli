package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/config"
	"github.com/edkadigital/cli/internal/kubeconfig"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// withoutSecrets drops fields that authenticate to Edka. Cluster rows include
// the Keel webhook token, which Edka's MCP tools also withhold.
func withoutSecrets(m map[string]any) map[string]any {
	delete(m, "keel_webhook_token")
	return m
}

// workers counts ready and desired worker nodes across a cluster's pools.
func workers(m map[string]any) string {
	pools, ok := m["worker_node_pools"].([]any)
	if !ok || len(pools) == 0 {
		return ""
	}
	ready, desired := 0, 0
	for _, pool := range pools {
		if p, ok := pool.(map[string]any); ok {
			ready += number(p["actual_count"])
			desired += number(p["instance_count"])
		}
	}
	return fmt.Sprintf("%d/%d", ready, desired)
}
func yesNo(v any) string {
	if b, _ := v.(bool); b {
		return "yes"
	}
	return "no"
}

var clusterColumns = []ui.Column{
	ui.Field("NAME", "name"),
	ui.Field("STATUS", "status"),
	ui.Field("PROVIDER", "provider"),
	ui.Field("LOCATION", "location"),
	ui.Field("VERSION", "k3s_version"),
	{Header: "WORKERS", Value: workers},
}

var nodePoolColumns = []ui.Column{
	ui.Field("POOL", "name"),
	ui.Field("TYPE", "instance_type"),
	{Header: "NODES", Value: func(m map[string]any) string {
		return fmt.Sprintf("%d/%d", number(m["actual_count"]), number(m["instance_count"]))
	}},
	ui.Field("LOCATION", "location"),
	{Header: "AUTOSCALING", Value: func(m map[string]any) string {
		if enabled, _ := m["autoscaling_enabled"].(bool); !enabled {
			return "off"
		}
		return fmt.Sprintf("%d–%d", number(m["autoscaling_min_instances"]), number(m["autoscaling_max_instances"]))
	}},
}

// showCluster reads one cluster and prints its summary and node pools.
func (a *App) showCluster(ctx context.Context, id string) error {
	response, err := a.request(ctx, "GET", "/api/clusters/"+id, nil, nil)
	if err != nil {
		return err
	}
	m, err := record(response.Body, "data")
	if err != nil {
		return err
	}
	withoutSecrets(m)
	if a.output != "table" {
		return ui.Render(a.Out, jsonBody(map[string]any{"data": m}), a.output, false)
	}
	controlPlane := ""
	if count := number(m["master_instance_count"]); count > 0 {
		controlPlane = fmt.Sprintf("%d × %s", count, first(text(m, "master_instance_type"), "node"))
		if ha, _ := m["master_ha"].(bool); ha {
			controlPlane += " (HA)"
		}
	}
	connectivity := text(m, "connectivity_status")
	if since := when(m["unreachable_since"]); since != "" && connectivity != "connected" {
		connectivity += " since " + since
	}
	price := ""
	if m["monthly_price"] != nil {
		price = strings.TrimSpace(ui.Text(m["monthly_price"]) + " " + text(m, "pricing_currency") + " per month")
	}
	if err := ui.Fields(a.Out, [][2]string{
		{"Name", text(m, "name")},
		{"Status", text(m, "status")},
		{"Provider", text(m, "provider")},
		{"Location", text(m, "location")},
		{"Kubernetes", text(m, "k3s_version")},
		{"API endpoint", first(text(m, "apiEndpoint"), text(m, "api_endpoint"))},
		{"Connectivity", connectivity},
		{"Control plane", controlPlane},
		{"Protected", yesNo(m["protected"])},
		{"Price", price},
		{"Created", when(m["date_created"])},
		{"ID", text(m, "id")},
	}, a.color); err != nil {
		return err
	}
	pools := []map[string]any{}
	if list, ok := m["worker_node_pools"].([]any); ok {
		for _, pool := range list {
			if p, ok := pool.(map[string]any); ok {
				pools = append(pools, p)
			}
		}
	}
	if len(pools) == 0 {
		return nil
	}
	fmt.Fprintln(a.Out)
	return ui.Table(a.Out, nodePoolColumns, pools, a.color)
}

// checkWritable proves a new private file can be created at path before a
// one-time credential is spent on it.
func checkWritable(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists; choose a new path so no kubeconfig is overwritten", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return checkDirWritable(filepath.Dir(path))
}

func checkDirWritable(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".edka-check-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	_ = f.Close()
	return os.Remove(f.Name())
}

// checkMergeTarget proves a kubeconfig can take the merged entries before a
// one-time credential is spent on it.
func checkMergeTarget(path, name string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := kubeconfig.Check(existing, name); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return checkDirWritable(filepath.Dir(path))
}

// kubeconfigName names a cluster's merged kubeconfig entries after the
// organization, or the start of its ID when the profile has no name for it.
func (a *App) kubeconfigName(cluster string) string {
	organization := a.current.OrganizationName
	if organization == "" && len(a.organization) >= 8 {
		organization = a.organization[:8]
	}
	return kubeconfig.Name(organization, cluster)
}

// mergeKubeconfig merges a downloaded kubeconfig into target as name. When the
// merge fails, the one-time kubeconfig is saved to a new private file instead.
func (a *App) mergeKubeconfig(target, name, clusterID, clusterName string, downloaded []byte, use bool) error {
	var contexts []string
	backup, err := kubeconfig.Update(target, func(existing []byte) ([]byte, error) {
		merged, names, err := kubeconfig.Merge(existing, downloaded, kubeconfig.Options{Name: name, ClusterName: clusterName, ClusterID: clusterID, OrganizationID: a.organization, Use: use})
		contexts = names
		return merged, err
	})
	if err != nil {
		kept, keepErr := keepKubeconfig(filepath.Dir(target), name, downloaded)
		if keepErr != nil {
			return fmt.Errorf("the kubeconfig was issued but could not be merged into %s: %w\nIt could not be saved either (%v); rotate to get a new one", target, err, keepErr)
		}
		return fmt.Errorf("the kubeconfig was issued but could not be merged into %s: %w\nIt was saved to %s instead", target, err, kept)
	}
	lines := []string{fmt.Sprintf("✓ Merged your kubeconfig for %s into %s as context %s", ui.Clean(clusterName), target, contexts[0])}
	if len(contexts) > 1 {
		lines = append(lines, "  Contexts for the other API endpoints: "+strings.Join(contexts[1:], ", "))
	}
	if use {
		lines = append(lines, "  kubectl now uses "+contexts[0])
	} else {
		lines = append(lines, "  Next: kubectl --context "+contexts[0]+" get nodes")
	}
	if backup != "" {
		lines = append(lines, "  The previous file is in "+backup)
	}
	a.message("%s", strings.Join(lines, "\n"))
	return nil
}

// keepKubeconfig saves a kubeconfig that was issued but couldn't be merged.
func keepKubeconfig(dir, name string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, name+"-*.yaml")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return path, err
}

func (a *App) addClusters(root *cobra.Command) {
	clusters := &cobra.Command{Use: "clusters", Aliases: []string{"cluster"}, Short: "Create, inspect, protect and delete clusters", GroupID: "resources", Example: "  edka clusters create staging --wait\n  edka clusters list\n  edka clusters get production\n  edka clusters kubeconfig production --merge --use"}
	a.strictGroup(clusters)
	list := &cobra.Command{Use: "list", Short: "List clusters", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.clusterRows(cmd.Context())
		if err != nil {
			return err
		}
		for _, row := range rows {
			withoutSecrets(row)
		}
		return a.renderRows(rows, clusterColumns)
	}}
	get := &cobra.Command{Use: "get [cluster]", Short: "Show a cluster and its node pools", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolveCluster(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		id, err := safeID(c.ID)
		if err != nil {
			return err
		}
		return a.showCluster(cmd.Context(), id)
	}}
	var outputFile string
	var merge, use, rotate bool
	kubeconfigCmd := &cobra.Command{Use: "kubeconfig <cluster>", Short: "Download your kubeconfig for a cluster", Long: "Download your own kubeconfig for a cluster to a new private file with\n--output-file, or into the kubeconfig kubectl uses with --merge.\n\n--merge adds the cluster, context and user as edka-<organization>-<cluster>,\nreplaces the entries an earlier merge wrote for the same cluster, and keeps the\nrest of the file. It writes to the first file in KUBECONFIG that exists, or to\n~/.kube/config, and keeps the previous version next to it with an .edka-backup\nsuffix. --use also makes the new context kubectl's current one.\n\nEdka issues each kubeconfig once. --rotate issues a new one and revokes the\nprevious kubeconfig.", Args: cobra.ExactArgs(1), Example: "  edka clusters kubeconfig production --merge --use\n  edka clusters kubeconfig production --rotate --merge\n  edka clusters kubeconfig production --output-file ~/.kube/production.yaml\n  KUBECONFIG=~/.kube/production.yaml kubectl get nodes", RunE: func(cmd *cobra.Command, args []string) error {
		switch {
		case outputFile == "" && !merge:
			return fmt.Errorf("pass --output-file or --merge; the kubeconfig contains credentials and Edka issues it once")
		case outputFile != "" && merge:
			return fmt.Errorf("pass either --output-file or --merge, not both")
		case use && !merge:
			return fmt.Errorf("--use needs --merge")
		}
		if outputFile != "" {
			if err := checkWritable(outputFile); err != nil {
				return err
			}
		}
		c, err := a.resolveCluster(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		id, err := safeID(c.ID)
		if err != nil {
			return err
		}
		name := a.kubeconfigName(c.Name)
		var target string
		if merge {
			if target, err = kubeconfig.DefaultPath(); err != nil {
				return err
			}
			if err := checkMergeTarget(target, name); err != nil {
				return err
			}
		}
		if rotate {
			if err := a.confirm("Rotate your credentials for cluster " + c.Name + "; your current kubeconfig for it stops working"); err != nil {
				return err
			}
			if _, err := a.request(cmd.Context(), "POST", "/api/clusters/"+id+"/user-credentials/rotate", nil, nil); err != nil {
				return err
			}
			a.message("✓ Rotated your credentials for %s; the previous kubeconfig no longer works", ui.Clean(c.Name))
		}
		response, err := a.request(cmd.Context(), "GET", "/api/clusters/"+id+"/user-kubeconfig/download", nil, nil)
		var apiError *api.Error
		if errors.As(err, &apiError) && apiError.Status == 403 && strings.Contains(apiError.Message, "already been downloaded") {
			if merge {
				return fmt.Errorf("%w\nRotating issues a new one and revokes the previous kubeconfig: edka clusters kubeconfig %s --rotate --merge", err, ui.Clean(c.Name))
			}
			return fmt.Errorf("%w\nRotating issues a new one and revokes the previous kubeconfig: edka clusters kubeconfig %s --rotate --output-file %s", err, ui.Clean(c.Name), shellJoin([]string{outputFile}))
		}
		if err != nil {
			return err
		}
		if outputFile != "" {
			if err := config.WritePrivate(outputFile, response.Body); err != nil {
				return fmt.Errorf("the kubeconfig was issued but could not be saved: %w", err)
			}
			a.message("✓ Saved your kubeconfig for %s to %s\n  Edka issues it once; keep this file private.\n  Next: KUBECONFIG=%s kubectl get nodes", ui.Clean(c.Name), outputFile, shellJoin([]string{outputFile}))
			return nil
		}
		return a.mergeKubeconfig(target, name, c.ID, c.Name, response.Body, use)
	}}
	kubeconfigCmd.Flags().StringVar(&outputFile, "output-file", "", "New file to write, readable only by you")
	kubeconfigCmd.Flags().BoolVar(&merge, "merge", false, "Merge into the kubeconfig kubectl uses")
	kubeconfigCmd.Flags().BoolVar(&use, "use", false, "With --merge, switch kubectl to the merged context")
	kubeconfigCmd.Flags().BoolVar(&rotate, "rotate", false, "Rotate your credentials first, which revokes your previous kubeconfig")
	protection := func(use, short string, protect bool) *cobra.Command {
		return &cobra.Command{Use: use + " <cluster>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.resolveCluster(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			id, err := safeID(c.ID)
			if err != nil {
				return err
			}
			action := "protect"
			if !protect {
				action = "unprotect"
				if err := a.confirm("Remove deletion protection from cluster " + c.Name); err != nil {
					return err
				}
			}
			response, err := a.request(cmd.Context(), "POST", "/api/clusters/"+id+"/"+action, nil, nil)
			if err != nil {
				return err
			}
			if protect {
				a.message("✓ Protected %s from deletion", ui.Clean(c.Name))
			} else {
				a.message("✓ Removed deletion protection from %s", ui.Clean(c.Name))
			}
			if a.output != "table" {
				return a.render(response)
			}
			return nil
		}}
	}
	remove := &cobra.Command{Use: "delete <cluster>", Short: "Delete a cluster, its servers and data", Long: "Delete a cluster with its servers, volumes and data. Deletion continues in the\nbackground. Protected clusters must be unprotected first.\n\nTo remove a disconnected or self-hosted cluster from Edka without touching its\nservers, use `edka api clusters delete <cluster> --field remove_from_edka_only:=true`.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolveCluster(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		id, err := safeID(c.ID)
		if err != nil {
			return err
		}
		if err := a.confirmTyped(fmt.Sprintf("Delete cluster %s with its servers, volumes and data", c.Name), c.Name); err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "DELETE", "/api/clusters/"+id, nil, nil)
		var apiError *api.Error
		if errors.As(err, &apiError) && apiError.Status == 403 && strings.Contains(apiError.Message, "protected") {
			return fmt.Errorf("cluster %s is protected; run `edka clusters unprotect %s` first", ui.Clean(c.Name), ui.Clean(c.Name))
		}
		if err != nil {
			return err
		}
		a.message("✓ Deleting %s in the background\n  Check progress: edka clusters get %s", ui.Clean(c.Name), ui.Clean(c.Name))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	protect, unprotect := protection("protect", "Protect a cluster from deletion", true), protection("unprotect", "Allow a cluster to be deleted", false)
	diagnose := a.clusterDiagnoseCommand()
	a.completes(a.clusterChoices, get, diagnose, kubeconfigCmd, protect, unprotect, remove)
	clusters.AddCommand(list, get, diagnose, a.clusterCreateCommand(), kubeconfigCmd, protect, unprotect, remove)
	root.AddCommand(clusters)
}
