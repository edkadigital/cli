package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/kubeconfig"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// clusterName is the console's rule for cluster names.
var clusterName = regexp.MustCompile(`^[a-z0-9]+(-+[a-z0-9]+)*$`)

// clusterWaitSteps describes a cluster status that is still in progress.
var clusterWaitSteps = map[string]string{
	"pending":           "Waiting for provisioning to start…",
	"creating":          "Creating servers and installing k3s…",
	"installing_addons": "Installing add-ons…",
	"updating":          "Updating the cluster…",
}

// backupCron picks a daily etcd backup time between 00:00 and 06:00 UTC, like
// the console, so clusters don't all back up at the same minute.
func backupCron() string {
	minutes := rand.IntN(6*60 + 1)
	return fmt.Sprintf("%d %d * * *", minutes%60, minutes/60)
}

// clusterCreateCommand creates a Hetzner cluster with the console's defaults.
func (a *App) clusterCreateCommand() *cobra.Command {
	var location, masterType, k3sVersion, nodeType, data string
	var fields []string
	var nodes int
	var ha, noProtect, noEtcdBackup, dryRun, wait, mergeKubeconfig, use bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{Use: "create <name>", Short: "Create a cluster on Hetzner", Long: "Create a cluster on Hetzner with the console's defaults: deletion protection\non, a daily etcd backup at a random time between 00:00 and 06:00 UTC, and one\nnode pool named default. Edka creates the servers in your Hetzner project with\nthe organization's stored Hetzner token.\n\nBefore creating anything, the command shows the estimated monthly price and asks\nfor confirmation; --yes skips it, and --dry-run prints the request instead.\n--data and --field set or replace top-level request fields for options without\na flag, such as worker_node_pools or nat_gateway_enabled.\n\nWith --wait, provisioning progress goes to stderr until the cluster is active.\nWith --merge-kubeconfig, your kubeconfig is then merged as by\n`edka clusters kubeconfig <cluster> --merge`.", Args: cobra.ExactArgs(1), Example: "  edka clusters create staging --wait\n  edka clusters create production --ha --nodes 3 --node-type cx33 --wait --merge-kubeconfig --use\n  edka clusters create edge --location ash --dry-run\n  edka clusters create batch --field 'worker_node_pools:=[{\"name\":\"spot\",\"instance_type\":\"cx43\",\"instance_count\":2}]'", RunE: func(cmd *cobra.Command, args []string) error {
		switch {
		case nodes < 1:
			return fmt.Errorf("--nodes must be at least 1")
		case use && !mergeKubeconfig:
			return fmt.Errorf("--use needs --merge-kubeconfig")
		case mergeKubeconfig && !wait:
			return fmt.Errorf("--merge-kubeconfig needs --wait; a kubeconfig exists once the cluster is active")
		case wait && waitTimeout <= 0:
			return fmt.Errorf("wait-timeout must be positive")
		}
		ctx := cmd.Context()
		body := map[string]any{
			"name":                 args[0],
			"location":             location,
			"master_instance_type": masterType,
			"master_ha":            ha,
			"protected":            !noProtect,
			// The pool runs in the cluster's location.
			"worker_node_pools": []any{map[string]any{"name": "default", "instance_type": nodeType, "instance_count": nodes}},
		}
		if k3sVersion != "" {
			body["k3s_version"] = k3sVersion
		}
		if !noEtcdBackup {
			body["etcd_backup"] = map[string]any{"enabled": true, "snapshot_schedule_cron": backupCron()}
		}
		if data != "" || len(fields) > 0 {
			raw, err := a.body(data, fields)
			if err != nil {
				return err
			}
			overrides := map[string]any{}
			if err := json.Unmarshal(raw, &overrides); err != nil {
				return fmt.Errorf("--data must be a JSON object")
			}
			for key, value := range overrides {
				body[key] = value
			}
		}

		name, _ := body["name"].(string)
		if len(name) < 3 || len(name) > 32 || !clusterName.MatchString(name) {
			return fmt.Errorf("cluster name %q must be 3 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or digit", name)
		}
		if err := a.checkLocation(ctx, body); err != nil {
			return err
		}
		if err := a.chooseK3sVersion(ctx, body); err != nil {
			return err
		}
		var target, entries string
		if mergeKubeconfig {
			var err error
			if target, err = kubeconfig.DefaultPath(); err != nil {
				return err
			}
			entries = a.kubeconfigName(name)
			// Fail before paying for a cluster whose kubeconfig can't be merged.
			if err := checkMergeTarget(target, entries); err != nil {
				return err
			}
		}

		a.message("%s", strings.Join(append(describeCluster(body), a.clusterPrice(ctx, body)...), "\n"))
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		if dryRun {
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, encoded, "", "  "); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, pretty.String())
			return nil
		}
		if err := a.confirm("Create cluster " + name); err != nil {
			return err
		}

		response, err := a.request(ctx, "POST", "/api/clusters", nil, encoded)
		var apiError *api.Error
		if errors.As(err, &apiError) {
			switch {
			// Edka answers a plan's cluster or vCPU limit with 429 too, and explains it.
			case apiError.Status == 429 && (strings.HasSuffix(apiError.Reason, "limit reached") || apiError.Code == "quota_exceeded"):
				return fmt.Errorf("%s (HTTP 429)", ui.Clean(apiError.Message))
			case apiError.Status == 400 && strings.Contains(apiError.Message, "Hetzner API token is required"):
				return fmt.Errorf("%w\nSave the organization's Hetzner token in the Edka console, or pass hetzner_token in --data @file.json", err)
			}
		}
		if err != nil {
			return err
		}
		created, err := identityData(response.Body)
		if err != nil {
			return err
		}
		id, err := safeID(text(created, "clusterId"))
		if err != nil {
			return fmt.Errorf("cluster submitted but no ID returned; inspect `edka clusters list`")
		}
		if !wait {
			a.message("✓ Creating cluster %s\n  Next: edka clusters get %s", ui.Clean(name), ui.Clean(name))
			return a.render(response)
		}

		a.message("Creating cluster %s…", ui.Clean(name))
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		if err := a.waitCluster(waitCtx, id, name, map[string]bool{}); err != nil {
			return a.clusterWaitError(waitCtx, name, err)
		}
		a.message("✓ Cluster %s is active", ui.Clean(name))
		if mergeKubeconfig {
			downloaded, err := a.request(ctx, "GET", "/api/clusters/"+id+"/user-kubeconfig/download", nil, nil)
			if err != nil {
				return fmt.Errorf("cluster %s is active, but its kubeconfig could not be downloaded: %w\nRetry with: edka clusters kubeconfig %s --merge", ui.Clean(name), err, ui.Clean(name))
			}
			if err := a.mergeKubeconfig(target, entries, id, name, downloaded.Body, use); err != nil {
				return err
			}
		}
		return a.showCluster(ctx, id)
	}}
	f := cmd.Flags()
	f.StringVar(&location, "location", "fsn1", "Hetzner location of the control plane and the default pool")
	f.StringVar(&masterType, "master-type", "cx23", "Server type of the control plane")
	f.BoolVar(&ha, "ha", false, "Run three control plane servers instead of one")
	f.StringVar(&k3sVersion, "k3s-version", "", "k3s version (default: the one Edka recommends for new clusters)")
	f.StringVar(&nodeType, "node-type", "cx23", "Server type of the default node pool")
	f.IntVar(&nodes, "nodes", 1, "Servers in the default node pool")
	f.BoolVar(&noProtect, "no-protect", false, "Allow the cluster to be deleted without unprotecting it first")
	f.BoolVar(&noEtcdBackup, "no-etcd-backup", false, "Skip the daily etcd backup")
	f.StringVarP(&data, "data", "d", "", "Request fields as JSON, @file.json, or @-")
	f.StringArrayVarP(&fields, "field", "f", nil, "Request field: key=value or key:=JSON")
	f.BoolVar(&dryRun, "dry-run", false, "Print the request and price without creating the cluster")
	f.BoolVar(&wait, "wait", false, "Wait until the cluster is active")
	f.DurationVar(&waitTimeout, "wait-timeout", 30*time.Minute, "Maximum provisioning wait")
	f.BoolVar(&mergeKubeconfig, "merge-kubeconfig", false, "With --wait, merge your kubeconfig into the one kubectl uses")
	f.BoolVar(&use, "use", false, "With --merge-kubeconfig, switch kubectl to the new context")
	return cmd
}

// checkLocation rejects a location Edka can't create clusters in.
func (a *App) checkLocation(ctx context.Context, body map[string]any) error {
	location, _ := body["location"].(string)
	response, err := a.request(ctx, "GET", "/api/clusters/locations", nil, nil)
	if err != nil {
		return err
	}
	v, err := api.Data(response.Body)
	if err != nil {
		return err
	}
	var locations []string
	for _, item := range asList(v) {
		if s, ok := item.(string); ok {
			locations = append(locations, s)
		}
	}
	if len(locations) > 0 && !slices.Contains(locations, location) {
		return fmt.Errorf("unknown location %q; choose one of %s", location, strings.Join(locations, ", "))
	}
	return nil
}

// chooseK3sVersion fills in Edka's default k3s version, or checks the chosen one
// against the versions Edka installs.
func (a *App) chooseK3sVersion(ctx context.Context, body map[string]any) error {
	chosen, _ := body["k3s_version"].(string)
	response, err := a.request(ctx, "GET", "/api/clusters/k3s-versions", nil, nil)
	var apiError *api.Error
	if errors.As(err, &apiError) && apiError.Status == 404 {
		if chosen == "" {
			return fmt.Errorf("pass --k3s-version; this Edka API doesn't list k3s versions yet")
		}
		return nil
	}
	if err != nil {
		return err
	}
	v, err := identityData(response.Body)
	if err != nil {
		return err
	}
	var versions []string
	for _, item := range asList(v["versions"]) {
		if s, ok := item.(string); ok {
			versions = append(versions, s)
		}
	}
	if chosen == "" {
		chosen = text(v, "default")
		if chosen == "" {
			return fmt.Errorf("pass --k3s-version; Edka returned no default version")
		}
		body["k3s_version"] = chosen
		return nil
	}
	if len(versions) > 0 && !slices.Contains(versions, chosen) {
		return fmt.Errorf("Edka doesn't install k3s %s; choose one of %s", chosen, strings.Join(versions, ", "))
	}
	return nil
}

// describeCluster summarizes a create request for the confirmation.
func describeCluster(body map[string]any) []string {
	location, _ := body["location"].(string)
	masterType, _ := body["master_instance_type"].(string)
	controlPlane := "1 × " + masterType
	if ha, _ := body["master_ha"].(bool); ha {
		controlPlane = "3 × " + masterType + " (HA)"
	}
	lines := []string{
		fmt.Sprintf("Cluster %s in %s, k3s %s", text(body, "name"), location, text(body, "k3s_version")),
		"  Control plane: " + controlPlane,
	}
	for _, item := range asList(body["worker_node_pools"]) {
		pool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		size := fmt.Sprintf("%d", max(number(pool["instance_count"]), 1))
		if autoscaling, _ := pool["autoscaling_enabled"].(bool); autoscaling {
			size = fmt.Sprintf("%d–%d", number(pool["autoscaling_min_instances"]), number(pool["autoscaling_max_instances"]))
		}
		lines = append(lines, fmt.Sprintf("  Node pool %s: %s × %s", first(text(pool, "name"), "unnamed"), size, first(text(pool, "instance_type"), "cx23")))
	}
	protection := "off"
	if protected, _ := body["protected"].(bool); protected {
		protection = "on"
	}
	backup := "off"
	if etcd, ok := body["etcd_backup"].(map[string]any); ok {
		if enabled, _ := etcd["enabled"].(bool); enabled {
			backup = "on"
			var minute, hour int
			if n, _ := fmt.Sscanf(text(etcd, "snapshot_schedule_cron"), "%d %d * * *", &minute, &hour); n == 2 {
				backup = fmt.Sprintf("daily at %02d:%02d UTC", hour, minute)
			}
		}
	}
	return append(lines, fmt.Sprintf("  Deletion protection %s, etcd backup %s", protection, backup))
}

// clusterPrice estimates the monthly price with the console's pricing preview.
// A failed preview doesn't block creation, which still asks for confirmation.
func (a *App) clusterPrice(ctx context.Context, body map[string]any) []string {
	preview := map[string]any{"use_global_token": true}
	for key, value := range body {
		// The preview prices with the organization's token; don't send another.
		if key != "hetzner_token" && key != "temporary_hetzner_token" {
			preview[key] = value
		}
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		return []string{"  Price unavailable: " + err.Error()}
	}
	response, err := a.request(ctx, "POST", "/api/clusters/pricing-preview", nil, encoded)
	if err != nil {
		return []string{"  Price unavailable: " + ui.Clean(strings.SplitN(err.Error(), "\n", 2)[0])}
	}
	pricing, err := record(response.Body, "pricing")
	if err != nil {
		return []string{"  Price unavailable: " + err.Error()}
	}
	estimate := "  Estimated"
	if exact, _ := pricing["exact"].(bool); !exact {
		estimate = "  About"
	}
	monthly, ok := pricing["monthly_net"].(float64)
	if !ok {
		return []string{"  Price unavailable: Edka returned no monthly price"}
	}
	lines := []string{fmt.Sprintf("%s %.2f %s per month, excluding VAT", estimate, monthly, text(pricing, "currency"))}
	for _, warning := range asList(pricing["warnings"]) {
		if s, ok := warning.(string); ok && s != "" {
			lines = append(lines, "  Note: "+ui.Clean(s))
		}
	}
	return lines
}

// waitCluster prints a cluster's events to stderr until the cluster is active.
// seen holds the IDs of the events that are not printed, such as those of
// earlier changes.
func (a *App) waitCluster(ctx context.Context, id, name string, seen map[string]bool) error {
	p := a.startProgress()
	last := ""
	for {
		response, err := a.poll(ctx, p, "/api/clusters/"+id, nil)
		if err != nil {
			return err
		}
		cluster, err := record(response.Body, "data")
		if err != nil {
			return err
		}
		status := strings.ToLower(text(cluster, "status"))
		// Events arrive newest first.
		if events, err := a.objectsQuery(ctx, "/api/clusters/"+id+"/events", url.Values{"limit": {"100"}}); err == nil {
			for i := len(events) - 1; i >= 0; i-- {
				event := events[i]
				key := fmt.Sprint(event["id"])
				message := ui.Clean(text(event, "message"))
				if seen[key] || message == "" {
					continue
				}
				seen[key] = true
				last = message
				if progress := number(event["progress"]); progress > 0 {
					message = fmt.Sprintf("%3d%% %s", progress, message)
				}
				fmt.Fprintln(a.Err, message)
			}
		}
		switch status {
		case "active", "running":
			return nil
		case "failed", "error":
			return fmt.Errorf("cluster %s failed: %s\nFind the cause with `edka clusters diagnose %s`", ui.Clean(name), first(last, status), ui.Clean(name))
		}
		p.say("status", first(clusterWaitSteps[status], "Status: "+status))
		if err := pause(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// clusterWaitError explains a wait for a new cluster that ran out of time.
func (a *App) clusterWaitError(ctx context.Context, name string, err error) error {
	if ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("cluster %s is not active yet: %w; it keeps provisioning, follow it with `edka clusters get %s`", ui.Clean(name), ctx.Err(), ui.Clean(name))
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}
