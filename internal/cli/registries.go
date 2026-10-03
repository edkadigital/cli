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

// ownRegistry is the name Edka stores for a cluster's own registry as its default.
const ownRegistry = "__managed_in_cluster_zot__"

// registryTypes are the kinds of registry Edka stores credentials for.
var registryTypes = []string{"docker-hub", "github-registry", "google-artifact", "aws-ecr", "custom"}

// registryUse says how a cluster uses a registry: whether it is the default,
// and the state of its pull secrets.
func registryUse(m map[string]any) string {
	use, ok := m["cluster_use"].(map[string]any)
	if !ok {
		return ""
	}
	state := first(text(use, "syncStatus"), "applied")
	if reason := text(use, "lastError"); reason != "" {
		state += ": " + reason
	}
	if use["isDefault"] == true {
		return "default, " + state
	}
	return state
}

// clusterRegistries reads the registries a cluster uses, and its default one.
func (a *App) clusterRegistries(ctx context.Context, cluster string) (registries []map[string]any, fallback string, err error) {
	response, err := a.request(ctx, "GET", "/api/clusters/"+cluster+"/registries", nil, nil)
	if err != nil {
		return nil, "", err
	}
	return usedRegistries(response.Body)
}

// usedRegistries returns the registries in a cluster's answer, and its default one.
func usedRegistries(body []byte) ([]map[string]any, string, error) {
	var answer struct {
		Default    string           `json:"defaultRegistry"`
		Registries []map[string]any `json:"registries"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, "", fmt.Errorf("unexpected response; rerun with --json to inspect it")
	}
	return answer.Registries, answer.Default, nil
}

// registryClusters lists the clusters that use a registry, by name, and marks
// the ones it is the default of.
func (a *App) registryClusters(ctx context.Context, name string) ([]string, error) {
	rows, err := a.clusterScopedLists(ctx, true, "registries", func(_ string, body []byte) ([]map[string]any, error) {
		registries, _, err := usedRegistries(body)
		return registries, err
	})
	if err != nil {
		return nil, err
	}
	clusters := []string{}
	for _, row := range rows {
		if text(row, "name") != name {
			continue
		}
		cluster := text(row, "cluster_name")
		if row["isDefault"] == true {
			cluster += " (default)"
		}
		clusters = append(clusters, cluster)
	}
	return clusters, nil
}

// registryPath is /api/registry/:name.
func registryPath(name string) (string, error) {
	id, err := safeID(name)
	if err != nil {
		return "", fmt.Errorf("invalid registry name %q", name)
	}
	return "/api/registry/" + id, nil
}

// registryPassword reads a registry's password at a hidden prompt, or from a
// stdin that is not a terminal. It is never an argument, which shell history
// and the process list would keep.
func (a *App) registryPassword(ctx context.Context, name, username string) (string, error) {
	if !ui.InputTerminal(a.In) {
		return a.stdinSecret("the password")
	}
	if a.noInput {
		return "", fmt.Errorf("pipe the password on stdin: printf %%s \"$PASSWORD\" | edka registries add %s", name)
	}
	return a.promptSecret(ctx, fmt.Sprintf("Password or token of %s for registry %s: ", ui.Clean(username), ui.Clean(name)), "the password")
}

// bytesOf prints a size in the binary units Kubernetes uses.
func bytesOf(v any) string {
	n, ok := v.(float64)
	if !ok || n < 0 {
		return ""
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n, i = n/1024, i+1
	}
	if i == 0 || n >= 100 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", n), ".0") + " " + units[i]
}

func (a *App) addRegistries(root *cobra.Command) {
	registries := &cobra.Command{Use: "registries", Aliases: []string{"registry"}, Short: "Store registry credentials and browse a cluster's registry", Long: "A registry here is the credentials of a container registry, stored for the\norganization: Docker Hub, GitHub, Google Artifact Registry, Amazon ECR or any\nother. A cluster pulls private images from a registry once the registry is\napplied to it, which creates its pull secret in every namespace.\n\nA cluster's default registry is the one a Git deployment builds to and pulls\nfrom when it names none. It is an applied registry, or the cluster's own\nregistry, which `edka registries images` shows.\n\napply, remove, default, images, tags and delete-tags act on the linked cluster,\nor on the one --cluster names.", GroupID: "resources", Example: "  edka registries add ghcr --type github-registry --url ghcr.io --username octocat\n  edka registries apply ghcr\n  edka registries default ghcr\n  edka registries list\n  edka registries images\n  edka registries tags api"}
	a.strictGroup(registries)

	list := &cobra.Command{Use: "list", Short: "List the organization's registries", Long: "List the registries of the organization. With a linked or selected cluster, the\nIN CLUSTER column says how that cluster uses each one: whether it is the\ndefault, and the state of its pull secrets. A registry the cluster uses whose\ncredentials were deleted is listed with the type Edka last knew.", Args: cobra.NoArgs, Example: "  edka registries list\n  edka registries list --cluster production --json", RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		rows, err := a.objects(ctx, "/api/registry")
		if err != nil {
			return err
		}
		columns := []ui.Column{ui.Field("NAME", "name"), ui.Field("TYPE", "type"), ui.Field("URL", "url"), ui.Field("USERNAME", "username")}
		if a.cluster != "" {
			cluster, err := a.clusterID(ctx)
			if err != nil {
				return err
			}
			used, _, err := a.clusterRegistries(ctx, cluster)
			if err != nil {
				return err
			}
			for _, use := range used {
				row := recordWith(rows, "name", text(use, "name"))
				if row == nil {
					row = map[string]any{"name": text(use, "name"), "type": text(use, "type"), "url": use["url"]}
					rows = append(rows, row)
				}
				row["cluster_use"] = use
			}
			columns = append(columns, ui.Column{Header: "IN CLUSTER", Value: registryUse})
		}
		return a.renderRows(rows, columns)
	}}
	get := &cobra.Command{Use: "get <registry>", Short: "Show a registry and the clusters that use it", Args: cobra.ExactArgs(1), Example: "  edka registries get ghcr\n  edka registries get ghcr --json", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		path, err := registryPath(args[0])
		if err != nil {
			return err
		}
		response, err := a.request(ctx, "GET", path, nil, nil)
		if err != nil {
			return err
		}
		m, err := identityData(response.Body)
		if err != nil {
			return err
		}
		clusters, err := a.registryClusters(ctx, args[0])
		if err != nil {
			return err
		}
		m["clusters"] = clusters
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": m}), a.output, false)
		}
		return ui.Fields(a.Out, [][2]string{
			{"Name", text(m, "name")},
			{"Type", text(m, "type")},
			{"URL", text(m, "url")},
			{"Username", text(m, "username")},
			{"Email", text(m, "email")},
			{"AWS region", text(m, "awsRegion")},
			{"Clusters", strings.Join(clusters, ", ")},
			{"Saved", when(first(text(m, "timestamp"), text(m, "created_at")))},
		}, a.color)
	}}

	var kind, address, username, email, region string
	var replace bool
	add := &cobra.Command{Use: "add <name>", Short: "Store the credentials of a registry", Long: "Store the credentials of a container registry for the organization. The\npassword, or the token that stands for it, is read at a hidden prompt, or from\nstdin when stdin is not a terminal. It is never an argument, because shell\nhistory keeps arguments and other local users can read them in the process\nlist.\n\nEvery type but docker-hub needs --url. For aws-ecr the username is the access\nkey ID and the password the secret access key; --aws-region is for a URL that\ndoes not hold the region.\n\n--replace stores new credentials for a registry that exists. Flags left out\nkeep their values, the password is asked again, and the clusters that use the\nregistry get the new credentials.", Args: cobra.ExactArgs(1), Example: "  edka registries add ghcr --type github-registry --url ghcr.io --username octocat\n  edka registries add hub --type docker-hub --username acme\n  printf %s \"$GHCR_TOKEN\" | edka registries add ghcr --replace", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, name := cmd.Context(), args[0]
		path, err := registryPath(name)
		if err != nil {
			return err
		}
		response, err := a.request(ctx, "GET", path+"/exists", nil, nil)
		if err != nil {
			return err
		}
		answer, err := identityData(response.Body)
		if err != nil {
			return err
		}
		exists := answer["exists"] == true
		switch {
		case exists && !replace:
			return fmt.Errorf("registry %s exists; store new credentials for it with --replace", name)
		case exists:
			// The flags left out keep the values of the registry.
			current, err := a.request(ctx, "GET", path, nil, nil)
			if err != nil {
				return err
			}
			stored, err := identityData(current.Body)
			if err != nil {
				return err
			}
			kind, address, username = first(kind, text(stored, "type")), first(address, text(stored, "url")), first(username, text(stored, "username"))
			email, region = first(email, text(stored, "email")), first(region, text(stored, "awsRegion"))
		}
		switch {
		case kind == "":
			return fmt.Errorf("pass --type with one of %s", strings.Join(registryTypes, ", "))
		case !slices.Contains(registryTypes, kind):
			return fmt.Errorf("unknown registry type %q; choose one of %s", kind, strings.Join(registryTypes, ", "))
		case username == "":
			return fmt.Errorf("pass --username")
		case kind != "docker-hub" && address == "":
			return fmt.Errorf("a %s registry needs --url, such as ghcr.io", kind)
		}
		password, err := a.registryPassword(ctx, name, username)
		if err != nil {
			return err
		}
		body := map[string]any{"name": name, "type": kind, "username": username, "password": password}
		for key, value := range map[string]string{"url": address, "email": email, "awsRegion": region} {
			if value != "" {
				body[key] = value
			}
		}
		saved, err := a.request(ctx, "POST", "/api/registry", nil, jsonBody(body))
		if err != nil {
			return err
		}
		if exists {
			a.message("✓ Stored new credentials for registry %s\n  The clusters that use it get them.", ui.Clean(name))
		} else {
			a.message("✓ Stored registry %s\n  Next: edka registries apply %s", ui.Clean(name), shellJoin([]string{name}))
		}
		if a.output != "table" {
			return a.render(saved)
		}
		return nil
	}}
	add.Flags().StringVar(&kind, "type", "", "Kind of registry: "+strings.Join(registryTypes, ", "))
	add.Flags().StringVar(&address, "url", "", "Address of the registry, such as ghcr.io")
	add.Flags().StringVar(&username, "username", "", "User the password or token belongs to")
	add.Flags().StringVar(&email, "email", "", "Email address of a docker-hub account")
	add.Flags().StringVar(&region, "aws-region", "", "Region of an aws-ecr registry whose URL does not hold it")
	add.Flags().BoolVar(&replace, "replace", false, "Store new credentials for a registry that exists")
	a.completesFlag(add, "type", values(registryTypes...))

	remove := &cobra.Command{Use: "delete <registry>", Short: "Delete a registry's credentials", Long: "Delete the credentials of a registry from the organization. A registry that a\ncluster still uses is refused: take it from each cluster first, with\n`edka registries remove <registry> --cluster <cluster>`.", Args: cobra.ExactArgs(1), Example: "  edka registries delete ghcr\n  edka registries delete ghcr --yes", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, name := cmd.Context(), args[0]
		path, err := registryPath(name)
		if err != nil {
			return err
		}
		if _, err := a.request(ctx, "GET", path, nil, nil); err != nil {
			return err
		}
		clusters, err := a.registryClusters(ctx, name)
		if err != nil {
			return err
		}
		if len(clusters) > 0 {
			return fmt.Errorf("registry %s is used by %s; take it from each cluster first with `edka registries remove %s --cluster <cluster>`", name, plural(len(clusters), "cluster ", "clusters ")+ui.Clean(strings.Join(clusters, ", ")), shellJoin([]string{name}))
		}
		if err := a.confirm("Delete registry " + name + " and its credentials"); err != nil {
			return err
		}
		response, err := a.request(ctx, "DELETE", path, nil, nil)
		if err != nil {
			return err
		}
		a.message("✓ Deleted registry %s", ui.Clean(name))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}

	// applied sends a registry to the context cluster, or takes it from it.
	applied := func(use, short, long, example string, apply bool) *cobra.Command {
		return &cobra.Command{Use: use + " <registry>", Short: short, Long: long, Args: cobra.ExactArgs(1), Example: example, RunE: func(cmd *cobra.Command, args []string) error {
			ctx, name := cmd.Context(), args[0]
			path, err := registryPath(name)
			if err != nil {
				return err
			}
			cluster, err := a.resolveCluster(ctx, "")
			if err != nil {
				return err
			}
			id, err := safeID(cluster.ID)
			if err != nil {
				return err
			}
			method := "POST"
			if !apply {
				method = "DELETE"
				if err := a.confirm(fmt.Sprintf("Remove registry %s from cluster %s, with its pull secret in every namespace", name, cluster.Name)); err != nil {
					return err
				}
			}
			response, err := a.request(ctx, method, path+"/apply/"+id, nil, nil)
			if err != nil {
				return err
			}
			if apply {
				a.message("✓ Applying registry %s to cluster %s\n  Edka creates its pull secret in every namespace. Check it with `edka registries list --cluster %s`", ui.Clean(name), ui.Clean(cluster.Name), ui.Clean(cluster.Name))
			} else {
				a.message("✓ Removing registry %s from cluster %s", ui.Clean(name), ui.Clean(cluster.Name))
			}
			if a.output != "table" {
				return a.render(response)
			}
			return nil
		}}
	}
	apply := applied("apply", "Let a cluster pull from a registry", "Apply a registry to the linked or selected cluster. Edka creates the registry's\npull secret in every namespace.", "  edka registries apply ghcr\n  edka registries apply ghcr --cluster production", true)
	unapply := applied("remove", "Take a registry from a cluster", "Remove a registry from the linked or selected cluster. Edka deletes its pull\nsecret from every namespace, so the cluster can no longer pull the private\nimages of that registry. A registry that was the cluster's default leaves the\ncluster without one. The credentials stay in the organization.", "  edka registries remove ghcr\n  edka registries remove ghcr --cluster production --yes", false)

	var own, none bool
	defaultCmd := &cobra.Command{Use: "default [registry]", Short: "Show or set a cluster's default registry", Long: "Without an argument, show the default registry of the linked or selected\ncluster: the one a Git deployment builds to and pulls from when it names none.\n\nWith a registry, make it the default, which also applies it to the cluster.\n--own makes the cluster's own registry the default, and --none leaves the\ncluster without one.", Args: cobra.MaximumNArgs(1), Example: "  edka registries default\n  edka registries default ghcr\n  edka registries default --own\n  edka registries default --none", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if len(args) == 1 && (own || none) || own && none {
			return fmt.Errorf("pass a registry, --own or --none, not more than one")
		}
		cluster, err := a.resolveCluster(ctx, "")
		if err != nil {
			return err
		}
		id, err := safeID(cluster.ID)
		if err != nil {
			return err
		}
		clusterName := ui.Clean(cluster.Name)
		if len(args) == 0 && !own && !none {
			_, current, err := a.clusterRegistries(ctx, id)
			if err != nil {
				return err
			}
			if a.output != "table" {
				return ui.Render(a.Out, jsonBody(map[string]any{"registry": current, "own": current == ownRegistry}), a.output, false)
			}
			switch current {
			case "":
				a.message("Cluster %s has no default registry.", clusterName)
			case ownRegistry:
				fmt.Fprintln(a.Out, "the cluster's own registry")
			default:
				fmt.Fprintln(a.Out, ui.Clean(current))
			}
			return nil
		}
		var target any
		switch {
		case own:
			target = ownRegistry
		case len(args) == 1:
			if _, err := registryPath(args[0]); err != nil {
				return err
			}
			target = args[0]
		}
		response, err := a.request(ctx, "POST", "/api/clusters/"+id+"/registries/default", nil, jsonBody(map[string]any{"registry": target}))
		if err != nil {
			return err
		}
		switch {
		case none:
			a.message("✓ Cluster %s has no default registry", clusterName)
		case own:
			a.message("✓ Cluster %s uses its own registry by default", clusterName)
		default:
			a.message("✓ Cluster %s uses registry %s by default", clusterName, ui.Clean(args[0]))
		}
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	defaultCmd.Flags().BoolVar(&own, "own", false, "Make the cluster's own registry the default")
	defaultCmd.Flags().BoolVar(&none, "none", false, "Leave the cluster without a default registry")

	// overview reads the context cluster's own registry.
	overview := func(ctx context.Context) (*candidate, map[string]any, error) {
		cluster, err := a.resolveCluster(ctx, "")
		if err != nil {
			return nil, nil, err
		}
		id, err := safeID(cluster.ID)
		if err != nil {
			return nil, nil, err
		}
		response, err := a.request(ctx, "GET", "/api/clusters/"+id+"/registries/in-cluster/overview", nil, nil)
		if err != nil {
			return nil, nil, err
		}
		m, err := identityData(response.Body)
		if err != nil {
			return nil, nil, err
		}
		if m["installed"] != true {
			return nil, nil, fmt.Errorf("cluster %s has no registry of its own; install it from Registries in the Edka console", ui.Clean(cluster.Name))
		}
		return cluster, m, nil
	}
	images := &cobra.Command{Use: "images", Short: "Show a cluster's own registry and the images it holds", Long: "Show the registry that runs in the linked or selected cluster: its address\ninside the cluster, its pods, its storage, and each repository with how many\ntags it has. `edka registries tags <repository>` lists the tags.", Args: cobra.NoArgs, Example: "  edka registries images\n  edka registries images --cluster production --json", RunE: func(cmd *cobra.Command, _ []string) error {
		cluster, m, err := overview(cmd.Context())
		if err != nil {
			return err
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": m}), a.output, false)
		}
		pods, _ := m["pods"].(map[string]any)
		volume, _ := m["volume"].(map[string]any)
		storage := ""
		if volume["capacityBytes"] != nil {
			storage = fmt.Sprintf("%s of %s used", bytesOf(volume["usedBytes"]), bytesOf(volume["capacityBytes"]))
			if volume["usagePercent"] != nil {
				storage += fmt.Sprintf(" (%d%%)", number(volume["usagePercent"]))
			}
		}
		warnings := []string{}
		for _, item := range asList(m["warnings"]) {
			warnings = append(warnings, ui.Text(item))
		}
		if err := ui.Fields(a.Out, [][2]string{
			{"Registry", text(m, "endpoint")},
			{"Status", text(m, "status")},
			{"Pods", fmt.Sprintf("%d of %d ready", number(pods["ready"]), number(pods["total"]))},
			{"Storage", storage},
			{"Warnings", strings.Join(warnings, "; ")},
			{"Cluster", cluster.Name},
		}, a.color); err != nil {
			return err
		}
		repositories := []map[string]any{}
		for _, item := range asList(m["repositories"]) {
			if repository, ok := item.(map[string]any); ok {
				repositories = append(repositories, repository)
			}
		}
		if len(repositories) == 0 {
			return nil
		}
		fmt.Fprintln(a.Out)
		return ui.Table(a.Out, []ui.Column{ui.Field("REPOSITORY", "name"), {Header: "TAGS", Value: func(r map[string]any) string {
			count := fmt.Sprint(number(r["tagCount"]))
			if r["tagCountTruncated"] == true {
				count += "+"
			}
			return count
		}}, {Header: "SAMPLE", Value: func(r map[string]any) string {
			sample := []string{}
			for _, tag := range asList(r["tags"]) {
				if len(sample) == 3 {
					break
				}
				sample = append(sample, ui.Text(tag))
			}
			return strings.Join(sample, ", ")
		}}}, repositories, a.color)
	}}
	// tagsOf reads the tags of a repository in the context cluster's own registry.
	tagsOf := func(ctx context.Context, repository string) (*candidate, map[string]any, error) {
		cluster, err := a.resolveCluster(ctx, "")
		if err != nil {
			return nil, nil, err
		}
		id, err := safeID(cluster.ID)
		if err != nil {
			return nil, nil, err
		}
		response, err := a.request(ctx, "GET", "/api/clusters/"+id+"/registries/in-cluster/tags", url.Values{"repository": {repository}}, nil)
		if err != nil {
			return nil, nil, err
		}
		m, err := identityData(response.Body)
		return cluster, m, err
	}
	tags := &cobra.Command{Use: "tags <repository>", Short: "List the tags of a repository in a cluster's own registry", Args: cobra.ExactArgs(1), Example: "  edka registries tags api\n  edka registries tags acme/api --cluster production --json", RunE: func(cmd *cobra.Command, args []string) error {
		_, m, err := tagsOf(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(m), a.output, false)
		}
		if m["truncated"] == true {
			a.message("The repository has more tags than Edka lists.")
		}
		rows := []map[string]any{}
		for _, tag := range asList(m["tags"]) {
			rows = append(rows, map[string]any{"tag": tag})
		}
		return ui.Table(a.Out, []ui.Column{ui.Field("TAG", "tag")}, rows, a.color)
	}}

	var force bool
	deleteTags := &cobra.Command{Use: "delete-tags <repository> <tag>...", Short: "Delete tags from a cluster's own registry", Long: "Delete tags of a repository in the registry that runs in the linked or selected\ncluster, after confirmation or with --yes. A deployment that runs a deleted\nimage keeps running, and can no longer pull it.\n\nThe registry deletes an image, and with it every tag of that image. When a tag\nyou did not name shares an image with one you named, Edka refuses, and --force\ndeletes those tags too.", Args: cobra.MinimumNArgs(2), Example: "  edka registries delete-tags api v1 v2\n  edka registries delete-tags api latest --force --yes", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, repository, selected := cmd.Context(), args[0], args[1:]
		if len(selected) > 100 {
			return fmt.Errorf("Edka deletes at most 100 tags in one request; %d were named", len(selected))
		}
		cluster, err := a.resolveCluster(ctx, "")
		if err != nil {
			return err
		}
		id, err := safeID(cluster.ID)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("Delete %s of %s from the registry of cluster %s: %s", plural(len(selected), "1 tag", fmt.Sprintf("%d tags", len(selected))), repository, cluster.Name, strings.Join(selected, ", "))
		if force {
			label += ", and every tag that shares an image with them"
		}
		if err := a.confirm(label); err != nil {
			return err
		}
		response, err := a.request(ctx, "DELETE", "/api/clusters/"+id+"/registries/in-cluster/tags", nil, jsonBody(map[string]any{"repository": repository, "tags": selected, "force": force}))
		if err != nil {
			if strings.Contains(err.Error(), "share") && !force {
				return fmt.Errorf("%w\nTo delete those tags too, rerun with --force", err)
			}
			return err
		}
		m, err := identityData(response.Body)
		if err != nil {
			return err
		}
		names := func(key string) string {
			list := []string{}
			for _, tag := range asList(m[key]) {
				list = append(list, ui.Text(tag))
			}
			return strings.Join(list, ", ")
		}
		if deleted := names("deletedTags"); deleted != "" {
			a.message("✓ Deleted from %s: %s", ui.Clean(repository), deleted)
		}
		if implicit := names("implicitlyDeletedTags"); implicit != "" {
			a.message("  Deleted with them, as tags of the same images: %s", implicit)
		}
		if a.output != "table" {
			if err := a.render(response); err != nil {
				return err
			}
		}
		if failed := names("failedTags"); failed != "" {
			return fmt.Errorf("the registry did not delete %s", failed)
		}
		return nil
	}}
	deleteTags.Flags().BoolVar(&force, "force", false, "Also delete the tags that share an image with the named ones")

	names := func(ctx context.Context) ([]candidate, error) {
		rows, err := a.objects(ctx, "/api/registry")
		choices := []candidate{}
		for _, row := range rows {
			choices = append(choices, candidate{Name: text(row, "name"), Detail: details(text(row, "type"), text(row, "url"))})
		}
		return choices, err
	}
	used := func(ctx context.Context) ([]candidate, error) {
		cluster, err := a.clusterID(ctx)
		if err != nil {
			return nil, err
		}
		rows, _, err := a.clusterRegistries(ctx, cluster)
		choices := []candidate{}
		for _, row := range rows {
			choices = append(choices, candidate{Name: text(row, "name"), Detail: details(text(row, "type"), text(row, "syncStatus"))})
		}
		return choices, err
	}
	repositories := func(ctx context.Context) ([]candidate, error) {
		_, m, err := overview(ctx)
		choices := []candidate{}
		for _, item := range asList(m["repositories"]) {
			if repository, ok := item.(map[string]any); ok {
				choices = append(choices, candidate{Name: text(repository, "name"), Detail: plural(number(repository["tagCount"]), "1 tag", fmt.Sprintf("%d tags", number(repository["tagCount"])))})
			}
		}
		return choices, err
	}
	a.completes(names, get, remove, apply, defaultCmd)
	a.completes(used, unapply)
	a.completes(repositories, tags)
	// After the repository, delete-tags completes its tags.
	deleteTags.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return a.offers(repositories)(cmd, args, toComplete)
		}
		return a.offers(func(ctx context.Context) ([]candidate, error) {
			_, m, err := tagsOf(ctx, args[0])
			choices := []candidate{}
			for _, tag := range asList(m["tags"]) {
				if name := ui.Text(tag); !slices.Contains(args[1:], name) {
					choices = append(choices, candidate{Name: name})
				}
			}
			return choices, err
		})(cmd, args, toComplete)
	}
	registries.AddCommand(list, get, add, remove, apply, unapply, defaultCmd, images, tags, deleteTags)
	root.AddCommand(registries)
}
