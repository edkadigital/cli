package cli

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/edkadigital/cli/internal/ui"
)

var showIfEquality = regexp.MustCompile(`^(\w+)\s*===\s*['"](.+)['"]$`)

// showIf evaluates a setting's show_if condition the way Edka does: names of
// true booleans, "!name", and "name === 'value'", joined by &&.
func showIf(condition string, configuration map[string]any) bool {
	for _, part := range strings.Split(condition, "&&") {
		part = strings.TrimSpace(part)
		switch match := showIfEquality.FindStringSubmatch(part); {
		case part == "":
		case strings.HasPrefix(part, "!"):
			if configuration[strings.TrimSpace(part[1:])] == true {
				return false
			}
		case match != nil:
			value := configuration[match[1]]
			if value == nil {
				value = ""
			}
			if fmt.Sprint(value) != match[2] {
				return false
			}
		default:
			if configuration[part] != true {
				return false
			}
		}
	}
	return true
}

// showIfNames lists the settings a show_if condition reads.
func showIfNames(condition string) []string {
	names := []string{}
	for _, part := range strings.Split(condition, "&&") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "!")
		if match := showIfEquality.FindStringSubmatch(part); match != nil {
			part = match[1]
		}
		if part = strings.TrimSpace(part); part != "" {
			names = append(names, part)
		}
	}
	return names
}

// withDefaults is a configuration with each unset setting's default, as the
// install will see it.
func withDefaults(fields []appField, configuration map[string]any) map[string]any {
	effective := map[string]any{}
	for _, f := range fields {
		if v, ok := f.Config["default"]; ok {
			effective[f.Name] = v
		}
	}
	for k, v := range configuration {
		effective[k] = v
	}
	return effective
}

// needsAnswer reports whether a setting is required and nothing fills it in:
// it has no default and is not a generated password.
func needsAnswer(f appField) bool {
	generated := f.kind() == "password" && f.Config["generate"] == true
	return f.Config["required"] == true && f.kind() != "hidden" && isBlank(f.Config["default"]) && !generated
}

// gatesAnswer reports whether a boolean setting shows a setting that needs an
// answer, such as the switch that exposes an app and shows its hostname.
func gatesAnswer(f appField, fields []appField) bool {
	if f.kind() != "boolean" {
		return false
	}
	for _, g := range fields {
		if needsAnswer(g) && slices.Contains(showIfNames(text(g.Config, "show_if")), f.Name) {
			return true
		}
	}
	return false
}

// trafficClassField finds the traffic class a hostname setting is exposed
// through: webhook_hostname pairs with webhook_ingress_class, and a plain
// hostname with the traffic class setting in its tab.
func trafficClassField(f appField, fields []appField) (appField, bool) {
	isClass := func(g appField) bool {
		source := text(g.Config, "data_source")
		return source == "cluster-ingress-classes" || source == "cluster-gateway-classes"
	}
	prefix := strings.TrimSuffix(f.Name, "hostname")
	if g, ok := findField(fields, prefix+"ingress_class"); ok && isClass(g) {
		return g, true
	}
	for _, g := range fields {
		if g.Tab == f.Tab && isClass(g) {
			return g, true
		}
	}
	return appField{}, false
}

func isHostnameField(f appField) bool {
	return (f.Name == "hostname" || strings.HasSuffix(f.Name, "_hostname")) && text(f.Config, "validation") == "domain"
}

var subdomainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// domainChoices lists the hostnames Edka manages for a traffic class: each
// domain, and for a wildcard domain its apex when it includes the apex, and a
// choice to enter one label under it.
func domainChoices(domains []map[string]any, trafficClass string) []ui.Choice {
	choices := []ui.Choice{}
	for _, d := range domains {
		name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(text(d, "domain")), "."))
		if name == "" || trafficClass != "" && text(d, "ingress_class") != trafficClass {
			continue
		}
		if d["is_wildcard"] != true {
			choices = append(choices, ui.Choice{ID: name, Name: name})
			continue
		}
		base := strings.TrimPrefix(name, "*.")
		choices = append(choices, ui.Choice{ID: name, Name: "<name>." + base, Detail: "A name you choose under " + base})
		if d["include_apex"] == true {
			choices = append(choices, ui.Choice{ID: base, Name: base})
		}
	}
	return choices
}

// askHostname asks for a hostname Edka manages on the chosen traffic class, as
// the console does. The Tailscale traffic class takes any hostname.
func (a *App) askHostname(ctx context.Context, cluster, clusterName string, f appField, trafficClass string) (string, error) {
	if trafficClass == "tailscale" {
		return a.askLine(f.label() + ": ")
	}
	domains, err := a.objects(ctx, "/api/clusters/"+cluster+"/domains")
	if err != nil {
		return "", err
	}
	choices := domainChoices(domains, trafficClass)
	if len(choices) == 0 {
		return "", fmt.Errorf("no domains in cluster %s use traffic class %s; add one with `edka domains add <domain> --class %s`, or set the hostname with --set %s=<hostname>", ui.Clean(clusterName), ui.Clean(trafficClass), ui.Clean(trafficClass), f.Name)
	}
	if len(choices) == 1 {
		a.message("Domain: %s, the only one for traffic class %s", ui.Clean(choices[0].Name), ui.Clean(trafficClass))
	}
	chosen, err := ui.Select(ctx, "Choose a domain for "+ui.Clean(f.label()), choices, a.In, a.Err)
	if err != nil || !strings.HasPrefix(chosen, "*.") {
		return chosen, err
	}
	base := strings.TrimPrefix(chosen, "*.")
	for {
		label, err := a.askLine(fmt.Sprintf("Name under %s: ", base))
		if err != nil {
			return "", err
		}
		label = strings.ToLower(label)
		if subdomainLabel.MatchString(label) {
			return label + "." + base, nil
		}
		a.message("Use letters, digits and hyphens, with no dots.")
	}
}

func (a *App) askLine(prompt string) (string, error) {
	fmt.Fprint(a.Err, ui.Clean(prompt))
	answer, err := a.readLine()
	if err != nil && answer == "" {
		return "", fmt.Errorf("no answer given")
	}
	return strings.TrimSpace(answer), nil
}

// askYesNo asks a yes or no question; an empty answer takes the default.
func (a *App) askYesNo(question string, yes bool) (bool, error) {
	hint := " [y/N] "
	if yes {
		hint = " [Y/n] "
	}
	for {
		answer, err := a.askLine(question + hint)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return yes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// guideInstall asks in a terminal for what an install needs and the flags left
// out, then shows every chosen setting and asks to go ahead. Other settings keep
// their defaults.
func (a *App) guideInstall(ctx context.Context, cluster *candidate, clusterID string, app *candidate, fields []appField, configuration map[string]any, labels map[string]string, name *string) error {
	shown := [][2]string{}
	if *name == "" && number(app.Record["installed_count"]) > 0 {
		answer, err := a.askLine(fmt.Sprintf("%s is already installed in cluster %s. Name for this one: ", ui.Clean(app.Name), ui.Clean(cluster.Name)))
		if err != nil {
			return err
		}
		*name = answer
	}
	if *name != "" {
		shown = append(shown, [2]string{"Name", *name})
	}
	given := map[string]bool{}
	for key := range configuration {
		given[key] = true
	}
	effective := withDefaults(fields, configuration)
	for _, f := range fields {
		if given[f.Name] {
			shown = append(shown, [2]string{f.label(), first(labels[f.Name], displayValue(f, configuration[f.Name]))})
			continue
		}
		if f.kind() == "hidden" || !showIf(text(f.Config, "show_if"), effective) {
			continue
		}
		var value any
		var label string
		var err error
		switch {
		case gatesAnswer(f, fields):
			value, err = a.askYesNo(ui.Clean(f.label())+"?", effective[f.Name] == true)
		case !needsAnswer(f):
			continue
		case isHostnameField(f):
			class := ""
			if g, ok := trafficClassField(f, fields); ok {
				class = ui.Text(effective[g.Name])
				if isBlank(effective[g.Name]) {
					class = ""
				}
			}
			value, err = a.askHostname(ctx, clusterID, cluster.Name, f, class)
		default:
			value, label, err = a.askSetting(ctx, clusterID, f, effective)
		}
		if err != nil {
			return err
		}
		configuration[f.Name], effective[f.Name] = value, value
		shown = append(shown, [2]string{f.label(), first(label, displayValue(f, value))})
	}
	fmt.Fprintf(a.Err, "\nInstall %s in cluster %s with:\n", ui.Clean(app.Name), ui.Clean(cluster.Name))
	fmt.Fprint(a.Err, settingLines(shown))
	fmt.Fprintf(a.Err, "Other settings keep their defaults; list them with `%s`.\n", catalogCommand(app))
	if a.yes {
		return nil
	}
	ok, err := a.askYesNo("Install?", true)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("installation cancelled")
	}
	return nil
}

// settingLines lists chosen settings with their values in one column, set
// past the longest label.
func settingLines(rows [][2]string) string {
	width := 24
	for _, row := range rows {
		width = max(width, utf8.RuneCountInString(ui.Clean(row[0])))
	}
	var lines strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&lines, "  %-*s %s\n", width, ui.Clean(row[0]), ui.Clean(row[1]))
	}
	return lines.String()
}

func displayValue(f appField, v any) string {
	if f.secret() {
		return "(hidden)"
	}
	if b, ok := v.(bool); ok {
		if b {
			return "yes"
		}
		return "no"
	}
	return ui.Text(v)
}
