package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// managedAddons are the add-ons the console installs, updates and removes
// from another cluster view, with that view's settings, instead of Add-ons.
// Envoy Gateway and MetalLB are not among them: the Gateway view changes them
// with the same calls as any other add-on.
var managedAddons = map[string]string{
	"zot":                              "Registries",
	"tailscale-operator":               "Gateway",
	"cloudflared":                      "Gateway",
	"github-actions-runner-controller": "Actions",
}

// addonUpdateNotes say what an update does beyond the add-on's own pods.
var addonUpdateNotes = map[string]string{
	"envoy-gateway": "The Envoy proxies roll out again, so every gateway class briefly serves traffic from new pods.",
}

// managedElsewhere refuses a change to an add-on that another console view manages.
func managedElsewhere(name string) error {
	if area, ok := managedAddons[name]; ok {
		return fmt.Errorf("%s is managed from Cluster > %s in the console; open the cluster with `edka open`", ui.Clean(name), area)
	}
	return nil
}

// addonVersion matches an optional v, dotted numbers and an optional
// prerelease. Build metadata after + is ignored.
var addonVersion = regexp.MustCompile(`^v?(\d+(?:\.\d+)*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?$`)

// compareDigits orders two strings of digits by value, whatever their length.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return cmp.Compare(len(a), len(b))
	}
	return strings.Compare(a, b)
}

// newerVersion reports whether candidate is a later release than current by
// SemVer precedence, the rule the console uses to offer an update. Versions
// that are not dotted numbers are newer only when they differ.
func newerVersion(candidate, current string) bool {
	candidate, current = strings.TrimSpace(candidate), strings.TrimSpace(current)
	next, installed := addonVersion.FindStringSubmatch(candidate), addonVersion.FindStringSubmatch(current)
	if next == nil || installed == nil {
		return candidate != current
	}
	a, b := strings.Split(next[1], "."), strings.Split(installed[1], ".")
	for i := range max(len(a), len(b)) {
		// A missing part counts as zero, so 1.2 equals 1.2.0.
		x, y := "", ""
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if d := compareDigits(x, y); d != 0 {
			return d > 0
		}
	}
	// A release outranks every prerelease of the same version.
	if next[2] == "" || installed[2] == "" {
		return next[2] == "" && installed[2] != ""
	}
	a, b = strings.Split(next[2], "."), strings.Split(installed[2], ".")
	for i := range min(len(a), len(b)) {
		numberA, numberB := strings.Trim(a[i], "0123456789") == "", strings.Trim(b[i], "0123456789") == ""
		d := strings.Compare(a[i], b[i])
		switch {
		case numberA && numberB:
			d = compareDigits(a[i], b[i])
		case numberA:
			// Numeric identifiers sort below alphanumeric ones.
			d = -1
		case numberB:
			d = 1
		}
		if d != 0 {
			return d > 0
		}
	}
	return len(a) > len(b)
}

// addonDependencies lists the add-ons that a catalog entry's template names
// as its dependencies.
func addonDependencies(entry map[string]any) []string {
	var manifest struct {
		Metadata struct {
			Annotations map[string]any `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	dependencies := []string{}
	// The first document of the template carries the annotations.
	if yaml.NewDecoder(strings.NewReader(text(entry, "template"))).Decode(&manifest) != nil {
		return dependencies
	}
	names, _ := manifest.Metadata.Annotations["meta_dependencies"].(string)
	for _, name := range strings.Split(names, ",") {
		if name = strings.TrimSpace(name); name != "" {
			dependencies = append(dependencies, name)
		}
	}
	return dependencies
}

// recordWith is the first record whose key has value, or nil.
func recordWith(rows []map[string]any, key, value string) map[string]any {
	for _, row := range rows {
		if text(row, key) == value {
			return row
		}
	}
	return nil
}

// describeAddons adds what the catalog says about each installed add-on:
// catalog_version, category, namespace, required and depends_on. It also sets
// update_available, dependents for the add-ons in the same cluster that depend
// on it, and managed_from for an add-on that another console view manages.
func describeAddons(rows, catalog []map[string]any) {
	dependencies := map[string][]string{}
	for _, row := range rows {
		name := text(row, "addon_name")
		entry := recordWith(catalog, "name", name)
		if _, read := dependencies[name]; !read {
			dependencies[name] = addonDependencies(entry)
		}
		row["catalog_version"], row["category"], row["namespace"] = entry["default_version"], entry["category"], entry["namespace"]
		row["required"] = entry["required"] == true
		row["depends_on"] = dependencies[name]
		row["managed_from"] = nil
		if area, managed := managedAddons[name]; managed {
			row["managed_from"] = area
		}
		version, latest := text(row, "version"), text(row, "catalog_version")
		row["update_available"] = text(row, "status") == "installed" && row["managed_from"] == nil && version != "" && latest != "" && newerVersion(latest, version)
	}
	for _, row := range rows {
		dependents := []string{}
		for _, other := range rows {
			if text(other, "cluster_id") == text(row, "cluster_id") && slices.Contains(dependencies[text(other, "addon_name")], text(row, "addon_name")) {
				dependents = append(dependents, text(other, "addon_name"))
			}
		}
		row["dependents"] = dependents
	}
}

// addonRows lists installed add-ons, described by the catalog and sorted by
// cluster and name.
func (a *App) addonRows(ctx context.Context, all bool) ([]map[string]any, error) {
	rows, err := a.clusterScopedRows(ctx, all, "addons")
	if err != nil || len(rows) == 0 {
		return rows, err
	}
	catalog, err := a.objects(ctx, "/api/addons/catalog")
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(rows, func(x, y map[string]any) int {
		return cmp.Or(strings.Compare(text(x, "cluster_name"), text(y, "cluster_name")), strings.Compare(text(x, "addon_name"), text(y, "addon_name")))
	})
	describeAddons(rows, catalog)
	return rows, nil
}

// resolveAddon selects an installed add-on by ID or name.
func (a *App) resolveAddon(ctx context.Context, target string) (*candidate, error) {
	if target == "" && a.noInput {
		return nil, fmt.Errorf("name the add-on; list them with `edka addons list`")
	}
	rows, err := a.addonRows(ctx, false)
	if err != nil {
		return nil, err
	}
	return a.pickScoped(ctx, "add-on", rows, target, "no add-ons installed; browse them with `edka addons catalog`", addonNames, addonDetail)
}

func addonNames(m map[string]any) []string { return []string{text(m, "addon_name")} }
func addonDetail(m map[string]any) string {
	return details(text(m, "version"), text(m, "status"), text(m, "cluster_name"))
}

// addonSteps names the operation behind an add-on status that is in progress.
var addonSteps = map[string]string{
	"installing":   "Installing",
	"upgrading":    "Updating",
	"uninstalling": "Uninstalling",
}

// addonStatus is an add-on's status, with its progress while an operation runs.
func addonStatus(m map[string]any) string {
	status := text(m, "status")
	if _, running := addonSteps[status]; running && m["progress"] != nil {
		return fmt.Sprintf("%s %d%%", status, number(m["progress"]))
	}
	return status
}

// addonError is the reason an add-on's last operation failed.
func addonError(m map[string]any) string {
	metadata, _ := m["addons"].(map[string]any)
	if encoded, ok := m["addons"].(string); ok {
		_ = json.Unmarshal([]byte(encoded), &metadata)
	}
	return text(metadata, "lastError")
}

// nameList joins the add-on names describeAddons stored under a key.
func nameList(v any) string {
	list, _ := v.([]string)
	return strings.Join(list, ", ")
}

var addonColumns = []ui.Column{
	ui.Field("NAME", "addon_name"),
	ui.Field("VERSION", "version"),
	{Header: "UPDATE", Value: func(m map[string]any) string {
		if m["update_available"] == true {
			return text(m, "catalog_version")
		}
		return ""
	}},
	{Header: "STATUS", Value: addonStatus},
	ui.Field("CATEGORY", "category"),
	ui.Field("CLUSTER", "cluster_name"),
}

// showAddon prints one add-on that describeAddons described.
func (a *App) showAddon(m map[string]any) error {
	if a.output != "table" {
		return ui.Render(a.Out, jsonBody(map[string]any{"data": m}), a.output, false)
	}
	update, problem, managed := "", "", ""
	if m["update_available"] == true {
		update = text(m, "catalog_version")
	}
	if text(m, "status") == "failed" {
		problem = addonError(m)
	}
	if area := text(m, "managed_from"); area != "" {
		managed = "Cluster > " + area + " in the console"
	}
	return ui.Fields(a.Out, [][2]string{
		{"Name", text(m, "addon_name")},
		{"Version", text(m, "version")},
		{"Update", update},
		{"Status", addonStatus(m)},
		{"Error", problem},
		{"Category", text(m, "category")},
		{"Required", yesNo(m["required"])},
		{"Depends on", nameList(m["depends_on"])},
		{"Used by", nameList(m["dependents"])},
		{"Managed from", managed},
		{"Cluster", text(m, "cluster_name")},
		{"Namespace", text(m, "namespace")},
		{"Installed", when(m["created_at"])},
		{"ID", text(m, "id")},
	}, a.color)
}

// showInstalled reads an add-on again from its cluster and prints it.
func (a *App) showInstalled(ctx context.Context, clusterID, name string) error {
	a.cluster = clusterID
	rows, err := a.addonRows(ctx, false)
	if err != nil {
		return err
	}
	row := recordWith(rows, "addon_name", name)
	if row == nil {
		return fmt.Errorf("add-on %q was %w; inspect `edka addons list`", name, errNotFound)
	}
	return a.showAddon(row)
}

// waitAddon prints an add-on operation's progress to stderr until the add-on
// is installed, or until it is gone when removing.
func (a *App) waitAddon(parent context.Context, clusterID, name string, removing bool, timeout time.Duration) error {
	cluster, err := safeID(clusterID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	p := a.startProgress()
	shown, status, reread := ui.Clean(name), "", false
	unfinished := func(err error) error {
		if ctx.Err() == nil {
			return err
		}
		// Edka accepted the operation, even when no read has succeeded yet.
		state := "has not finished"
		if status != "" {
			state = "is still " + ui.Clean(status)
		}
		return fmt.Errorf("%s %s: %w; the operation continues, follow it with `edka addons list`", shown, state, ctx.Err())
	}
	path := "/api/clusters/" + cluster + "/addons"
	for {
		response, err := a.poll(ctx, p, path, nil)
		if err != nil {
			return unfinished(err)
		}
		rows, err := rowsOf(path, response.Body)
		if err != nil {
			return err
		}
		row := recordWith(rows, "addon_name", name)
		switch {
		case row == nil && removing:
			return nil
		case row == nil:
			return fmt.Errorf("add-on %q was %w; inspect `edka addons list`", name, errNotFound)
		}
		status = text(row, "status")
		reason := addonError(row)
		switch {
		case status == "installed" && !removing:
			return nil
		// Edka saves the reason right after the status, so read once more for it.
		case status == "failed" && (reason != "" || reread):
			if reason != "" {
				reason = ": " + ui.Clean(reason)
			}
			if removing {
				return fmt.Errorf("uninstalling %s failed%s\nRetry with `edka addons uninstall %s`", shown, reason, shown)
			}
			return fmt.Errorf("%s failed%s\nRetry with `edka addons install %s`, or remove it with `edka addons uninstall %s`", shown, reason, shown, shown)
		case status == "failed":
			reread = true
		default:
			if step, running := addonSteps[status]; running && number(row["progress"]) > 0 {
				p.say("progress", fmt.Sprintf("%s %s: %d%%", step, shown, number(row["progress"])))
			}
		}
		if err := pause(ctx, pollInterval); err != nil {
			return unfinished(err)
		}
	}
}

// startUpdate asks Edka to update an installed add-on and returns the version
// the update installs.
func (a *App) startUpdate(ctx context.Context, cluster, name, target string) (*api.Response, string, error) {
	response, err := a.request(ctx, "PUT", "/api/clusters/"+cluster+"/addons", nil, jsonBody(map[string]any{"addonName": name, "version": target}))
	if err != nil {
		return nil, "", err
	}
	if queued, err := identityData(response.Body); err == nil {
		target = first(text(queued, "version"), target)
	}
	return response, target, nil
}

// updateAddons updates every add-on of the context cluster that has a newer
// catalog version, each after the add-ons it depends on. With wait, an update
// finishes before the next starts.
func (a *App) updateAddons(ctx context.Context, wait bool, timeout time.Duration) error {
	cluster, err := a.resolveCluster(ctx, "")
	if err != nil {
		return err
	}
	id, err := safeID(cluster.ID)
	if err != nil {
		return err
	}
	rows, err := a.addonRows(ctx, false)
	if err != nil {
		return err
	}
	shown := ui.Clean(cluster.Name)
	updates, placed := []map[string]any{}, map[string]bool{}
	var place func(map[string]any)
	place = func(row map[string]any) {
		name := text(row, "addon_name")
		if placed[name] {
			return
		}
		placed[name] = true
		dependencies, _ := row["depends_on"].([]string)
		for _, dependency := range dependencies {
			if other := recordWith(rows, "addon_name", dependency); other != nil {
				place(other)
			}
		}
		if row["update_available"] == true {
			updates = append(updates, row)
		}
	}
	for _, row := range rows {
		place(row)
	}
	// An add-on that can't take an update here is named with the reason, so
	// "up to date" never covers a failed add-on.
	skipped := []string{}
	for _, row := range rows {
		name, status := ui.Clean(text(row, "addon_name")), text(row, "status")
		switch {
		case status == "failed":
			skipped = append(skipped, fmt.Sprintf("  %s failed; retry with `edka addons install %s`", name, name))
		case status != "installed":
			skipped = append(skipped, fmt.Sprintf("  %s is %s", name, ui.Clean(status)))
		case row["managed_from"] != nil && text(row, "catalog_version") != "" && newerVersion(text(row, "catalog_version"), text(row, "version")):
			skipped = append(skipped, fmt.Sprintf("  %s is managed from Cluster > %s in the console", name, text(row, "managed_from")))
		}
	}
	others := "The add-ons"
	if len(skipped) > 0 {
		others = "The other add-ons"
		a.message("Not updated in cluster %s:\n%s", shown, strings.Join(skipped, "\n"))
	}
	if len(updates) == 0 {
		a.message("%s in cluster %s are up to date.", others, shown)
		if a.output != "table" {
			return a.renderRows(updates, addonColumns)
		}
		return nil
	}
	plan := []string{fmt.Sprintf("Updates in cluster %s:", shown)}
	for _, row := range updates {
		plan = append(plan, fmt.Sprintf("  %s %s to %s", ui.Clean(text(row, "addon_name")), ui.Clean(text(row, "version")), ui.Clean(text(row, "catalog_version"))))
		if note := addonUpdateNotes[text(row, "addon_name")]; note != "" {
			plan = append(plan, "    "+note)
		}
	}
	a.message("%s", strings.Join(plan, "\n"))
	count := fmt.Sprintf("%d add-ons", len(updates))
	if len(updates) == 1 {
		count = "1 add-on"
	}
	if err := a.confirm(fmt.Sprintf("Update %s in cluster %s", count, cluster.Name)); err != nil {
		return err
	}
	// notStarted names the updates a failure left unsent.
	notStarted := func(err error, rest []map[string]any) error {
		if len(rest) == 0 {
			return err
		}
		names := make([]string, len(rest))
		for i, row := range rest {
			names[i] = ui.Clean(text(row, "addon_name"))
		}
		return fmt.Errorf("%w\nNot started: %s", err, strings.Join(names, ", "))
	}
	started := []map[string]any{}
	for i, row := range updates {
		name := text(row, "addon_name")
		response, target, err := a.startUpdate(ctx, id, name, text(row, "catalog_version"))
		if err != nil {
			return notStarted(err, updates[i:])
		}
		queued, err := identityData(response.Body)
		if err != nil {
			queued = map[string]any{}
		}
		queued["addon_name"] = name
		started = append(started, queued)
		if !wait {
			a.message("✓ Updating %s to %s", ui.Clean(name), ui.Clean(target))
			continue
		}
		a.message("Updating %s to %s…", ui.Clean(name), ui.Clean(target))
		if err := a.waitAddon(ctx, cluster.ID, name, false, timeout); err != nil {
			return notStarted(err, updates[i+1:])
		}
		a.message("✓ Updated %s to %s", ui.Clean(name), ui.Clean(target))
	}
	if !wait {
		a.message("  Check progress: edka addons list")
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": started}), a.output, false)
		}
		return nil
	}
	rows, err = a.addonRows(ctx, false)
	if err != nil {
		return err
	}
	return a.renderRows(slices.DeleteFunc(rows, func(m map[string]any) bool {
		return recordWith(updates, "addon_name", text(m, "addon_name")) == nil
	}), addonColumns)
}

func (a *App) addAddons(root *cobra.Command) {
	addons := &cobra.Command{Use: "addons", Aliases: []string{"addon", "add-ons"}, Short: "Install, update and uninstall add-ons", GroupID: "resources", Example: "  edka addons list\n  edka addons list --outdated\n  edka addons update cert-manager --wait\n  edka addons catalog"}
	a.strictGroup(addons)
	var all, outdated bool
	list := &cobra.Command{Use: "list", Short: "List installed add-ons and their updates", Long: "List installed add-ons in the linked or selected cluster, or in every cluster\nwhen none is linked. Use --all to include every cluster.\n\nUPDATE shows the catalog version when it is newer than the installed one.\nUpdate an add-on with `edka addons update <add-on>`.", Args: cobra.NoArgs, Example: "  edka addons list\n  edka addons list --outdated --cluster production\n  edka addons list --all --json", RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.addonRows(cmd.Context(), all)
		if err != nil {
			return err
		}
		if outdated {
			rows = slices.DeleteFunc(rows, func(m map[string]any) bool { return m["update_available"] != true })
		}
		return a.renderRows(rows, addonColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List add-ons in every cluster, not only the linked one")
	list.Flags().BoolVar(&outdated, "outdated", false, "Only add-ons with a newer version in the catalog")
	get := &cobra.Command{Use: "get [add-on]", Short: "Show an installed add-on", Args: cobra.MaximumNArgs(1), Example: "  edka addons get cert-manager\n  edka addons get cert-manager --cluster production --json", RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.resolveAddon(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		return a.showAddon(c.Record)
	}}
	var category string
	catalog := &cobra.Command{Use: "catalog", Short: "List add-ons you can install", Args: cobra.NoArgs, Example: "  edka addons catalog\n  edka addons catalog --category monitoring", RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.objects(cmd.Context(), "/api/addons/catalog")
		if err != nil {
			return err
		}
		if category != "" {
			categories := []string{}
			for _, row := range rows {
				if name := text(row, "category"); name != "" && !slices.Contains(categories, name) {
					categories = append(categories, name)
				}
			}
			if !slices.Contains(categories, category) {
				slices.Sort(categories)
				return fmt.Errorf("no add-ons in category %q; choose one of %s", category, ui.Clean(strings.Join(categories, ", ")))
			}
			rows = slices.DeleteFunc(rows, func(m map[string]any) bool { return text(m, "category") != category })
		}
		return a.renderRows(rows, []ui.Column{ui.Field("NAME", "name"), ui.Field("CATEGORY", "category"), ui.Field("VERSION", "default_version"), {Header: "REQUIRED", Value: func(m map[string]any) string { return yesNo(m["required"]) }}, ui.Field("DESCRIPTION", "description")})
	}}
	catalog.Flags().StringVar(&category, "category", "", "Only add-ons in this category")
	a.completesFlag(catalog, "category", a.offers(a.catalogChoices("/api/addons/catalog", "category", "")))
	var version string
	var wait bool
	var waitTimeout time.Duration
	waitFlags := func(cmd *cobra.Command, until string) {
		cmd.Flags().BoolVar(&wait, "wait", false, "Wait until the add-on is "+until)
		cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 15*time.Minute, "Maximum wait for each add-on")
	}
	install := &cobra.Command{Use: "install <add-on>", Short: "Install an add-on from the catalog", Long: "Install an add-on in the linked or selected cluster, at the catalog version\nunless --version names another. Edka first installs the add-ons it depends on.\n\nInstalling an add-on that failed retries it. With --wait, progress goes to\nstderr until the add-on is installed.", Args: cobra.ExactArgs(1), Example: "  edka addons install reflector --wait\n  edka addons install cert-manager --cluster staging --version 1.16.2", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		ctx, name := cmd.Context(), args[0]
		if err := managedElsewhere(name); err != nil {
			return err
		}
		entries, err := a.objects(ctx, "/api/addons/catalog")
		if err != nil {
			return err
		}
		entry := recordWith(entries, "name", name)
		if entry == nil {
			return fmt.Errorf("add-on %q is not in the catalog; list them with `edka addons catalog`", name)
		}
		cluster, err := a.resolveCluster(ctx, "")
		if err != nil {
			return err
		}
		id, err := safeID(cluster.ID)
		if err != nil {
			return err
		}
		body := map[string]any{"addonName": name}
		if version != "" {
			body["version"] = version
		}
		response, err := a.request(ctx, "POST", "/api/clusters/"+id+"/addons", nil, jsonBody(body))
		var apiError *api.Error
		if errors.As(err, &apiError) && apiError.Status == 409 && apiError.Reason == "Addon already installed" {
			return fmt.Errorf("%w\nUpdate it with `edka addons update %s`", err, name)
		}
		if err != nil {
			return err
		}
		queued, err := identityData(response.Body)
		if err != nil {
			return err
		}
		what := fmt.Sprintf("%s in cluster %s", strings.TrimSpace(name+" "+ui.Clean(text(queued, "version"))), ui.Clean(cluster.Name))
		dependencies := ""
		if required := addonDependencies(entry); len(required) > 0 {
			dependencies = "\n  It depends on " + ui.Clean(strings.Join(required, ", ")) + ". Edka installs the missing ones first."
		}
		if !wait {
			a.message("✓ Installing %s%s\n  Next: edka addons get %s", what, dependencies, name)
			if a.output != "table" {
				return a.render(response)
			}
			return nil
		}
		a.message("Installing %s…%s", what, dependencies)
		if err := a.waitAddon(ctx, cluster.ID, name, false, waitTimeout); err != nil {
			return err
		}
		a.message("✓ Installed %s in cluster %s", name, ui.Clean(cluster.Name))
		return a.showInstalled(ctx, cluster.ID, name)
	}}
	install.Flags().StringVar(&version, "version", "", "Chart version to install (default: the catalog version)")
	waitFlags(install, "installed")
	var updateAll bool
	update := &cobra.Command{Use: "update [add-on]", Aliases: []string{"upgrade"}, Short: "Update add-ons to the catalog version", Long: "Update an installed add-on to the catalog version, or to the one --version\nnames. The add-on keeps its configuration. The command asks for confirmation\nand names both versions; --yes skips it. When the add-on already runs the\ncatalog version, nothing is sent.\n\n--all updates every add-on in the linked or selected cluster that has a newer\ncatalog version. It lists them with both versions and asks once. An add-on\nupdates after the add-ons it depends on. The command names the add-ons it\nleaves out and why: one that failed or is mid-operation, and one the console\nmanages from Registries, Gateway or Actions.\n\nWith --wait, progress goes to stderr until the update is installed. With --all,\neach update finishes before the next starts, and a failure stops the rest.", Args: cobra.MaximumNArgs(1), Example: "  edka addons update cert-manager --wait\n  edka addons update cert-manager --cluster production --version 1.16.2 --yes\n  edka addons update --all --cluster production --wait", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		switch {
		case wait && waitTimeout <= 0:
			return fmt.Errorf("wait-timeout must be positive")
		case updateAll && len(args) > 0:
			return fmt.Errorf("pass an add-on or --all, not both")
		case updateAll && version != "":
			return fmt.Errorf("--version needs one add-on; --all updates each add-on to its catalog version")
		case updateAll:
			return a.updateAddons(ctx, wait, waitTimeout)
		case len(args) == 0:
			return fmt.Errorf("name the add-on, or pass --all for every add-on with an update")
		}
		c, err := a.resolveAddon(ctx, args[0])
		if err != nil {
			return err
		}
		if err := managedElsewhere(c.Name); err != nil {
			return err
		}
		name, cluster, current := ui.Clean(c.Name), ui.Clean(text(c.Record, "cluster_name")), text(c.Record, "version")
		switch status := text(c.Record, "status"); status {
		case "installed":
		case "failed":
			reason := ""
			if problem := addonError(c.Record); problem != "" {
				reason = ": " + ui.Clean(problem)
			}
			return fmt.Errorf("%s failed in cluster %s%s\nRetry with `edka addons install %s`, or remove it with `edka addons uninstall %s`", name, cluster, reason, name, name)
		default:
			return fmt.Errorf("%s is %s in cluster %s; follow it with `edka addons get %s`", name, ui.Clean(status), cluster, name)
		}
		target := first(version, text(c.Record, "catalog_version"))
		switch {
		case target == "":
			return fmt.Errorf("the catalog has no version of %s; pass --version", name)
		case target == current || version == "" && !newerVersion(target, current):
			a.message("%s %s is up to date in cluster %s.", name, ui.Clean(current), cluster)
			if a.output != "table" {
				return a.showAddon(c.Record)
			}
			return nil
		}
		id, err := safeID(text(c.Record, "cluster_id"))
		if err != nil {
			return err
		}
		if note := addonUpdateNotes[c.Name]; note != "" {
			a.message("%s", note)
		}
		if err := a.confirm(fmt.Sprintf("Update add-on %s in cluster %s from %s to %s", c.Name, text(c.Record, "cluster_name"), current, target)); err != nil {
			return err
		}
		response, target, err := a.startUpdate(ctx, id, c.Name, target)
		if err != nil {
			return err
		}
		if !wait {
			a.message("✓ Updating %s to %s in cluster %s\n  Check progress: edka addons get %s", name, ui.Clean(target), cluster, name)
			if a.output != "table" {
				return a.render(response)
			}
			return nil
		}
		a.message("Updating %s to %s in cluster %s…", name, ui.Clean(target), cluster)
		if err := a.waitAddon(ctx, text(c.Record, "cluster_id"), c.Name, false, waitTimeout); err != nil {
			return err
		}
		a.message("✓ Updated %s to %s in cluster %s", name, ui.Clean(target), cluster)
		return a.showInstalled(ctx, text(c.Record, "cluster_id"), c.Name)
	}}
	update.Flags().StringVar(&version, "version", "", "Chart version to update to (default: the catalog version)")
	update.Flags().BoolVar(&updateAll, "all", false, "Update every add-on in the cluster that has a newer catalog version")
	waitFlags(update, "updated")
	uninstall := &cobra.Command{Use: "uninstall <add-on>", Short: "Uninstall an add-on from its cluster", Long: "Uninstall an add-on after confirmation, or with --yes. Edka refuses to\nuninstall a required add-on, or one that another add-on, an app, a database or\na domain still uses. The error names what uses it.\n\nWith --wait, progress goes to stderr until the add-on is gone.", Args: cobra.ExactArgs(1), Example: "  edka addons uninstall reflector\n  edka addons uninstall reflector --cluster staging --yes --wait", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		ctx := cmd.Context()
		c, err := a.resolveAddon(ctx, args[0])
		if err != nil {
			return err
		}
		if err := managedElsewhere(c.Name); err != nil {
			return err
		}
		name, cluster := ui.Clean(c.Name), ui.Clean(text(c.Record, "cluster_name"))
		// Edka queues an uninstall beside an operation that is still running.
		if status := text(c.Record, "status"); addonSteps[status] != "" {
			return fmt.Errorf("%s is %s in cluster %s; follow it with `edka addons get %s`", name, ui.Clean(status), cluster, name)
		}
		if c.Record["required"] == true {
			return fmt.Errorf("%s is a required add-on and can't be uninstalled", name)
		}
		if dependents := nameList(c.Record["dependents"]); dependents != "" {
			return fmt.Errorf("%s is used by %s in cluster %s; uninstall those add-ons first", name, ui.Clean(dependents), cluster)
		}
		id, err := safeID(text(c.Record, "cluster_id"))
		if err != nil {
			return err
		}
		if err := a.confirm(fmt.Sprintf("Uninstall add-on %s from cluster %s", strings.TrimSpace(c.Name+" "+text(c.Record, "version")), text(c.Record, "cluster_name"))); err != nil {
			return err
		}
		response, err := a.request(ctx, "DELETE", "/api/clusters/"+id+"/addons", nil, jsonBody(map[string]any{"addonName": c.Name}))
		if err != nil {
			return err
		}
		if wait {
			a.message("Uninstalling %s from cluster %s…", name, cluster)
			if err := a.waitAddon(ctx, text(c.Record, "cluster_id"), c.Name, true, waitTimeout); err != nil {
				return err
			}
			a.message("✓ Uninstalled %s from cluster %s", name, cluster)
		} else {
			a.message("✓ Uninstalling %s from cluster %s\n  Check progress: edka addons list", name, cluster)
		}
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	waitFlags(uninstall, "gone")
	a.completes(a.scopedChoices("addons", addonNames, addonDetail), get, update, uninstall)
	a.completes(a.catalogChoices("/api/addons/catalog", "name", "category"), install)
	addons.AddCommand(list, get, catalog, install, update, uninstall)
	root.AddCommand(addons)
}
