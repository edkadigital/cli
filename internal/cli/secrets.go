package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	// secretName is Kubernetes' rule for the name of a Secret.
	secretName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$`)
	// secretKey is Kubernetes' rule for a key of a Secret.
	secretKey = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

// secretKeys are the keys of a secret, sorted.
func secretKeys(m map[string]any) []string {
	keys := []string{}
	for _, key := range asList(m["keys"]) {
		if s, ok := key.(string); ok {
			keys = append(keys, s)
		}
	}
	slices.Sort(keys)
	return keys
}

var secretColumns = []ui.Column{
	ui.Field("NAME", "name"),
	ui.Field("NAMESPACE", "namespace"),
	{Header: "KEYS", Value: func(m map[string]any) string { return strings.Join(secretKeys(m), ", ") }},
	ui.Field("TYPE", "type"),
	{Header: "CREATED", Value: func(m map[string]any) string { return when(m["createdAt"]) }},
	ui.Field("CLUSTER", "cluster_name"),
}

// secretTarget is one secret of the context cluster: its name, its namespace
// and its route.
type secretTarget struct {
	cluster   *candidate
	clusterID string
	name      string
	namespace string
}

func (t secretTarget) path() string      { return "/api/clusters/" + t.clusterID + "/secrets/" + t.name }
func (t secretTarget) query() url.Values { return url.Values{"namespace": {t.namespace}} }

// in says where the secret is, for a message.
func (t secretTarget) in() string {
	return fmt.Sprintf("namespace %s of cluster %s", t.namespace, ui.Clean(t.cluster.Name))
}

// secretTarget names a secret in the context cluster.
func (a *App) secretTarget(ctx context.Context, name, namespace string) (secretTarget, error) {
	if len(name) > 253 || !secretName.MatchString(name) {
		return secretTarget{}, fmt.Errorf("secret name %q must be lowercase letters, digits, hyphens and dots, starting and ending with a letter or digit", name)
	}
	if _, err := safeID(namespace); err != nil {
		return secretTarget{}, fmt.Errorf("invalid namespace %q", namespace)
	}
	cluster, err := a.resolveCluster(ctx, "")
	if err != nil {
		return secretTarget{}, err
	}
	id, err := safeID(cluster.ID)
	if err != nil {
		return secretTarget{}, err
	}
	return secretTarget{cluster: cluster, clusterID: id, name: name, namespace: namespace}, nil
}

// readSecret reads the metadata of a secret: its keys and never its values.
// It returns nil when the namespace has no such secret that Edka manages. The
// error then says which namespace has one of that name, when one does.
func (a *App) readSecret(ctx context.Context, t secretTarget) (map[string]any, error) {
	response, err := a.request(ctx, "GET", t.path(), t.query(), nil)
	var apiError *api.Error
	if errors.As(err, &apiError) && apiError.Status == 404 {
		return nil, a.secretMissing(ctx, t)
	}
	if err != nil {
		return nil, err
	}
	return record(response.Body, "data")
}

// secretMissing explains that a namespace has no secret of a name.
func (a *App) secretMissing(ctx context.Context, t secretTarget) error {
	elsewhere := []string{}
	if rows, err := a.objects(ctx, "/api/clusters/"+t.clusterID+"/secrets"); err == nil {
		for _, row := range rows {
			if text(row, "name") == t.name && text(row, "namespace") != t.namespace {
				elsewhere = append(elsewhere, text(row, "namespace"))
			}
		}
	}
	if len(elsewhere) > 0 {
		slices.Sort(elsewhere)
		return fmt.Errorf("secret %s is not in %s, but in namespace %s; pass --namespace %s", t.name, t.in(), ui.Clean(strings.Join(elsewhere, ", ")), ui.Clean(elsewhere[0]))
	}
	return fmt.Errorf("secret %s was not found in %s; list them with `edka secrets list`", t.name, t.in())
}

// secretInput reads the value of each key: from the file a --from-file names
// for it, else at a hidden prompt, or for one key from a stdin that is not a
// terminal. A value is never an argument, which shell history and the process
// list would keep.
func (a *App) secretInput(ctx context.Context, t secretTarget, keys []string, files map[string]string) (map[string]string, error) {
	values := map[string]string{}
	asked := []string{}
	for _, key := range keys {
		path, ok := files[key]
		if !ok {
			asked = append(asked, key)
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxSecretValue+1))
		_ = f.Close()
		switch {
		case err != nil:
			return nil, err
		case len(data) > maxSecretValue:
			return nil, fmt.Errorf("%s exceeds 1 MiB, the most a secret holds", path)
		case len(data) == 0:
			return nil, fmt.Errorf("%s is empty; nothing was changed", path)
		}
		values[key] = string(data)
	}
	switch {
	case len(asked) == 0:
		return values, nil
	case !ui.InputTerminal(a.In):
		if len(asked) != 1 {
			return nil, fmt.Errorf("stdin holds one value; set one key at a time, or give the others with --from-file")
		}
		value, err := a.stdinSecret(asked[0])
		if err != nil {
			return nil, err
		}
		values[asked[0]] = value
		return values, nil
	case a.noInput:
		return nil, fmt.Errorf("pipe the value on stdin: printf %%s \"$VALUE\" | edka secrets set %s %s", t.name, asked[0])
	}
	for _, key := range asked {
		value, err := a.promptSecret(ctx, fmt.Sprintf("Value for %s in secret %s: ", key, t.name), key)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
}

func (a *App) addSecrets(root *cobra.Command) {
	secrets := &cobra.Command{Use: "secrets", Aliases: []string{"secret"}, Short: "Create, change and delete Kubernetes secrets in a cluster", Long: "Manage the Kubernetes secrets Edka created in a cluster: generic secrets that\na deployment, an app or a cronjob reads. Edka lists the keys of a secret and\nnever its values, and it leaves alone the secrets it did not create.\n\nA secret lives in a namespace, which --namespace names; without it the\ncommands use the namespace default. They act on the linked cluster, or on the\none --cluster names.\n\nA deployment's own variables and secrets are set with `edka env`.", GroupID: "resources", Example: "  edka secrets list\n  edka secrets set database PASSWORD --namespace shop\n  edka secrets set tls-ca --from-file ca.crt=./ca.crt\n  edka secrets get database --namespace shop\n  edka secrets unset database OLD_PASSWORD --namespace shop\n  edka secrets delete database --namespace shop"}
	a.strictGroup(secrets)

	var all bool
	var namespace string
	list := &cobra.Command{Use: "list", Short: "List the secrets Edka manages, with their keys", Long: "List the secrets Edka created in the linked or selected cluster, or in every\ncluster when none is linked. Use --all to include every cluster, and\n--namespace for one namespace. The list shows the keys of each secret and never\na value.", Args: cobra.NoArgs, Example: "  edka secrets list\n  edka secrets list --namespace shop --json", RunE: func(cmd *cobra.Command, _ []string) error {
		suffix := "secrets"
		if namespace != "" {
			suffix += "?" + url.Values{"namespace": {namespace}}.Encode()
		}
		rows, err := a.clusterScopedRows(cmd.Context(), all, suffix)
		if err != nil {
			return err
		}
		return a.renderRows(rows, secretColumns)
	}}
	list.Flags().BoolVar(&all, "all", false, "List secrets in every cluster, not only the linked one")
	list.Flags().StringVar(&namespace, "namespace", "", "Only secrets in this namespace")

	// namespaced adds --namespace to a command that acts on one secret.
	var ns string
	namespaced := func(cmd *cobra.Command) *cobra.Command {
		cmd.Flags().StringVar(&ns, "namespace", "default", "Namespace of the secret")
		return cmd
	}
	get := namespaced(&cobra.Command{Use: "get <secret>", Short: "Show a secret's keys, type and age", Long: "Show a secret: its keys, its type and when it was created. Edka never returns\nthe values.", Args: cobra.ExactArgs(1), Example: "  edka secrets get database --namespace shop\n  edka secrets get database --json", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		t, err := a.secretTarget(ctx, args[0], ns)
		if err != nil {
			return err
		}
		m, err := a.readSecret(ctx, t)
		if err != nil {
			return err
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": m}), a.output, false)
		}
		return ui.Fields(a.Out, [][2]string{
			{"Name", text(m, "name")},
			{"Namespace", text(m, "namespace")},
			{"Type", text(m, "type")},
			{"Keys", strings.Join(secretKeys(m), ", ")},
			{"Created", when(m["createdAt"])},
			{"Cluster", t.cluster.Name},
		}, a.color)
	}})

	var fromFiles []string
	set := namespaced(&cobra.Command{Use: "set <secret> [KEY...]", Short: "Create a secret, or set keys of one", Long: "Set keys of a secret, and create the secret when the namespace has none of that\nname. The keys left out keep their values.\n\nEach value is read at a hidden prompt. When stdin is not a terminal, the\ncommand reads one value from stdin and drops the final newline. A value on the\ncommand line is refused, because shell history keeps it and other local users\ncan read it in the process list. --from-file KEY=path takes the content of a\nfile as the value of a key.\n\nA pod that reads the secret as environment variables gets a new value when it\nrestarts.", Args: cobra.MinimumNArgs(1), Example: "  edka secrets set database PASSWORD REPLICA_PASSWORD --namespace shop\n  printf %s \"$TOKEN\" | edka secrets set github TOKEN\n  edka secrets set tls-ca --from-file ca.crt=./ca.crt", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		keys, files := []string{}, map[string]string{}
		for _, key := range args[1:] {
			if name, _, assigned := strings.Cut(key, "="); assigned {
				return fmt.Errorf("values go at the prompt or on stdin, not in arguments that shell history keeps; for example: printf %%s \"$VALUE\" | edka secrets set %s %s", args[0], name)
			}
			keys = append(keys, key)
		}
		for _, flag := range fromFiles {
			key, path, ok := strings.Cut(flag, "=")
			if !ok || key == "" || path == "" {
				return fmt.Errorf("--from-file takes KEY=path: %q", flag)
			}
			files[key] = path
			keys = append(keys, key)
		}
		if len(keys) == 0 {
			return fmt.Errorf("name the keys to set, or give them with --from-file KEY=path")
		}
		for i, key := range keys {
			switch {
			case len(key) > 253 || !secretKey.MatchString(key):
				return fmt.Errorf("invalid key %q; use letters, digits, hyphens, underscores and dots", key)
			case slices.Contains(keys[:i], key):
				return fmt.Errorf("%s is given more than once", key)
			}
		}
		t, err := a.secretTarget(ctx, args[0], ns)
		if err != nil {
			return err
		}
		// A missing secret is created. Another failure of the read stops the command.
		_, err = a.request(ctx, "GET", t.path(), t.query(), nil)
		var apiError *api.Error
		exists := err == nil
		if err != nil && (!errors.As(err, &apiError) || apiError.Status != 404) {
			return err
		}
		values, err := a.secretInput(ctx, t, keys, files)
		if err != nil {
			return err
		}
		literals := []map[string]string{}
		for _, key := range keys {
			literals = append(literals, map[string]string{"key": key, "value": values[key]})
		}
		var response *api.Response
		if exists {
			response, err = a.request(ctx, "PUT", t.path(), t.query(), jsonBody(map[string]any{"literals": literals, "removeKeys": []string{}}))
		} else {
			response, err = a.request(ctx, "POST", "/api/clusters/"+t.clusterID+"/secrets", nil, jsonBody(map[string]any{"name": t.name, "namespace": t.namespace, "literals": literals}))
		}
		if err != nil {
			return err
		}
		if exists {
			a.message("✓ Set %s on secret %s in %s", strings.Join(keys, ", "), t.name, t.in())
		} else {
			a.message("✓ Created secret %s in %s with %s", t.name, t.in(), strings.Join(keys, ", "))
		}
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}})
	set.Flags().StringArrayVar(&fromFiles, "from-file", nil, "Take the content of a file as a value: KEY=path")

	unset := namespaced(&cobra.Command{Use: "unset <secret> KEY...", Short: "Remove keys from a secret", Long: "Remove keys from a secret and keep the others. The command fails without a\nchange when a key is not set.", Args: cobra.MinimumNArgs(2), Example: "  edka secrets unset database OLD_PASSWORD --namespace shop", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		t, err := a.secretTarget(ctx, args[0], ns)
		if err != nil {
			return err
		}
		m, err := a.readSecret(ctx, t)
		if err != nil {
			return err
		}
		keys := []string{}
		for _, key := range args[1:] {
			switch {
			case slices.Contains(keys, key):
				continue
			case !slices.Contains(secretKeys(m), key):
				return fmt.Errorf("%s is not set on secret %s in %s; it has %s", key, t.name, t.in(), first(strings.Join(secretKeys(m), ", "), "no keys"))
			}
			keys = append(keys, key)
		}
		response, err := a.request(ctx, "PUT", t.path(), t.query(), jsonBody(map[string]any{"literals": []string{}, "removeKeys": keys}))
		if err != nil {
			return err
		}
		a.message("✓ Removed %s from secret %s in %s", strings.Join(keys, ", "), t.name, t.in())
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}})

	remove := namespaced(&cobra.Command{Use: "delete <secret>", Short: "Delete a secret", Long: "Delete a secret from its namespace, after confirmation or with --yes. A pod\nthat reads it keeps running, and fails to start again without it.", Args: cobra.ExactArgs(1), Example: "  edka secrets delete database --namespace shop\n  edka secrets delete database --yes", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		t, err := a.secretTarget(ctx, args[0], ns)
		if err != nil {
			return err
		}
		if _, err := a.readSecret(ctx, t); err != nil {
			return err
		}
		if err := a.confirm(fmt.Sprintf("Delete secret %s from namespace %s of cluster %s", t.name, t.namespace, t.cluster.Name)); err != nil {
			return err
		}
		response, err := a.request(ctx, "DELETE", t.path(), t.query(), nil)
		if err != nil {
			return err
		}
		a.message("✓ Deleted secret %s from %s", t.name, t.in())
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}})

	// A secret is completed from the namespace --namespace names.
	names := func(ctx context.Context) ([]candidate, error) {
		cluster, err := a.clusterID(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := a.objectsQuery(ctx, "/api/clusters/"+cluster+"/secrets", url.Values{"namespace": {ns}})
		choices := []candidate{}
		for _, row := range rows {
			choices = append(choices, candidate{Name: text(row, "name"), Detail: strings.Join(secretKeys(row), ", ")})
		}
		return choices, err
	}
	a.completes(names, get, remove)
	// After the secret, unset completes the keys it has.
	keysOf := func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return a.offers(names)(cmd, args, toComplete)
		}
		return a.offers(func(ctx context.Context) ([]candidate, error) {
			t, err := a.secretTarget(ctx, args[0], ns)
			if err != nil {
				return nil, err
			}
			m, err := a.readSecret(ctx, t)
			choices := []candidate{}
			for _, key := range secretKeys(m) {
				if !slices.Contains(args[1:], key) {
					choices = append(choices, candidate{Name: key})
				}
			}
			return choices, err
		})(cmd, args, toComplete)
	}
	unset.ValidArgsFunction = keysOf
	set.ValidArgsFunction = firstArgument(a.offers(names))
	namespaces := a.offers(func(ctx context.Context) ([]candidate, error) {
		cluster, err := a.clusterID(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := a.objects(ctx, "/api/clusters/"+cluster+"/namespaces")
		choices := []candidate{}
		for _, row := range rows {
			choices = append(choices, candidate{Name: text(row, "name")})
		}
		return choices, err
	})
	for _, cmd := range []*cobra.Command{list, get, set, unset, remove} {
		a.completesFlag(cmd, "namespace", namespaces)
	}
	secrets.AddCommand(list, get, set, unset, remove)
	root.AddCommand(secrets)
}
