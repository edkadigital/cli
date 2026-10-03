package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// appField is one setting of a catalog app, from its inputs_schema.
type appField struct {
	Name   string
	Tab    string
	Config map[string]any
}

func (f appField) kind() string  { return text(f.Config, "type") }
func (f appField) label() string { return first(text(f.Config, "label"), f.Name) }
func (f appField) secret() bool  { return f.kind() == "password" || f.Config["secret"] == true }

// appFields lists a catalog app's settings in the order the console shows its tabs.
func appFields(app map[string]any) []appField {
	schema, _ := app["inputs_schema"].(map[string]any)
	tabs := []string{}
	if order, ok := app["configuration_tabs"].([]any); ok {
		for _, tab := range order {
			if name, ok := tab.(string); ok && schema[name] != nil && !slices.Contains(tabs, name) {
				tabs = append(tabs, name)
			}
		}
	}
	rest := []string{}
	for tab := range schema {
		if !slices.Contains(tabs, tab) {
			rest = append(rest, tab)
		}
	}
	sort.Strings(rest)
	fields := []appField{}
	for _, tab := range append(tabs, rest...) {
		list, _ := schema[tab].([]any)
		for _, item := range list {
			m, _ := item.(map[string]any)
			config, _ := m["config"].(map[string]any)
			if name := text(m, "name"); name != "" && config != nil {
				fields = append(fields, appField{Name: name, Tab: tab, Config: config})
			}
		}
	}
	return fields
}

func findField(fields []appField, name string) (appField, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return appField{}, false
}

// catalogCommand is the command that lists an app's settings.
func catalogCommand(app *candidate) string {
	return "edka apps catalog " + shellJoin([]string{first(text(app.Record, "slug"), app.ID)})
}

func noSetting(app *candidate, key string) error {
	return fmt.Errorf("%s has no setting %q; list them with `%s`", ui.Clean(app.Name), key, catalogCommand(app))
}

// catalogRow describes a setting for `apps catalog <app>`.
func catalogRow(f appField) map[string]any {
	row := map[string]any{"name": f.Name, "tab": f.Tab, "type": f.kind(), "label": f.label(), "required": f.Config["required"] == true, "secret": f.secret()}
	for _, key := range []string{"description", "default", "show_if", "data_source", "depends_on"} {
		if v, ok := f.Config[key]; ok {
			row[key] = v
		}
	}
	if f.kind() == "password" && f.Config["generate"] == true {
		row["generated"] = true
	}
	if options := staticOptions(f); len(options) > 0 {
		values := []string{}
		for _, o := range options {
			values = append(values, o.Value)
		}
		row["options"] = values
	}
	return row
}

var catalogFieldColumns = []ui.Column{
	ui.Field("SETTING", "name"),
	ui.Field("TYPE", "type"),
	{Header: "DEFAULT", Value: func(m map[string]any) string {
		switch {
		case m["generated"] == true:
			return "generated"
		case m["secret"] == true:
			return ""
		case m["default"] != nil:
			return ui.Text(m["default"])
		}
		return ""
	}},
	{Header: "REQUIRED", Value: func(m map[string]any) string {
		if m["required"] != true {
			return ""
		}
		if condition := text(m, "show_if"); condition != "" {
			return "if " + condition
		}
		return "yes"
	}},
	{Header: "DESCRIPTION", Value: func(m map[string]any) string { return first(text(m, "description"), text(m, "label")) }},
}

// resolveCatalogApp selects an app from a cluster's catalog by ID, slug or name.
func (a *App) resolveCatalogApp(ctx context.Context, cluster, target string) (*candidate, error) {
	rows, err := a.objects(ctx, "/api/clusters/"+cluster+"/apps/catalog")
	if err != nil {
		return nil, err
	}
	candidates := []candidate{}
	for _, row := range rows {
		if id := text(row, "id"); id != "" {
			candidates = append(candidates, candidate{ID: id, Name: first(text(row, "name"), id), Names: []string{text(row, "slug")}, Detail: text(row, "category"), Record: row})
		}
	}
	c, err := a.pick(ctx, "catalog app", candidates, target)
	if err != nil && errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("%w; list them with `edka apps catalog`", err)
	}
	return c, err
}

// fieldOption is one choice for a select field.
type fieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func staticOptions(f appField) []fieldOption {
	list, _ := f.Config["options"].([]any)
	options := []fieldOption{}
	for _, item := range list {
		switch o := item.(type) {
		case string:
			options = append(options, fieldOption{Value: o, Label: o})
		case map[string]any:
			value := ui.Text(o["value"])
			options = append(options, fieldOption{Value: value, Label: first(text(o, "label"), value)})
		}
	}
	return options
}

// appDataSources are the option lists Edka serves for app settings by name.
var appDataSources = []string{
	"cluster-postgresql-instances", "cluster-postgresql-databases", "cluster-postgresql-users",
	"cluster-valkey-instances", "cluster-clickhouse-instances", "cluster-hermes-agent-instances",
	"cluster-environment-agents", "cluster-gateway-wildcard-domains", "organization-object-storage-integrations",
}

// optionList is what a setting can be set to.
type optionList struct {
	Options []fieldOption
	// Listed is false when a setting takes typed text, or the CLI cannot list its choices.
	Listed bool
	// Open lists suggestions, such as existing namespaces: another value is allowed too.
	Open bool
	// Needs names the setting the list depends on, while that setting has no value.
	Needs string
}

func isBlank(v any) bool {
	s, isText := v.(string)
	return v == nil || isText && strings.TrimSpace(s) == ""
}

// namedOptions lists rows by name, marking the default one.
func namedOptions(rows []map[string]any, keep func(map[string]any) bool) []fieldOption {
	options := []fieldOption{}
	for _, row := range rows {
		name := text(row, "name")
		if name == "" || keep != nil && !keep(row) {
			continue
		}
		label := name
		if row["isDefault"] == true {
			label += " (default)"
		}
		options = append(options, fieldOption{Value: name, Label: label})
	}
	return options
}

// fieldOptions lists what a setting can be set to, from the same routes the
// console's form reads. Pass withDefaults of the configuration, so a list that
// depends on a setting with a default uses it.
func (a *App) fieldOptions(ctx context.Context, cluster string, f appField, configuration map[string]any) (optionList, error) {
	if f.kind() == "select" {
		return optionList{Options: staticOptions(f), Listed: true}, nil
	}
	if f.kind() != "dynamic-select" {
		return optionList{}, nil
	}
	dependency := text(f.Config, "depends_on")
	if dependency != "" && isBlank(configuration[dependency]) {
		return optionList{Listed: true, Needs: dependency}, nil
	}
	source := text(f.Config, "data_source")
	path, query := "", url.Values{}
	switch {
	case source == "cluster-ingress-classes" || source == "cluster-gateway-classes":
		path = "/ingress-classes"
	case source == "cluster-storage-classes":
		path = "/storage-classes"
	case source == "cluster-node-pools":
		path = "/nodepools"
	case source == "cluster-namespaces":
		path = "/namespaces"
	case slices.Contains(appDataSources, source):
		path = "/apps/data-sources/" + url.PathEscape(source)
		if dependency != "" {
			query.Set(dependency, fmt.Sprint(configuration[dependency]))
		}
	default:
		return optionList{}, nil
	}
	rows, err := a.objectsQuery(ctx, "/api/clusters/"+cluster+path, query)
	if err != nil {
		return optionList{}, err
	}
	list := optionList{Listed: true}
	switch source {
	case "cluster-ingress-classes":
		list.Options = namedOptions(rows, nil)
	case "cluster-gateway-classes":
		list.Options = namedOptions(rows, func(row map[string]any) bool { return text(row, "controller_type") == "gateway-api" })
	case "cluster-storage-classes":
		list.Options = namedOptions(rows, nil)
	case "cluster-node-pools":
		list.Options = append([]fieldOption{{Value: "", Label: "Default placement (no specific node pool)"}}, namedOptions(rows, nil)...)
	case "cluster-namespaces":
		list.Options, list.Open = namedOptions(rows, nil), true
	default:
		for _, row := range rows {
			if value := ui.Text(row["value"]); row["value"] != nil && value != "" {
				list.Options = append(list.Options, fieldOption{Value: value, Label: first(text(row, "label"), value)})
			}
		}
	}
	return list, nil
}

// optionValue finds the option an input names: its value, its label, or the
// label's name before a parenthesis, such as "orders" for "orders (orders.postgres)".
func optionValue(options []fieldOption, input string) (string, bool) {
	for _, o := range options {
		if o.Value == input {
			return o.Value, true
		}
	}
	matches := []string{}
	for _, o := range options {
		name, _, _ := strings.Cut(o.Label, " (")
		if o.Label == input || strings.TrimSpace(name) == input {
			matches = append(matches, o.Value)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

func optionLabels(options []fieldOption) string {
	labels := []string{}
	for _, o := range options[:min(len(options), 8)] {
		labels = append(labels, ui.Clean(o.Label))
	}
	if len(options) > 8 {
		labels = append(labels, "…")
	}
	return strings.Join(labels, ", ")
}

// typedValue converts a --set value to the setting's type.
func typedValue(f appField, raw string) (any, error) {
	switch f.kind() {
	case "number":
		// ParseFloat accepts NaN and Inf, which JSON cannot carry.
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%s takes a number, not %q", f.Name, raw)
		}
		return n, nil
	case "boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%s takes true or false, not %q", f.Name, raw)
		}
		return b, nil
	}
	return raw, nil
}

// installConfiguration builds a configuration from --data and --set. Settings
// with option lists accept an option's name, which becomes its value; labels
// keeps that option's label to show.
func (a *App) installConfiguration(ctx context.Context, cluster string, app *candidate, fields []appField, data string, sets []string) (configuration map[string]any, labels map[string]string, err error) {
	configuration, labels = map[string]any{}, map[string]string{}
	if data != "" {
		raw, err := a.body(data, nil)
		if err != nil {
			return nil, nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &configuration); err != nil || configuration == nil {
				return nil, nil, fmt.Errorf("--data must be a JSON object of settings")
			}
		}
	}
	given := map[string]string{}
	for _, set := range sets {
		// Split at the first "=", so a value may contain ":=".
		key, raw, ok := strings.Cut(set, "=")
		if !ok {
			return nil, nil, fmt.Errorf("set a setting as key=value, or key:=JSON: %q", set)
		}
		key, typed := strings.CutSuffix(key, ":")
		f, ok := findField(fields, key)
		if !ok {
			return nil, nil, noSetting(app, key)
		}
		if f.secret() {
			return nil, nil, fmt.Errorf("%s is a secret; enter it at the prompt, or put it in a file for --data @file, so shell history does not keep it", key)
		}
		if typed {
			var v any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				return nil, nil, fmt.Errorf("invalid JSON for %s: %w", key, err)
			}
			configuration[key] = v
			// A later JSON value replaces an earlier option name.
			delete(given, key)
			continue
		}
		v, err := typedValue(f, raw)
		if err != nil {
			return nil, nil, err
		}
		configuration[key] = v
		given[key] = raw
	}
	// Resolve names in schema order, so a list that depends on another setting
	// sees that setting's value.
	for _, f := range fields {
		raw, ok := given[f.Name]
		if !ok || f.kind() != "dynamic-select" && f.kind() != "select" {
			continue
		}
		list, err := a.fieldOptions(ctx, cluster, f, withDefaults(fields, configuration))
		if err != nil {
			return nil, nil, fmt.Errorf("cannot list the options for %s: %w", f.Name, err)
		}
		if !list.Listed || list.Open || list.Needs != "" {
			continue
		}
		value, ok := optionValue(list.Options, raw)
		if !ok {
			if len(list.Options) == 0 {
				return nil, nil, fmt.Errorf("%s has no options in this cluster", f.Name)
			}
			return nil, nil, fmt.Errorf("%s has no option %q; choose one of: %s", f.Name, raw, optionLabels(list.Options))
		}
		configuration[f.Name] = value
		for _, o := range list.Options {
			if o.Value == value {
				labels[f.Name] = o.Label
			}
		}
	}
	return configuration, labels, nil
}

// SettingError is a setting an install still needs, with what it can be set to.
type SettingError struct {
	Field   string        `json:"field"`
	Message string        `json:"message"`
	Label   string        `json:"label,omitempty"`
	Type    string        `json:"type,omitempty"`
	Secret  bool          `json:"secret,omitempty"`
	Options []fieldOption `json:"options,omitempty"`
	// OptionsOpen means the options are suggestions and another value is allowed.
	OptionsOpen bool `json:"options_open,omitempty"`
	// DependsOn names the setting to choose first, before the options can be listed.
	DependsOn string `json:"depends_on,omitempty"`
}

// FieldsError explains the settings an install still needs. It wraps the API
// error, so JSON errors keep its status, and replaces its fields with Fields.
type FieldsError struct {
	message string
	Fields  []SettingError
	api     *api.Error
}

func (e *FieldsError) Error() string { return e.message }
func (e *FieldsError) Unwrap() error { return e.api }

func (a *App) missingSettings(ctx context.Context, cluster string, app *candidate, fields []appField, configuration map[string]any, apiError *api.Error) error {
	settings := []SettingError{}
	lines, unknown := []string{}, []string{}
	secret, missing := false, true
	effective := withDefaults(fields, configuration)
	for _, fe := range apiError.Fields {
		setting := SettingError{Field: fe.Field, Message: fe.Message}
		line := fmt.Sprintf("  %s: %s", ui.Clean(fe.Field), ui.Clean(fe.Message))
		missing = missing && fe.Message == "Required"
		// A JSON setting's error names a path inside it, such as labels.team.
		name, _, _ := strings.Cut(fe.Field, ".")
		if f, ok := findField(fields, name); !ok {
			unknown = append(unknown, fe.Field)
		} else {
			setting.Label, setting.Type, setting.Secret = f.label(), f.kind(), f.secret()
			line += ". " + ui.Clean(first(text(f.Config, "description"), f.label()))
			secret = secret || f.secret()
			if list, err := a.fieldOptions(ctx, cluster, f, effective); err == nil && list.Listed {
				setting.Options, setting.OptionsOpen, setting.DependsOn = list.Options, list.Open, list.Needs
				switch {
				case list.Needs != "":
					line += ". Choose " + list.Needs + " first"
				case len(list.Options) == 0:
					line += ". No options in this cluster yet"
				default:
					line += ". Options: " + optionLabels(list.Options)
				}
			}
		}
		lines = append(lines, line)
		settings = append(settings, setting)
	}
	title := "%s needs these settings:"
	if !missing {
		title = "%s cannot install with these settings:"
	}
	lines = append([]string{fmt.Sprintf(title, ui.Clean(app.Name))}, lines...)
	hint := "Set them with --set key=value"
	if len(unknown) > 0 {
		lines = append(lines, fmt.Sprintf("Remove %s from --data; list the app's settings with `%s`.", ui.Clean(strings.Join(unknown, ", ")), catalogCommand(app)))
		hint = "Set the others with --set key=value"
	}
	if len(unknown) < len(apiError.Fields) {
		if secret {
			hint += ", and secrets in a file for --data @file"
		}
		lines = append(lines, hint+".")
	}
	return &FieldsError{message: strings.Join(lines, "\n"), Fields: settings, api: apiError}
}

// askSettings asks for each setting the API reports, and reports false when
// one of them is not a setting of the app.
func (a *App) askSettings(ctx context.Context, cluster string, fields []appField, configuration map[string]any, reported []api.FieldError) (bool, error) {
	messages := map[string]string{}
	for _, fe := range reported {
		if _, ok := findField(fields, fe.Field); !ok {
			return false, nil
		}
		if _, ok := messages[fe.Field]; !ok {
			messages[fe.Field] = fe.Message
		}
	}
	// Ask in the app's order, so a list that depends on another setting comes after it.
	for _, f := range fields {
		message, ok := messages[f.Name]
		if !ok {
			continue
		}
		if message != "Required" {
			a.message("%s: %s", ui.Clean(f.label()), ui.Clean(message))
		}
		value, _, err := a.askSetting(ctx, cluster, f, withDefaults(fields, configuration))
		if err != nil {
			return false, err
		}
		configuration[f.Name] = value
	}
	return true, nil
}

// askSetting asks for one setting and returns its value, and how to show it.
// effective is the configuration with defaults, for lists that depend on
// another setting.
func (a *App) askSetting(ctx context.Context, cluster string, f appField, effective map[string]any) (any, string, error) {
	label := ui.Clean(f.label())
	if f.secret() {
		value, err := ui.ReadSecret(ctx, label+": ", a.In, a.Err)
		if err == nil && strings.ContainsRune(value, '\x1b') {
			err = fmt.Errorf("the value for %s contains a terminal escape sequence; enter it again", f.Name)
		}
		return value, "(hidden)", err
	}
	list, err := a.fieldOptions(ctx, cluster, f, effective)
	if err != nil {
		return nil, "", err
	}
	if list.Needs != "" {
		return nil, "", fmt.Errorf("%s depends on %s; set it first", f.Name, list.Needs)
	}
	if list.Listed && !list.Open {
		if len(list.Options) == 0 {
			return nil, "", fmt.Errorf("%s has no options in this cluster; create one first", label)
		}
		if len(list.Options) == 1 {
			a.message("%s: %s, the only option", label, ui.Clean(list.Options[0].Label))
			return list.Options[0].Value, list.Options[0].Label, nil
		}
		choices := make([]ui.Choice, len(list.Options))
		for i, o := range list.Options {
			choices[i] = ui.Choice{ID: o.Value, Name: o.Label}
		}
		value, err := ui.Select(ctx, "Choose "+label, choices, a.In, a.Err)
		for _, o := range list.Options {
			if o.Value == value {
				return value, o.Label, err
			}
		}
		return value, value, err
	}
	prompt := label
	if description := text(f.Config, "description"); description != "" {
		prompt += " (" + ui.Clean(description) + ")"
	}
	for {
		answer, err := a.askLine(prompt + ": ")
		if err != nil {
			return nil, "", fmt.Errorf("no value entered for %s", f.Name)
		}
		value, err := typedValue(f, answer)
		if err == nil {
			return value, answer, nil
		}
		a.message("%s", err)
	}
}

// optionsCommand lists what an app setting can be set to, for scripts and agents
// that pass settings with --set.
func (a *App) optionsCommand() *cobra.Command {
	var sets []string
	options := &cobra.Command{Use: "options <app> <setting>", Short: "List the choices for an app setting", Long: "List what an app setting can be set to in the linked or selected cluster, such\nas its PostgreSQL instances or traffic classes. A list that depends on another\nsetting, such as the databases of an instance, needs that setting with --set.\n\n`edka apps install <app> --set key=value` takes the VALUE, or the LABEL's name\nbefore any parenthesis.", Args: cobra.ExactArgs(2), Example: "  edka apps options umami postgres_instance\n  edka apps options umami postgres_database --set postgres_instance=postgres --json", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		cluster, err := a.clusterID(ctx)
		if err != nil {
			return err
		}
		app, err := a.resolveCatalogApp(ctx, cluster, args[0])
		if err != nil {
			return err
		}
		fields := appFields(app.Record)
		f, ok := findField(fields, args[1])
		if !ok {
			return noSetting(app, args[1])
		}
		configuration, _, err := a.installConfiguration(ctx, cluster, app, fields, "", sets)
		if err != nil {
			return err
		}
		list, err := a.fieldOptions(ctx, cluster, f, withDefaults(fields, configuration))
		switch {
		case err != nil:
			return err
		case !list.Listed:
			return fmt.Errorf("%s takes a %s value, not a choice from a list", f.Name, first(f.kind(), "text"))
		case list.Needs != "":
			return fmt.Errorf("%s depends on %s; pass --set %s=<name>", f.Name, list.Needs, list.Needs)
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": list.Options, "open": list.Open}), a.output, false)
		}
		if list.Open {
			a.message("Existing names; a new name works too.")
		}
		rows := make([]map[string]any, len(list.Options))
		for i, o := range list.Options {
			rows[i] = map[string]any{"value": o.Value, "label": o.Label}
		}
		return ui.Table(a.Out, []ui.Column{ui.Field("VALUE", "value"), ui.Field("LABEL", "label")}, rows, a.color)
	}}
	options.Flags().StringArrayVar(&sets, "set", nil, "A setting the list depends on, as key=value")
	return options
}

func (a *App) installCommand() *cobra.Command {
	var name, data string
	var sets []string
	var wait bool
	var waitTimeout time.Duration
	install := &cobra.Command{Use: "install <app>", Short: "Install an app from the catalog", Long: "Install an app from the catalog into the linked or selected cluster. Settings you\nleave out take the catalog's defaults, and generated passwords are created for\nyou. List an app's settings with `edka apps catalog <app>`.\n\nIn a terminal, install asks for what the app needs and --set left out: the\nPostgreSQL instance, database and user it uses, whether to expose it, its traffic\nclass, and a hostname from the cluster's domains. It then shows the settings and\nasks to go ahead; --yes skips that question.\n\nScripts and agents get no prompts. Settings with a list of options take the\noption's name; list them with `edka apps options <app> <setting>`. A missing\nsetting fails the install with an error that names it. Secrets are never taken\nfrom --set: enter them at the prompt, or put them in a file for --data @file.\n\nWith --wait, progress goes to stderr until the app is installed.", Args: cobra.ExactArgs(1), Example: "  edka apps install excalidraw --wait\n  edka apps install umami --set postgres_instance=postgres --set hostname=stats.example.com\n  edka apps install n8n --name n8n-staging --data @n8n.json", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		ctx := cmd.Context()
		cluster, err := a.resolveCluster(ctx, "")
		if err != nil {
			return err
		}
		clusterID, err := safeID(cluster.ID)
		if err != nil {
			return err
		}
		app, err := a.resolveCatalogApp(ctx, clusterID, args[0])
		if err != nil {
			return err
		}
		catalogID, err := safeID(app.ID)
		if err != nil {
			return err
		}
		fields := appFields(app.Record)
		configuration, labels, err := a.installConfiguration(ctx, clusterID, app, fields, data, sets)
		if err != nil {
			return err
		}
		// Prompts need a terminal on stderr too, where they appear.
		prompts := !a.noInput && ui.IsTerminal(a.Err)
		if prompts {
			if err := a.guideInstall(ctx, cluster, clusterID, app, fields, configuration, labels, &name); err != nil {
				return err
			}
		}
		var response *api.Response
		for {
			body := map[string]any{"configuration": configuration}
			if name != "" {
				body["displayName"] = name
			}
			sent, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				return marshalErr
			}
			response, err = a.request(ctx, "POST", "/api/clusters/"+clusterID+"/apps/"+catalogID+"/instances", nil, sent)
			var apiError *api.Error
			if !errors.As(err, &apiError) || len(apiError.Fields) == 0 {
				break
			}
			if !prompts {
				return a.missingSettings(ctx, clusterID, app, fields, configuration, apiError)
			}
			asked, askErr := a.askSettings(ctx, clusterID, fields, configuration, apiError.Fields)
			if askErr != nil {
				return askErr
			}
			// An answer taken without asking, such as a list's only option, can
			// repeat the rejected request.
			if again, _ := json.Marshal(body); !asked || bytes.Equal(again, sent) {
				return a.missingSettings(ctx, clusterID, app, fields, configuration, apiError)
			}
		}
		if err != nil {
			return err
		}
		record, err := identityData(response.Body)
		if err != nil {
			return err
		}
		appID, err := safeID(text(record, "app_id"))
		if err != nil {
			return fmt.Errorf("installation started but no app ID returned; inspect `edka apps list`")
		}
		instance := first(name, text(record, "instance_slug"), app.Name)
		if !wait {
			a.message("✓ Installing %s in cluster %s as %s.\n  Next: edka apps get %s", ui.Clean(app.Name), ui.Clean(cluster.Name), ui.Clean(instance), shellJoin([]string{instance}))
			if a.output != "table" {
				return a.render(response)
			}
			return nil
		}
		a.message("Installing %s in cluster %s as %s…", ui.Clean(app.Name), ui.Clean(cluster.Name), ui.Clean(instance))
		installed, m, err := a.waitApp(ctx, "/api/clusters/"+clusterID+"/apps/"+appID, instance, waitTimeout)
		if err != nil {
			return err
		}
		a.message("✓ %s is installed", ui.Clean(instance))
		if a.output != "table" {
			return a.render(installed)
		}
		return a.showApp(m, cluster.Name, text(record, "app_id"))
	}}
	install.Flags().StringVar(&name, "name", "", "Name for this instance; defaults to the app's name")
	install.Flags().StringArrayVar(&sets, "set", nil, "Setting as key=value, or key:=JSON")
	install.Flags().StringVarP(&data, "data", "d", "", "Settings as a JSON object, @file.json, or @-")
	install.Flags().BoolVar(&wait, "wait", false, "Wait until the app is installed")
	install.Flags().DurationVar(&waitTimeout, "wait-timeout", 15*time.Minute, "Maximum installation wait")
	return install
}
