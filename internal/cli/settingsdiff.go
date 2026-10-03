package cli

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/edkadigital/cli/internal/ui"
)

// settingsRoute is the route `edka up` changes a deployment's settings with.
const settingsRoute = "/api/deployments/:id/settings"

// change is one field a settings request would change. A list that Edka
// replaces whole has Added, Removed and Changed in place of From and To.
type change struct {
	Field   string   `json:"field"`
	From    any      `json:"from,omitempty"`
	To      any      `json:"to,omitempty"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Changed []string `json:"changed,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// settingsDiff compares a settings request with the deployment it would change.
type settingsDiff struct {
	Deployment string   `json:"deployment"`
	ID         string   `json:"id"`
	Generation int      `json:"generation"`
	Changes    []change `json:"changes"`
	// Unchanged are fields the request sends with the value they have.
	Unchanged []string `json:"unchanged"`
	// Ignored are fields the request sends to no effect, each with why.
	Ignored []string `json:"ignored"`
	// Refused are fields Edka refuses the request for, each with why.
	Refused []string `json:"refused"`
}

// settingsFields are the body fields the settings route reads, at the top
// level and in config, from the schema in the catalog. Both are nil when the
// catalog has no schema, and then no field counts as ignored.
func (a *App) settingsFields() (top, config map[string]bool) {
	op := a.catalog.Find("PATCH", settingsRoute)
	if op == nil || len(op.Body) == 0 {
		return nil, nil
	}
	var schema struct {
		Properties map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	if json.Unmarshal(op.Body, &schema) != nil || len(schema.Properties) == 0 {
		return nil, nil
	}
	top, config = map[string]bool{}, map[string]bool{}
	for name, property := range schema.Properties {
		top[name] = true
		if name == "config" {
			for field := range property.Properties {
				config[field] = true
			}
		}
	}
	return top, config
}

// diffSettings compares a settings request with a deployment as Edka returns
// it, whose config holds the columns it mirrors.
//
// Edka merges the config of a request into the stored one key by key, and
// replaces a list the request sends. So each key of the request is compared
// with the stored value, and a list entry by entry. The hostnames are compared
// as Edka stores them after the merge, which is none for a deployment the
// merged config does not expose.
func diffSettings(record, body map[string]any, top, configFields map[string]bool) *settingsDiff {
	current, _ := record["config"].(map[string]any)
	requested, _ := body["config"].(map[string]any)
	// The config of the request decides the exposure when it names it.
	exposure := requested["expose_via_ingress"]
	for _, stored := range []any{current["expose_via_ingress"], record["expose_via_ingress"]} {
		if exposure == nil {
			exposure = stored
		}
	}
	exposed := exposure == true
	namesHostnames := requested["hostname"] != nil || requested["hostnames"] != nil || body["hostname"] != nil || body["hostnames"] != nil
	// Edka refuses a config that exposes the deployment and names no hostname,
	// whatever hostnames are stored or at the top level of the body.
	exposesBare := requested["expose_via_ingress"] == true && len(hostnamesOf(requested, nil)) == 0
	d := &settingsDiff{Deployment: first(text(record, "name"), text(record, "id")), ID: text(record, "id"), Generation: number(record["spec_generation"]), Changes: []change{}, Unchanged: []string{}, Ignored: []string{}, Refused: []string{}}
	compare := func(field string, from, to any) {
		if same(from, to) {
			d.Unchanged = append(d.Unchanged, field)
			return
		}
		d.Changes = append(d.Changes, change{Field: field, From: from, To: to})
	}
	list := func(c change) {
		if len(c.Added)+len(c.Removed)+len(c.Changed) == 0 {
			d.Unchanged = append(d.Unchanged, c.Field)
			return
		}
		d.Changes = append(d.Changes, c)
	}
	// Edka refuses a request that names another name or namespace.
	identity := func(field string, from, to any) {
		if same(from, to) {
			d.Unchanged = append(d.Unchanged, field)
			return
		}
		d.Refused = append(d.Refused, fmt.Sprintf("%s: a deployment keeps its %s, %s", field, field[strings.LastIndex(field, ".")+1:], ui.Text(from)))
	}
	// Edka keeps hostnames only of a deployment it exposes. A request that
	// leaves it unexposed removes the stored ones and drops the ones it sends.
	hostnames := func(field string, sent []string) {
		stored := hostnamesOf(current, record)
		if exposed && len(sent) == 0 {
			// Edka refuses to expose a deployment under no hostname.
			if !exposesBare {
				d.Refused = append(d.Refused, field+": "+hostnameNeeded+"; send config.expose_via_ingress false to stop exposing it")
			}
			return
		}
		if exposed {
			list(stringList(field, stored, sent))
			return
		}
		if len(sent) > 0 {
			ignored := field + ": " + unexposedNote
			if requested["expose_via_ingress"] == nil {
				ignored += "; send config.expose_via_ingress true"
			}
			d.Ignored = append(d.Ignored, ignored)
		}
		switch {
		case len(stored) > 0:
			d.Changes = append(d.Changes, change{Field: field, Removed: stored, Note: unexposedNote})
		case len(sent) == 0:
			d.Unchanged = append(d.Unchanged, field)
		}
	}

	for _, key := range slices.Sorted(maps.Keys(requested)) {
		value := requested[key]
		field := "config." + key
		switch key {
		case "name":
			identity(field, first(text(current, "name"), text(record, "name")), value)
		case "namespace":
			identity(field, record["namespace"], value)
		case "env_variables":
			list(namedList(field, current[key], value, "name", func(m map[string]any) string { return text(m, "value") }))
		case "secrets":
			// A secret's value is never stored in the config, and never shown.
			c := namedList(field, current[key], value, "name", nil)
			// Edka refuses a secret the deployment lacks when the request
			// carries no value for it.
			stored, provided, refused := secretNames(current[key]), secretValueNames(body["secret_values"]), false
			for _, name := range secretNames(value) {
				if slices.Contains(stored, name) || slices.Contains(provided, name) {
					continue
				}
				d.Refused = append(d.Refused, fmt.Sprintf("%s: %s is a new secret and has no value; send secret_values.%s", field, name, name))
				c.Added = slices.DeleteFunc(c.Added, func(added string) bool { return strings.TrimSpace(added) == name })
				refused = true
			}
			if !refused || len(c.Added)+len(c.Removed)+len(c.Changed) > 0 {
				list(c)
			}
		case "volumes":
			list(namedList(field, current[key], value, "mountPath", func(m map[string]any) string {
				rest := maps.Clone(m)
				delete(rest, "mountPath")
				return compact(rest)
			}))
		case "hostname", "hostnames":
			// One list, which the top level of the body decides when it names it.
			if key == "hostname" && requested["hostnames"] != nil || body["hostname"] != nil || body["hostnames"] != nil {
				continue
			}
			hostnames("config.hostnames", hostnamesOf(requested, nil))
		case "expose_via_ingress":
			if exposesBare {
				d.Refused = append(d.Refused, field+": "+hostnameNeeded+"; send config.hostnames with it")
				continue
			}
			compare(field, current[key], value)
			// A request that ends the exposure removes the hostnames it does not name too.
			if !exposed && !namesHostnames && len(hostnamesOf(current, record)) > 0 {
				hostnames("config.hostnames", nil)
			}
		default:
			compare(field, current[key], value)
		}
	}
	// A request that moves the image and names no digest leaves the image unpinned.
	if digest := text(current, "image_digest"); digest != "" && requested != nil && requested["image_digest"] == nil &&
		(requested["image_tag"] != nil && !same(current["image_tag"], requested["image_tag"]) || requested["image_repository"] != nil && !same(current["image_repository"], requested["image_repository"])) {
		d.Changes = append(d.Changes, change{Field: "config.image_digest", From: digest, Note: "the image is no longer pinned to a digest"})
	}

	for _, key := range slices.Sorted(maps.Keys(body)) {
		value := body[key]
		// The route reads the exposure from the config, and still refuses one
		// at the top level that comes with no hostname there.
		if key == "expose_via_ingress" && value == true && len(hostnamesOf(body, nil)) == 0 {
			d.Refused = append(d.Refused, key+": "+hostnameNeeded+"; send config.expose_via_ingress with config.hostnames")
			continue
		}
		switch key {
		case "config", "expected_generation":
		case "name":
			identity(key, first(text(current, "name"), text(record, "name")), value)
		case "namespace":
			identity(key, record["namespace"], value)
		case "secret_values":
			// Edka writes a value only for a secret the merged config names.
			secrets := current["secrets"]
			if requested["secrets"] != nil {
				secrets = requested["secrets"]
			}
			declared := secretNames(secrets)
			set := []string{}
			for _, name := range secretValueNames(value) {
				if slices.Contains(declared, name) {
					set = append(set, name)
					continue
				}
				d.Ignored = append(d.Ignored, fmt.Sprintf("%s.%s: the deployment has no secret of that name; add it to config.secrets", key, name))
			}
			if len(set) > 0 {
				d.Changes = append(d.Changes, change{Field: key, Changed: set, Note: "values not shown"})
			}
		case "hostname", "hostnames":
			if key == "hostname" && body["hostnames"] != nil {
				continue
			}
			hostnames("hostnames", hostnamesOf(body, nil))
		case "https_only":
			// The top level sets it when the config of the request does not.
			if requested["https_only"] == nil {
				compare(key, current[key], value)
			}
		default:
			if top != nil && !top[key] {
				ignored := key + ": the settings route does not read it"
				if configFields[key] {
					ignored += "; send config." + key
				}
				d.Ignored = append(d.Ignored, ignored)
				continue
			}
			compare(key, record[key], value)
		}
	}
	return d
}

// same reports whether two JSON values are equal. A column that Edka returns
// as text equals the number a request sends for it.
func same(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch a.(type) {
	case map[string]any, []any:
		return false
	}
	switch b.(type) {
	case map[string]any, []any:
		return false
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// compact is a value as one line of JSON.
func compact(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(data)
}

// records are the objects of a JSON list, or nil with false when v is no list
// of objects.
func records(v any) ([]map[string]any, bool) {
	if v == nil {
		return nil, true
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	result := make([]map[string]any, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		result = append(result, m)
	}
	return result, true
}

// namedList compares two lists of objects that key names. A request replaces
// the stored list, so an entry it leaves out is removed. With no value
// function, only names are compared and nothing of an entry is shown.
func namedList(field string, from, to any, key string, value func(map[string]any) string) change {
	before, known := records(from)
	after, valid := records(to)
	if !known || !valid {
		if same(from, to) {
			return change{Field: field}
		}
		return change{Field: field, Changed: []string{"the list"}, Note: "not a list of objects, so it is not compared entry by entry"}
	}
	c := change{Field: field}
	old := map[string]map[string]any{}
	for _, item := range before {
		old[text(item, key)] = item
	}
	kept := map[string]bool{}
	for _, item := range after {
		name := text(item, key)
		kept[name] = true
		previous, exists := old[name]
		switch {
		case !exists && value == nil:
			c.Added = append(c.Added, name)
		case !exists:
			c.Added = append(c.Added, name+"="+value(item))
		case value != nil && value(previous) != value(item):
			c.Changed = append(c.Changed, fmt.Sprintf("%s: %s → %s", name, value(previous), value(item)))
		}
	}
	for _, item := range before {
		if name := text(item, key); !kept[name] {
			c.Removed = append(c.Removed, name)
		}
	}
	return c
}

// stringList compares two lists of names.
func stringList(field string, from, to []string) change {
	c := change{Field: field}
	for _, name := range to {
		if !slices.Contains(from, name) {
			c.Added = append(c.Added, name)
		}
	}
	for _, name := range from {
		if !slices.Contains(to, name) {
			c.Removed = append(c.Removed, name)
		}
	}
	return c
}

// hostnamesOf is the hostname list of a config or a body as Edka reads it:
// `hostname` first, then `hostnames`, lowercased and without repeats. It falls
// back to the record's columns for a stored config that lists none.
func hostnamesOf(m, record map[string]any) []string {
	names := []string{}
	add := func(v any) {
		name, _ := v.(string)
		name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, source := range []map[string]any{m, record} {
		add(source["hostname"])
		list, _ := source["hostnames"].([]any)
		for _, name := range list {
			add(name)
		}
		if len(names) > 0 {
			break
		}
	}
	return names
}

// unexposedNote is why a request that leaves a deployment unexposed leaves it
// no hostname.
const unexposedNote = "a deployment that is not exposed keeps no hostnames"

// hostnameNeeded is why Edka refuses a request that exposes a deployment under
// no hostname.
const hostnameNeeded = "an exposed deployment needs a hostname"

// secretNames are the names of the secrets a config lists, as Edka reads them.
func secretNames(v any) []string {
	names := []string{}
	list, _ := v.([]any)
	for _, item := range list {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// secretValueNames are the names whose value a `secret_values` sets, as Edka
// reads it: an object of values by name, or a list of {name, value}. An entry
// without a name or a value sets nothing.
func secretValueNames(v any) []string {
	names := []string{}
	add := func(name string, value any) {
		name = strings.TrimSpace(name)
		if name != "" && value != nil && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	switch values := v.(type) {
	case map[string]any:
		for name, value := range values {
			add(name, value)
		}
	case []any:
		for _, item := range values {
			if entry, ok := item.(map[string]any); ok {
				name, _ := entry["name"].(string)
				add(name, entry["value"])
			}
		}
	}
	slices.Sort(names)
	return names
}

// shown is a value as a diff prints it, on one line.
func shown(v any) string {
	if v == nil {
		return "(not set)"
	}
	if s, ok := v.(string); ok && s == "" {
		return `""`
	}
	s := ui.Text(v)
	if r := []rune(s); len(r) > 100 {
		s = string(r[:97]) + "…"
	}
	return s
}

// renderSettingsDiff prints what a settings request would change. Nothing of
// a secret is printed: `secrets` shows names, and `secret_values` the names
// whose value the request sets. With pinned, a diff that has changes says how
// to apply them only at the generation it compared with.
func (a *App) renderSettingsDiff(d *settingsDiff, pinned bool) error {
	if a.output != "table" {
		return ui.Render(a.Out, jsonBody(d), a.output, a.color)
	}
	name := ui.Clean(d.Deployment)
	count := "no change"
	switch len(d.Changes) {
	case 0:
	case 1:
		count = "1 change"
	default:
		count = fmt.Sprintf("%d changes", len(d.Changes))
	}
	at := ""
	if d.Generation > 0 {
		at = fmt.Sprintf(" at generation %d", d.Generation)
	}
	if _, err := fmt.Fprintf(a.Out, "%s%s: %s. Nothing was sent.\n", name, at, count); err != nil {
		return err
	}
	if len(d.Changes) > 0 {
		fmt.Fprintln(a.Out)
		tw := tabwriter.NewWriter(a.Out, 0, 4, 3, ' ', 0)
		for _, c := range d.Changes {
			lines := []string{}
			for _, entry := range c.Added {
				lines = append(lines, "+ "+ui.Clean(entry))
			}
			for _, entry := range c.Changed {
				lines = append(lines, "~ "+ui.Clean(entry))
			}
			for _, entry := range c.Removed {
				lines = append(lines, "- "+ui.Clean(entry))
			}
			if len(lines) == 0 {
				lines = []string{shown(c.From) + " → " + shown(c.To)}
			}
			if c.Note != "" {
				lines[len(lines)-1] += " (" + c.Note + ")"
			}
			for i, line := range lines {
				field := ""
				if i == 0 {
					field = ui.Paint(ui.Clean(c.Field), a.color)
				}
				fmt.Fprintf(tw, "  %s\t%s\n", field, line)
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	notes := [][2]string{{"Unchanged", strings.Join(d.Unchanged, ", ")}}
	for _, field := range d.Ignored {
		notes = append(notes, [2]string{"Ignored", field})
	}
	for _, field := range d.Refused {
		notes = append(notes, [2]string{"Refused", field})
	}
	printed := false
	for _, note := range notes {
		if note[1] == "" {
			continue
		}
		if !printed {
			fmt.Fprintln(a.Out)
			printed = true
		}
		fmt.Fprintf(a.Out, "%s: %s\n", note[0], ui.Clean(note[1]))
	}
	if pinned && len(d.Changes) > 0 && d.Generation > 0 {
		a.message("To apply this only while %s is at generation %d, run the command without --diff and with --expected-generation %d.", name, d.Generation, d.Generation)
	}
	return nil
}
