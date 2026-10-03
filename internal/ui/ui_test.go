package ui

import (
	"bytes"
	"encoding/json"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestJSONOutputRetainsEnvelopeWithoutANSI(t *testing.T) {
	var out bytes.Buffer
	body := []byte(`{"data":[{"name":"web"}],"nextCursor":"next"}`)
	if err := Render(&out, body, "json", true); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(out.Bytes(), &value) != nil || value["nextCursor"] != "next" || strings.Contains(out.String(), "\x1b") {
		t.Fatal(out.String())
	}
}
func TestTerminalOutputSanitizesRemoteControlSequences(t *testing.T) {
	for _, s := range []string{"\x1b[31mred\x1b[0m", "\x1b]52;c;token\aafter"} {
		clean := Clean(s)
		if strings.ContainsAny(clean, "\x1b\a") {
			t.Fatal("unsafe terminal output")
		}
	}
	var out bytes.Buffer
	if err := Render(&out, []byte(`{"data":[{"name":"web","id":"id","status":"running"}]}`), "table", false); err != nil || !strings.Contains(out.String(), "NAME") || !strings.Contains(out.String(), "running") {
		t.Fatal(out.String(), err)
	}
}
func TestStringTerminatorKeepsFollowingText(t *testing.T) {
	for in, want := range map[string]string{
		"see \x1b]8;;https://edka.io\x1b\\docs\x1b]8;;\x1b\\ now": "see docs now",
		"a\x1bPq#0;2;0;0;0\x1b\\b":                                "ab",
		"a\x1b_payload\ab":                                        "ab",
		"a\x1b]0;title\x1b[31mb":                                  "ab",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanTextKeepsLineBreaksAndTabs(t *testing.T) {
	in := "Exception in main\n\tat App.run(App.java:3)\x1b[31m\n\x1b]52;c;\ttoken\aafter"
	got := CleanText(in)
	if !strings.HasPrefix(got, "Exception in main\n\tat App.run(App.java:3)\n") || strings.ContainsAny(got, "\x1b\a") {
		t.Fatalf("CleanText(%q) = %q", in, got)
	}
}

func TestColorKeepsTableColumnsAligned(t *testing.T) {
	body := []byte(`{"data":[{"name":"api","id":"id1","status":"running"},{"name":"worker","id":"id2","status":"pending"}]}`)
	var plain, colored bytes.Buffer
	if err := Render(&plain, body, "table", false); err != nil {
		t.Fatal(err)
	}
	if err := Render(&colored, body, "table", true); err != nil {
		t.Fatal(err)
	}
	stripped := strings.ReplaceAll(strings.ReplaceAll(colored.String(), "\x1b[38;2;163;228;185m", ""), "\x1b[0m", "")
	if stripped != plain.String() {
		t.Fatal("color changed table alignment")
	}
}

func TestTablesNameRecordsWithoutANameField(t *testing.T) {
	var out bytes.Buffer
	body := []byte(`{"data":[{"id":"a1","app_name":"strapi","instance_name":"blog","status":"installed"},{"id":"a2","app_name":"excalidraw","instance_name":"default","status":"installed"},{"id":"d1","database_name":"orders","status":"ready"}]}`)
	if err := Render(&out, body, "table", false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "NAME ") || !strings.HasPrefix(lines[1], "blog ") || !strings.HasPrefix(lines[2], "excalidraw ") || !strings.HasPrefix(lines[3], "orders ") {
		t.Fatal(out.String())
	}
}

// Enter picks the highlighted choice; esc, q and ctrl+c quit without one.
func TestPickerKeys(t *testing.T) {
	choices := []Choice{{ID: "a1", Name: "alpha"}, {ID: "b2", Name: "beta"}}
	var m tea.Model = newPicker("Choose", choices)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p := m.(picker); p.selected != "b2" || cmd == nil {
		t.Fatalf("selected %q", p.selected)
	}
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}, {Code: 'v', Text: "v"}} {
		m, cmd := tea.Model(newPicker("Choose", choices)).Update(k)
		quit := cmd != nil && cmd() == tea.Quit()
		if p := m.(picker); p.selected != "" || quit != (k.Code != 'v') {
			t.Errorf("%s: selected %q, quit %v", k, p.selected, quit)
		}
	}
}

// The picker starts dark and switches to light styles when the terminal
// reports a light background.
func TestPickerFollowsTerminalBackground(t *testing.T) {
	m := newPicker("Choose", []Choice{{ID: "a1", Name: "alpha"}, {ID: "b2", Name: "beta"}})
	if m.Init() == nil {
		t.Fatal("picker does not ask for the background color")
	}
	lightText := "38;2;26;26;26"
	if strings.Contains(m.View().Content, lightText) {
		t.Fatal("picker starts with light styles")
	}
	light, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
	if !strings.Contains(light.(picker).View().Content, lightText) {
		t.Fatal("picker kept dark styles on a light background")
	}
}
