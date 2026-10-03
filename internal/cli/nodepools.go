package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// nodePoolName is Edka's rule for node pool names.
var nodePoolName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// taintEffects are the effects Kubernetes gives a taint.
var taintEffects = []string{"NoSchedule", "PreferNoSchedule", "NoExecute"}

// poolRecords returns the node pools in a response: the active pools, then the
// ones Edka is deleting, which it lists apart.
func poolRecords(path string, body []byte) ([]map[string]any, error) {
	var envelope struct {
		Data             []map[string]any `json:"data"`
		PendingDeletions []map[string]any `json:"pending_deletions"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("expected node pools from %s", path)
	}
	return append(envelope.Data, envelope.PendingDeletions...), nil
}

func poolActive(m map[string]any) bool {
	return slices.Contains([]string{"", "active"}, text(m, "lifecycle_status"))
}
func poolMetal(m map[string]any) bool { return text(m, "provider") == "hetzner_metal" }
func poolStatus(m map[string]any) string {
	switch text(m, "lifecycle_status") {
	case "pending_delete":
		return "deleting"
	case "delete_failed":
		return "delete failed"
	}
	return "active"
}
func poolType(m map[string]any) string {
	if poolMetal(m) {
		return "metal"
	}
	return text(m, "instance_type")
}
func poolAutoscaling(m map[string]any) string {
	if enabled, _ := m["autoscaling_enabled"].(bool); !enabled {
		return "off"
	}
	return fmt.Sprintf("%d–%d", number(m["autoscaling_min_instances"]), number(m["autoscaling_max_instances"]))
}

// poolSize is the servers a pool holds: its range when it autoscales.
func poolSize(m map[string]any) string {
	if enabled, _ := m["autoscaling_enabled"].(bool); enabled {
		return poolAutoscaling(m)
	}
	return fmt.Sprint(number(m["desired_count"]))
}

// poolPairs prints labels as key=value and taints as key=value:effect.
func poolPairs(v any) string {
	pairs := []string{}
	for _, item := range asList(v) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pair := text(m, "key") + "=" + text(m, "value")
		if effect := text(m, "effect"); effect != "" {
			pair += ":" + effect
		}
		pairs = append(pairs, pair)
	}
	return strings.Join(pairs, ", ")
}

var poolColumns = []ui.Column{
	ui.Field("POOL", "name"),
	{Header: "TYPE", Value: poolType},
	{Header: "NODES", Value: func(m map[string]any) string {
		return fmt.Sprintf("%d/%d", number(m["actual_count"]), number(m["desired_count"]))
	}},
	ui.Field("LOCATION", "location"),
	{Header: "AUTOSCALING", Value: poolAutoscaling},
	{Header: "STATUS", Value: poolStatus},
	ui.Field("CLUSTER", "cluster_name"),
}

func poolNames(m map[string]any) []string { return []string{text(m, "name")} }
func poolDetail(m map[string]any) string {
	return details(poolSize(m)+" × "+poolType(m), text(m, "cluster_name"))
}

// poolRows lists the node pools of the context cluster, or of every cluster
// when there is none or all is set.
func (a *App) poolRows(ctx context.Context, all bool) ([]map[string]any, error) {
	return a.clusterScopedLists(ctx, all, "nodepools", poolRecords)
}

// resolvePool selects a node pool by ID or name.
func (a *App) resolvePool(ctx context.Context, target string) (*candidate, error) {
	if target == "" && a.noInput {
		return nil, fmt.Errorf("name the node pool; list them with `edka nodepools list`")
	}
	rows, err := a.poolRows(ctx, false)
	if err != nil {
		return nil, err
	}
	return a.pickScoped(ctx, "node pool", rows, target, "no node pools; add one with `edka nodepools add workers --type cx33`", poolNames, poolDetail)
}

// showPool prints a node pool's summary, with what is pinned to it.
func (a *App) showPool(ctx context.Context, m map[string]any) error {
	if a.output != "table" {
		return ui.Render(a.Out, jsonBody(map[string]any{"data": m}), a.output, false)
	}
	pinned := ""
	if cluster, err := safeID(text(m, "cluster_id")); err == nil {
		if response, err := a.request(ctx, "GET", "/api/clusters/"+cluster+"/nodepools/scheduling-references", nil, nil); err == nil {
			if pools, err := identityData(response.Body); err == nil {
				names := []string{}
				for _, item := range asList(pools[text(m, "name")]) {
					if reference, ok := item.(map[string]any); ok {
						names = append(names, strings.ReplaceAll(text(reference, "kind"), "_", " ")+" "+text(reference, "name"))
					}
				}
				pinned = strings.Join(names, ", ")
			}
		}
	}
	return ui.Fields(a.Out, [][2]string{
		{"Name", text(m, "name")},
		{"Status", poolStatus(m)},
		{"Message", text(m, "deletion_error")},
		{"Type", poolType(m)},
		{"Location", text(m, "location")},
		{"Nodes", fmt.Sprintf("%d of %d", number(m["actual_count"]), number(m["desired_count"]))},
		{"Autoscaling", poolAutoscaling(m)},
		{"Labels", poolPairs(m["labels"])},
		{"Taints", poolPairs(m["taints"])},
		{"Pinned to it", pinned},
		{"Cluster", text(m, "cluster_name")},
		{"Created", when(m["created_at"])},
		{"Changed", when(m["spec_updated_at"])},
		{"ID", text(m, "id")},
	}, a.color)
}

// poolEntry is a pool as the update takes it: the fields the console sends for
// a Hetzner Cloud pool. An update replaces the labels, the taints and the
// autoscaling switch of every pool it names, so each entry carries them.
func poolEntry(m map[string]any) map[string]any {
	entry := map[string]any{
		"name":                      text(m, "name"),
		"provider":                  "hcloud",
		"instance_type":             text(m, "instance_type"),
		"location":                  text(m, "location"),
		"desired_count":             number(m["desired_count"]),
		"autoscaling_enabled":       m["autoscaling_enabled"] == true,
		"autoscaling_min_instances": number(m["autoscaling_min_instances"]),
		"autoscaling_max_instances": number(m["autoscaling_max_instances"]),
	}
	// Edka takes a list for each, and a pool without labels may report none.
	for _, key := range []string{"labels", "taints"} {
		entry[key] = append([]any{}, asList(m[key])...)
	}
	if id := text(m, "id"); id != "" {
		entry["id"] = id
	}
	return entry
}

// poolRevision is the time a pool last changed, as Edka compares it: in UTC
// with milliseconds.
func poolRevision(m map[string]any) (string, error) {
	changed, err := time.Parse(time.RFC3339Nano, first(text(m, "spec_updated_at"), text(m, "updated_at")))
	if err != nil {
		return "", fmt.Errorf("node pool %s has no change time; rerun with --json to inspect it", ui.Clean(text(m, "name")))
	}
	return changed.UTC().Format("2006-01-02T15:04:05.000Z"), nil
}

// poolChange is one change to a cluster's node pools, and the request that makes it.
type poolChange struct {
	cluster *candidate
	// pools are the cluster's active pools as Edka reported them.
	pools []map[string]any
	// entries are the pools the request leaves in the cluster.
	entries []map[string]any
}

// startPoolChange reads the pools of the context cluster. Edka takes every
// pool of a cluster in one request and deletes the ones the request leaves
// out, so a change sends each pool back as it read it.
func (a *App) startPoolChange(ctx context.Context) (*poolChange, error) {
	cluster, err := a.resolveCluster(ctx, "")
	if err != nil {
		return nil, err
	}
	id, err := safeID(cluster.ID)
	if err != nil {
		return nil, err
	}
	path := "/api/clusters/" + id + "/nodepools"
	response, err := a.request(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	rows, err := poolRecords(path, response.Body)
	if err != nil {
		return nil, err
	}
	change := &poolChange{cluster: cluster}
	for _, row := range rows {
		if !poolActive(row) {
			continue
		}
		// A metal pool carries its servers, its vSwitch and its Robot account,
		// and an update that names them wrongly would change them.
		if poolMetal(row) {
			return nil, fmt.Errorf("cluster %s has the Hetzner Metal node pool %s; change its node pools in the Edka console", ui.Clean(cluster.Name), ui.Clean(text(row, "name")))
		}
		change.pools = append(change.pools, row)
		change.entries = append(change.entries, poolEntry(row))
	}
	return change, nil
}

// find returns the entry of a pool named by ID or name, and the pool as read.
func (c *poolChange) find(target string) (entry, pool map[string]any, err error) {
	for i, row := range c.pools {
		if target == text(row, "id") || target == text(row, "name") {
			return c.entries[i], row, nil
		}
	}
	return nil, nil, fmt.Errorf("node pool %q was not found in cluster %s; list them with `edka nodepools list`", target, ui.Clean(c.cluster.Name))
}

// body is the update request. It names the time each pool last changed, so
// Edka refuses the request when a pool changed after it was read.
func (c *poolChange) body() ([]byte, error) {
	revisions := []map[string]string{}
	for _, pool := range c.pools {
		revision, err := poolRevision(pool)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, map[string]string{"id": text(pool, "id"), "updated_at": revision})
	}
	entries := c.entries
	if entries == nil {
		entries = []map[string]any{}
	}
	return json.Marshal(map[string]any{"node_pools": entries, "pool_revisions": revisions})
}

// poolWrite holds the flags every node pool change takes.
type poolWrite struct {
	wait        bool
	waitTimeout time.Duration
	dryRun      bool
}

func (w *poolWrite) flags(cmd *cobra.Command, until string) {
	cmd.Flags().BoolVar(&w.wait, "wait", false, "Wait until "+until)
	cmd.Flags().DurationVar(&w.waitTimeout, "wait-timeout", 20*time.Minute, "Maximum wait for the change")
	cmd.Flags().BoolVar(&w.dryRun, "dry-run", false, "Print the request without sending it")
}

// applyPoolChange confirms and sends a change, and with --wait follows it
// until the cluster is active again. It returns the time the wait ends, for
// the caller to wait for the pool itself. The time is zero when the command
// has nothing more to do: after a dry run, or without --wait.
func (a *App) applyPoolChange(ctx context.Context, change *poolChange, w poolWrite, confirmation, started string) (time.Time, error) {
	none := time.Time{}
	if w.wait && w.waitTimeout <= 0 {
		return none, fmt.Errorf("wait-timeout must be positive")
	}
	body, err := change.body()
	if err != nil {
		return none, err
	}
	if w.dryRun {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err != nil {
			return none, err
		}
		fmt.Fprintln(a.Out, pretty.String())
		return none, nil
	}
	if err := a.confirm(confirmation); err != nil {
		return none, err
	}
	id, err := safeID(change.cluster.ID)
	if err != nil {
		return none, err
	}
	// The wait prints the events of this change, not the ones before it.
	seen := map[string]bool{}
	if w.wait {
		for _, event := range a.clusterEvents(ctx, id) {
			seen[fmt.Sprint(event["id"])] = true
		}
	}
	response, err := a.request(ctx, "PUT", "/api/clusters/"+id+"/nodepools", nil, body)
	if err != nil {
		return none, err
	}
	name := ui.Clean(change.cluster.Name)
	if !w.wait {
		a.message("✓ %s\n  Check progress: edka nodepools list --cluster %s", started, name)
		if a.output != "table" {
			return none, a.render(response)
		}
		return none, nil
	}
	a.message("%s…", started)
	deadline := time.Now().Add(w.waitTimeout)
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := a.waitCluster(waitCtx, id, change.cluster.Name, seen); err != nil {
		return none, unfinished(waitCtx, err, name)
	}
	return deadline, nil
}

// unfinished explains a wait that ran out of time: the change keeps running.
func unfinished(ctx context.Context, err error, cluster string) error {
	if ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("the change is not finished: %w; it keeps running, follow it with `edka nodepools list --cluster %s`", ctx.Err(), cluster)
}

// clusterEvents reads a cluster's latest events, or none when the read fails.
func (a *App) clusterEvents(ctx context.Context, cluster string) []map[string]any {
	events, err := a.objectsQuery(ctx, "/api/clusters/"+cluster+"/events", map[string][]string{"limit": {"100"}})
	if err != nil {
		return nil
	}
	return events
}

// currentPool reads a pool again during a wait. It returns nil when the
// cluster no longer has it.
func (a *App) currentPool(ctx context.Context, p *progress, cluster *candidate, name string) (map[string]any, error) {
	id, err := safeID(cluster.ID)
	if err != nil {
		return nil, err
	}
	path := "/api/clusters/" + id + "/nodepools"
	response, err := a.poll(ctx, p, path, nil)
	if err != nil {
		return nil, err
	}
	rows, err := poolRecords(path, response.Body)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if text(row, "name") == name {
			row["cluster_name"] = cluster.Name
			if text(row, "cluster_id") == "" {
				row["cluster_id"] = cluster.ID
			}
			return row, nil
		}
	}
	return nil, nil
}

// settlePool waits until a pool has the servers it is set to have, and returns
// it. An autoscaling pool has as many as its workloads need.
func (a *App) settlePool(ctx context.Context, deadline time.Time, cluster *candidate, name string) (map[string]any, error) {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	p := a.startProgress()
	for {
		pool, err := a.currentPool(ctx, p, cluster, name)
		switch {
		case err != nil:
			return nil, unfinished(ctx, err, ui.Clean(cluster.Name))
		case pool == nil:
			return nil, fmt.Errorf("cluster %s is active, but it has no node pool %s; inspect `edka nodepools list --cluster %s`", ui.Clean(cluster.Name), name, ui.Clean(cluster.Name))
		case pool["autoscaling_enabled"] == true || number(pool["actual_count"]) == number(pool["desired_count"]):
			return pool, nil
		}
		p.say("servers", fmt.Sprintf("Node pool %s has %d of %s…", name, number(pool["actual_count"]), servers(number(pool["desired_count"]))))
		if err := pause(ctx, pollInterval); err != nil {
			return nil, unfinished(ctx, err, ui.Clean(cluster.Name))
		}
	}
}

// keyValues parses --label key=value flags into the records Edka stores.
func keyValues(flags []string) ([]any, error) {
	records := []any{}
	for _, flag := range flags {
		key, value, ok := strings.Cut(flag, "=")
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("a label is key=value: %q", flag)
		}
		records = append(records, map[string]any{"key": key, "value": value})
	}
	return records, nil
}

// taints parses --taint key=value:effect flags into the records Edka stores.
func taints(flags []string) ([]any, error) {
	records := []any{}
	for _, flag := range flags {
		pair, effect, _ := strings.Cut(flag, ":")
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" || value == "" || !slices.Contains(taintEffects, effect) {
			return nil, fmt.Errorf("a taint is key=value:effect, with the effect %s: %q", strings.Join(taintEffects, ", "), flag)
		}
		records = append(records, map[string]any{"key": key, "value": value, "effect": effect})
	}
	return records, nil
}

// serverTypes lists the server types Edka offers in a location.
func (a *App) serverTypes(ctx context.Context, location string) ([]map[string]any, error) {
	id, err := safeID(location)
	if err != nil {
		return nil, fmt.Errorf("invalid location %q", location)
	}
	return a.objects(ctx, "/api/instance-types/"+id)
}

func servers(count int) string {
	return fmt.Sprintf("%d %s", count, plural(count, "server", "servers"))
}

func (a *App) addNodePools(root *cobra.Command) {
	nodepools := &cobra.Command{Use: "nodepools", Aliases: []string{"nodepool", "node-pools"}, Short: "List, add, scale and delete node pools", Long: "List the node pools of a cluster, add a pool, change how many servers a pool\nhas, and delete a pool. Edka creates and deletes the servers in your Hetzner\nproject.\n\nadd, scale and delete show what they change and ask for confirmation; --yes\nskips the question, and --dry-run prints the request instead. They act on the\nlinked cluster, or on the one --cluster names. Edka applies one node pool change\nto a cluster at a time, and refuses a change while another one runs.", GroupID: "resources", Example: "  edka nodepools list\n  edka nodepools add workers --type cx33 --nodes 2 --wait\n  edka nodepools scale workers --nodes 4 --wait\n  edka nodepools delete workers"}
	a.strictGroup(nodepools)
	var all bool
	list := &cobra.Command{Use: "list", Short: "List node pools", Long: "List the node pools of the linked or selected cluster, or of every cluster when\nnone is linked. Use --all to include every cluster. NODES shows the servers that\nexist and the servers the pool is set to have.", Args: cobra.NoArgs, Example: "  edka nodepools list\n  edka nodepools list --all --json", RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.poolRows(cmd.Context(), all)
		if err != nil {
			return err
		}
		return a.renderRows(rows, poolColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List node pools in every cluster, not only the linked one")
	get := &cobra.Command{Use: "get [pool]", Short: "Show a node pool, its labels and taints", Args: cobra.MaximumNArgs(1), Example: "  edka nodepools get workers\n  edka nodepools get workers --cluster production --json", RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolvePool(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		return a.showPool(cmd.Context(), c.Record)
	}}

	var serverType, location string
	var nodes, minimum, maximum int
	var labels, taintFlags []string
	var adding poolWrite
	add := &cobra.Command{Use: "add <name>", Short: "Add a node pool to a cluster", Long: "Add a Hetzner Cloud node pool to the linked or selected cluster. The pool runs\nin the cluster's location unless --location names another.\n\n--nodes sets how many servers the pool has. With --min and --max the pool\nautoscales between them instead, and autoscaling stays on for the life of the\npool.\n\nA label is key=value. A taint is key=value:effect, with the effect NoSchedule,\nPreferNoSchedule or NoExecute.", Args: cobra.ExactArgs(1), Example: "  edka nodepools add workers --type cx33 --nodes 2 --wait\n  edka nodepools add burst --type cpx32 --min 0 --max 5\n  edka nodepools add gpu-jobs --type ccx33 --label workload=batch --taint dedicated=batch:NoSchedule\n  edka nodepools add workers --type cx33 --cluster production --dry-run", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, name := cmd.Context(), args[0]
		autoscaling := cmd.Flags().Changed("min") || cmd.Flags().Changed("max")
		switch {
		case !nodePoolName.MatchString(name):
			return fmt.Errorf("node pool name %q must be 1 to 63 lowercase letters, digits and hyphens, starting and ending with a letter or digit", name)
		case name == "master":
			return fmt.Errorf("the name master is taken by the control plane; choose another")
		case autoscaling && !(cmd.Flags().Changed("min") && cmd.Flags().Changed("max")):
			return fmt.Errorf("autoscaling needs both --min and --max")
		case autoscaling && (minimum < 0 || maximum < 1 || maximum < minimum):
			return fmt.Errorf("--max must be at least 1 and at least --min, and --min at least 0")
		case autoscaling && cmd.Flags().Changed("nodes") && (nodes < minimum || nodes > maximum):
			return fmt.Errorf("--nodes must be between --min and --max")
		case !autoscaling && nodes < 1:
			return fmt.Errorf("--nodes must be at least 1")
		}
		labelRecords, err := keyValues(labels)
		if err != nil {
			return err
		}
		taintRecords, err := taints(taintFlags)
		if err != nil {
			return err
		}
		change, err := a.startPoolChange(ctx)
		if err != nil {
			return err
		}
		cluster := ui.Clean(change.cluster.Name)
		if _, _, err := change.find(name); err == nil {
			return fmt.Errorf("cluster %s already has a node pool named %s", cluster, name)
		}
		where := first(location, text(change.cluster.Record, "location"))
		types, err := a.serverTypes(ctx, where)
		if err != nil {
			return err
		}
		known := []string{}
		for _, t := range types {
			known = append(known, text(t, "name"))
		}
		if len(known) == 0 {
			return fmt.Errorf("Edka offers no server types in %q; choose a location with --location", where)
		}
		if !slices.Contains(known, serverType) {
			return fmt.Errorf("unknown server type %q in %s; choose one of %s", serverType, where, ui.Clean(strings.Join(known, ", ")))
		}
		entry := map[string]any{"name": name, "provider": "hcloud", "instance_type": serverType, "location": where, "desired_count": nodes, "autoscaling_enabled": autoscaling, "labels": labelRecords, "taints": taintRecords}
		if autoscaling {
			entry["autoscaling_min_instances"], entry["autoscaling_max_instances"] = minimum, maximum
			if !cmd.Flags().Changed("nodes") {
				entry["desired_count"] = minimum
			}
		}
		change.entries = append(change.entries, entry)
		size := fmt.Sprintf("%d × %s in %s", nodes, serverType, where)
		if autoscaling {
			size = fmt.Sprintf("%d–%d × %s in %s, autoscaling", minimum, maximum, serverType, where)
		}
		deadline, err := a.applyPoolChange(ctx, change, adding, fmt.Sprintf("Add node pool %s to cluster %s with %s", name, change.cluster.Name, size), fmt.Sprintf("Adding node pool %s to cluster %s", name, cluster))
		if err != nil || deadline.IsZero() {
			return err
		}
		pool, err := a.settlePool(ctx, deadline, change.cluster, name)
		if err != nil {
			return err
		}
		a.message("✓ Node pool %s has %d of %s", name, number(pool["actual_count"]), servers(number(pool["desired_count"])))
		return a.showPool(ctx, pool)
	}}
	add.Flags().StringVar(&serverType, "type", "", "Hetzner server type, such as cx33")
	add.Flags().IntVar(&nodes, "nodes", 1, "Servers in the pool")
	add.Flags().StringVar(&location, "location", "", "Hetzner location (default: the cluster's)")
	add.Flags().IntVar(&minimum, "min", 0, "Fewest servers of an autoscaling pool")
	add.Flags().IntVar(&maximum, "max", 0, "Most servers of an autoscaling pool")
	add.Flags().StringArrayVar(&labels, "label", nil, "Node label as key=value")
	add.Flags().StringArrayVar(&taintFlags, "taint", nil, "Node taint as key=value:effect")
	_ = add.MarkFlagRequired("type")
	adding.flags(add, "the pool's servers have joined the cluster")
	a.completesFlag(add, "type", a.offers(func(ctx context.Context) ([]candidate, error) {
		where := location
		if where == "" {
			cluster, err := a.resolveCluster(ctx, "")
			if err != nil {
				return nil, err
			}
			where = text(cluster.Record, "location")
		}
		types, err := a.serverTypes(ctx, where)
		choices := []candidate{}
		for _, t := range types {
			choices = append(choices, candidate{Name: text(t, "name"), Detail: fmt.Sprintf("%d vCPU, %d GB", number(t["cores"]), number(t["memory"]))})
		}
		return choices, err
	}))
	a.completesFlag(add, "location", a.offers(func(ctx context.Context) ([]candidate, error) {
		response, err := a.request(ctx, "GET", "/api/clusters/locations", nil, nil)
		if err != nil {
			return nil, err
		}
		v, err := api.Data(response.Body)
		choices := []candidate{}
		for _, item := range asList(v) {
			if s, ok := item.(string); ok {
				choices = append(choices, candidate{Name: s})
			}
		}
		return choices, err
	}))

	var scaling poolWrite
	var scaleNodes, scaleMin, scaleMax int
	scale := &cobra.Command{Use: "scale <pool>", Short: "Change how many servers a node pool has", Long: "Set how many servers a node pool has with --nodes. Edka creates the servers a\nlarger pool needs. For a smaller pool it drains the servers it removes, then\ndeletes them.\n\nAn autoscaling pool takes --min and --max instead. On a pool with a set number\nof servers, --min and --max turn autoscaling on, and it stays on for the life\nof the pool.", Args: cobra.ExactArgs(1), Example: "  edka nodepools scale workers --nodes 4 --wait\n  edka nodepools scale burst --max 8\n  edka nodepools scale workers --min 2 --max 6", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		setNodes, setMin, setMax := cmd.Flags().Changed("nodes"), cmd.Flags().Changed("min"), cmd.Flags().Changed("max")
		switch {
		case !setNodes && !setMin && !setMax:
			return fmt.Errorf("pass --nodes, or --min and --max for an autoscaling pool")
		case setNodes && (setMin || setMax):
			return fmt.Errorf("pass --nodes, or --min and --max, not both")
		case setNodes && scaleNodes < 1:
			return fmt.Errorf("--nodes must be at least 1; to remove the pool, run `edka nodepools delete %s`", args[0])
		}
		change, err := a.startPoolChange(ctx)
		if err != nil {
			return err
		}
		entry, pool, err := change.find(args[0])
		if err != nil {
			return err
		}
		name, cluster := ui.Clean(text(pool, "name")), ui.Clean(change.cluster.Name)
		autoscales := pool["autoscaling_enabled"] == true
		var confirmation, unchanged string
		if setNodes {
			if autoscales {
				return fmt.Errorf("node pool %s autoscales between %d and %d servers; change the range with --min and --max", name, number(pool["autoscaling_min_instances"]), number(pool["autoscaling_max_instances"]))
			}
			current := number(pool["desired_count"])
			entry["desired_count"] = scaleNodes
			confirmation = fmt.Sprintf("Scale node pool %s in cluster %s from %s to %d", text(pool, "name"), change.cluster.Name, servers(current), scaleNodes)
			if scaleNodes < current {
				confirmation += fmt.Sprintf(", which drains and deletes %s", servers(current-scaleNodes))
			}
			if scaleNodes == current {
				unchanged = fmt.Sprintf("node pool %s already has %s", name, servers(scaleNodes))
			}
		} else {
			if !autoscales && !(setMin && setMax) {
				return fmt.Errorf("node pool %s has a set number of servers; change it with --nodes, or pass both --min and --max to turn autoscaling on", name)
			}
			low, high := number(pool["autoscaling_min_instances"]), number(pool["autoscaling_max_instances"])
			if setMin {
				low = scaleMin
			}
			if setMax {
				high = scaleMax
			}
			if low < 0 || high < 1 || high < low {
				return fmt.Errorf("--max must be at least 1 and at least --min, and --min at least 0; the range would be %d to %d", low, high)
			}
			entry["autoscaling_enabled"], entry["autoscaling_min_instances"], entry["autoscaling_max_instances"] = true, low, high
			if autoscales {
				confirmation = fmt.Sprintf("Change node pool %s in cluster %s to autoscale between %d and %d servers, from %d and %d", text(pool, "name"), change.cluster.Name, low, high, number(pool["autoscaling_min_instances"]), number(pool["autoscaling_max_instances"]))
				if low == number(pool["autoscaling_min_instances"]) && high == number(pool["autoscaling_max_instances"]) {
					unchanged = fmt.Sprintf("node pool %s already autoscales between %d and %d servers", name, low, high)
				}
			} else {
				confirmation = fmt.Sprintf("Turn autoscaling on for node pool %s in cluster %s, between %d and %d servers, for the life of the pool", text(pool, "name"), change.cluster.Name, low, high)
			}
		}
		if unchanged != "" {
			a.message("Nothing to change: %s.", unchanged)
			// No request was sent, so there is no response to print.
			if a.output != "table" {
				return ui.Render(a.Out, jsonBody(map[string]any{"changed": false, "data": pool}), a.output, false)
			}
			return nil
		}
		deadline, err := a.applyPoolChange(ctx, change, scaling, confirmation, fmt.Sprintf("Scaling node pool %s in cluster %s", name, cluster))
		if err != nil || deadline.IsZero() {
			return err
		}
		current, err := a.settlePool(ctx, deadline, change.cluster, text(pool, "name"))
		if err != nil {
			return err
		}
		a.message("✓ Node pool %s has %d of %s", name, number(current["actual_count"]), servers(number(current["desired_count"])))
		return a.showPool(ctx, current)
	}}
	scale.Flags().IntVar(&scaleNodes, "nodes", 0, "Servers the pool has")
	scale.Flags().IntVar(&scaleMin, "min", 0, "Fewest servers of an autoscaling pool")
	scale.Flags().IntVar(&scaleMax, "max", 0, "Most servers of an autoscaling pool")
	scaling.flags(scale, "the pool has its servers")

	var removing poolWrite
	remove := &cobra.Command{Use: "delete <pool>", Short: "Delete a node pool and its servers", Long: "Delete a node pool. Edka drains its servers, then deletes them. It refuses\nwhile a deployment, an app, a database or another resource is pinned to the\npool, and the error names them.", Args: cobra.ExactArgs(1), Example: "  edka nodepools delete workers\n  edka nodepools delete workers --cluster production --yes --wait", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		change, err := a.startPoolChange(ctx)
		if err != nil {
			return err
		}
		_, pool, err := change.find(args[0])
		if err != nil {
			return err
		}
		name, cluster := ui.Clean(text(pool, "name")), ui.Clean(change.cluster.Name)
		kept := []map[string]any{}
		for i, row := range change.pools {
			if text(row, "id") != text(pool, "id") {
				kept = append(kept, change.entries[i])
			}
		}
		change.entries = kept
		from := " from cluster " + change.cluster.Name
		if len(kept) == 0 {
			from = ", the last one of cluster " + change.cluster.Name + ","
		}
		confirmation := fmt.Sprintf("Delete node pool %s%s with its %s", text(pool, "name"), from, servers(max(number(pool["actual_count"]), number(pool["desired_count"]))))
		deadline, err := a.applyPoolChange(ctx, change, removing, confirmation, fmt.Sprintf("Deleting node pool %s from cluster %s", name, cluster))
		if err != nil || deadline.IsZero() {
			return err
		}
		// The cluster can be active again before the last server is gone.
		p := a.startProgress()
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		for {
			current, err := a.currentPool(waitCtx, p, change.cluster, text(pool, "name"))
			switch {
			case err != nil:
				return unfinished(waitCtx, err, cluster)
			case current == nil:
				a.message("✓ Deleted node pool %s from cluster %s", name, cluster)
				if a.output != "table" {
					return ui.Render(a.Out, jsonBody(map[string]any{"deleted": true, "node_pool": text(pool, "id")}), a.output, false)
				}
				return nil
			case text(current, "lifecycle_status") == "delete_failed":
				return fmt.Errorf("deleting node pool %s failed: %s\nRetry or restore the pool in the console's node pool settings", name, first(ui.Clean(text(current, "deletion_error")), "Edka gave no reason"))
			}
			p.say("deleting", "Waiting for the servers to be deleted…")
			if err := pause(waitCtx, pollInterval); err != nil {
				return unfinished(waitCtx, err, cluster)
			}
		}
	}}
	removing.flags(remove, "the pool's servers are deleted")
	a.completes(func(ctx context.Context) ([]candidate, error) {
		rows, err := a.poolRows(ctx, false)
		return scopedCandidates(rows, poolNames, poolDetail), err
	}, get, scale, remove)
	nodepools.AddCommand(list, get, add, scale, remove)
	root.AddCommand(nodepools)
}
