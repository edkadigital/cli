// Package ui renders terminal output and keyboard-driven selectors.
package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

const Accent = "#A3E4B9"

func IsTerminal(w io.Writer) bool { f, ok := w.(*os.File); return ok && term.IsTerminal(int(f.Fd())) }
func InputTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
func Clean(s string) string {
	// Drop CSI/OSC escapes and control characters from server-controlled labels.
	var b strings.Builder
	escaped := false
	osc := false
	for _, r := range s {
		if osc {
			// OSC, DCS, SOS, PM and APC end with BEL or ST (ESC \); an ESC always
			// ends the string, and the escape branch consumes its final byte.
			if r == 7 {
				osc = false
			} else if r == 27 {
				osc = false
				escaped = true
			}
			continue
		}
		if escaped {
			if r == ']' || r == 'P' || r == 'X' || r == '^' || r == '_' {
				osc = true
				escaped = false
				continue
			}
			if r >= '@' && r <= '~' && r != '[' {
				escaped = false
			}
			continue
		}
		if r == 27 {
			escaped = true
			continue
		}
		if !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CleanText is Clean for multi-line text such as errors and logs. It keeps
// line breaks and tabs, which indent suggestions and stack traces.
func CleanText(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		parts := strings.Split(line, "\t")
		for j, part := range parts {
			parts[j] = Clean(part)
		}
		lines[i] = strings.Join(parts, "\t")
	}
	return strings.Join(lines, "\n")
}
func Paint(s string, color bool) string {
	if !color {
		return s
	}
	return "\x1b[38;2;163;228;185m" + s + "\x1b[0m"
}
func Render(w io.Writer, body []byte, mode string, color bool) error {
	if mode == "raw" {
		_, err := w.Write(body)
		return err
	}
	if mode == "json" {
		if len(body) == 0 {
			_, err := fmt.Fprintln(w, "null")
			return err
		}
		if !json.Valid(body) {
			return fmt.Errorf("response is not JSON; use --output raw")
		}
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			return err
		}
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(v)
	}
	if len(body) == 0 {
		_, err := fmt.Fprintln(w, Paint("✓ Done", color))
		return err
	}
	var v any
	if json.Unmarshal(body, &v) != nil {
		_, err := fmt.Fprintln(w, Clean(string(body)))
		return err
	}
	if m, ok := v.(map[string]any); ok {
		if d, exists := m["data"]; exists {
			v = d
		}
	}
	if rows, ok := v.([]any); ok {
		return table(w, rows, color)
	}
	if m, ok := v.(map[string]any); ok {
		for _, key := range []string{"items", "rows", "clusters", "deployments", "apps", "databases", "resources", "entries"} {
			if rows, ok := m[key].([]any); ok {
				return table(w, rows, color)
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
		for _, key := range keys {
			fmt.Fprintf(tw, "%s\t%s\n", Paint(Clean(strings.ReplaceAll(key, "_", " ")), color), value(m[key]))
		}
		return tw.Flush()
	}
	_, err := fmt.Fprintln(w, value(v))
	return err
}
func value(v any) string {
	if v == nil {
		return "—"
	}
	switch n := v.(type) {
	case string:
		return Clean(n)
	case map[string]any:
		if name, ok := n["name"]; ok {
			return value(name)
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return Clean(string(data))
}
func table(w io.Writer, rows []any, color bool) error {
	records := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		m, _ := row.(map[string]any)
		records = append(records, m)
	}
	columns := []Column{}
	for _, m := range records {
		if recordName(m) != "" {
			columns = append(columns, Column{Header: "NAME", Value: recordName})
			break
		}
	}
	priority := []string{"id", "status", "cluster_name", "type", "provider", "location", "image", "version", "namespace", "date_created"}
	for _, key := range priority {
		for _, m := range records {
			if _, exists := m[key]; exists {
				columns = append(columns, Field(strings.ToUpper(strings.ReplaceAll(key, "_", " ")), key))
				break
			}
		}
		if len(columns) == 6 {
			break
		}
	}
	if len(rows) > 0 && len(columns) == 0 {
		for _, row := range rows {
			fmt.Fprintln(w, value(row))
		}
		return nil
	}
	return Table(w, columns, records, color)
}

// nameKeys are the fields Edka uses for a record's human name. Only some
// resources use name: app instances have display, instance and app names,
// and databases have database_name.
var nameKeys = []string{"name", "display_name", "instance_name", "database_name", "app_name", "release_name", "slug", "title"}

// recordName is the first name a record has. Edka stores "default" as the
// instance name of unnamed app instances, so that value is skipped.
func recordName(m map[string]any) string {
	for _, key := range nameKeys {
		if s, ok := m[key].(string); ok && s != "" && !(key == "instance_name" && s == "default") {
			return Clean(s)
		}
	}
	return ""
}

// Column is one table column computed from an API record.
type Column struct {
	Header string
	Value  func(map[string]any) string
}

// Field is a column showing one record key.
func Field(header, key string) Column {
	return Column{Header: header, Value: func(m map[string]any) string { return Text(m[key]) }}
}

// Text formats an API value for terminal display.
func Text(v any) string { return value(v) }

// Table renders records with fixed columns. Cells are sanitized by the column
// functions that use Text, truncated, and empty cells show a dash.
func Table(w io.Writer, columns []Column, rows []map[string]any, color bool) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No resources found.")
		return err
	}
	var buffer bytes.Buffer
	tw := tabwriter.NewWriter(&buffer, 0, 4, 3, ' ', 0)
	for i, column := range columns {
		if i > 0 {
			fmt.Fprint(tw, "\t")
		}
		fmt.Fprint(tw, column.Header)
	}
	fmt.Fprintln(tw)
	for _, row := range rows {
		for i, column := range columns {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			s := Clean(column.Value(row))
			if s == "" {
				s = "—"
			}
			r := []rune(s)
			if len(r) > 80 {
				s = string(r[:77]) + "…"
			}
			fmt.Fprint(tw, s)
		}
		fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	text := buffer.String()
	if color {
		header, rest, ok := strings.Cut(text, "\n")
		if ok {
			text = Paint(header, true) + "\n" + rest
		}
	}
	_, err := io.WriteString(w, text)
	return err
}

// Fields renders label and value pairs, omitting empty values.
func Fields(w io.Writer, fields [][2]string, color bool) error {
	tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	for _, field := range fields {
		v := Clean(field[1])
		if v == "" || v == "—" {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\n", Paint(field[0], color), v)
	}
	return tw.Flush()
}

type Choice struct {
	ID     string
	Name   string
	Detail string
}
type item struct{ Choice }

func (i item) Title() string       { return Clean(i.Name) }
func (i item) Description() string { return Clean(i.Detail) }
func (i item) FilterValue() string { return Clean(i.Name) + " " + Clean(i.ID) }

type picker struct {
	list      list.Model
	selected  string
	cancelled bool
}

func newPicker(title string, choices []Choice) picker {
	items := make([]list.Item, len(choices))
	for i, c := range choices {
		items[i] = item{c}
	}
	m := picker{list: list.New(items, list.NewDefaultDelegate(), 76, 16)}
	m.list.Title = title
	m.list.SetShowStatusBar(false)
	// Bubbles v2 binds quit to v and labels it select; q and esc cancel.
	m.list.KeyMap.Quit = key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "quit"))
	m.setBackground(true)
	return m
}

// setBackground styles the picker for a dark or light terminal. The list
// starts with dark styles and doesn't pass its filter styles to the filter
// input, so every part is set here.
func (m *picker) setBackground(dark bool) {
	styles := list.DefaultStyles(dark)
	styles.Title = lipgloss.NewStyle().Foreground(lipgloss.Color(Accent)).Bold(true)
	m.list.Styles = styles
	m.list.FilterInput.SetStyles(styles.Filter)
	m.list.Help.Styles = help.DefaultStyles(dark)
	m.list.Paginator.ActiveDot = styles.ActivePaginationDot.String()
	m.list.Paginator.InactiveDot = styles.InactivePaginationDot.String()
	delegate := list.NewDefaultDelegate()
	delegate.Styles = list.NewDefaultItemStyles(dark)
	m.list.SetDelegate(delegate)
}

// Init asks for the background color once the picker owns the terminal, so
// commands without a picker never query it. The picker keeps its dark styles
// until a reply arrives, and a terminal that never replies doesn't delay it.
func (m picker) Init() tea.Cmd  { return tea.RequestBackgroundColor }
func (m picker) View() tea.View { return tea.NewView(m.list.View()) }
func (m picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.setBackground(msg.IsDark())
		return m, nil
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, min(msg.Height, 18))
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			if !m.list.SettingFilter() {
				m.cancelled = true
				return m, tea.Quit
			}
		case "enter":
			if m.list.FilterState() != list.Filtering {
				if i, ok := m.list.SelectedItem().(item); ok {
					m.selected = i.ID
					return m, tea.Quit
				}
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}
func Select(ctx context.Context, title string, choices []Choice, in io.Reader, out io.Writer) (string, error) {
	if len(choices) == 0 {
		return "", fmt.Errorf("no resources available")
	}
	if len(choices) == 1 {
		return choices[0].ID, nil
	}
	if !InputTerminal(in) || !IsTerminal(out) {
		return "", fmt.Errorf("multiple resources match; pass an explicit ID")
	}
	p := tea.NewProgram(newPicker(title, choices), tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx))
	model, err := p.Run()
	if err != nil {
		return "", err
	}
	m := model.(picker)
	if m.cancelled || m.selected == "" {
		return "", fmt.Errorf("selection cancelled")
	}
	return m.selected, nil
}
