package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/edkadigital/cli/internal/api"
)

func TestShowIf(t *testing.T) {
	config := map[string]any{"access_enabled": true, "backup": false, "provider": "aws-s3", "replicas": float64(2)}
	for condition, want := range map[string]bool{
		"":                      true,
		"access_enabled":        true,
		"backup":                false,
		"missing":               false,
		"!backup":               true,
		"!access_enabled":       false,
		"provider === 'aws-s3'": true,
		`provider === "gcp"`:    false,
		"replicas === '2'":      true,
		// Like Edka, a comparison needs a value, so this reads as a setting name.
		"missing === ''": false,
		"access_enabled && provider === 'aws-s3'": true,
		"access_enabled && backup":                false,
	} {
		if got := showIf(condition, config); got != want {
			t.Errorf("%q: got %v", condition, got)
		}
	}
	if got := strings.Join(showIfNames("access_enabled && !backup && provider === 'aws-s3'"), ","); got != "access_enabled,backup,provider" {
		t.Fatal(got)
	}
}

func TestGuidedSettings(t *testing.T) {
	var catalog struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(whiteboardCatalog), &catalog); err != nil {
		t.Fatal(err)
	}
	fields := appFields(catalog.Data[0])
	asked, gates := []string{}, []string{}
	for _, f := range fields {
		if needsAnswer(f) {
			asked = append(asked, f.Name)
		}
		if gatesAnswer(f, fields) {
			gates = append(gates, f.Name)
		}
	}
	// Generated passwords, settings with defaults and hidden settings are not asked.
	if got := strings.Join(asked, ","); got != "api_key,postgres_instance,postgres_database,ingress_class" {
		t.Fatal(got)
	}
	if got := strings.Join(gates, ","); got != "access_enabled" {
		t.Fatal(got)
	}
	hostname := appField{Name: "hostname", Tab: "access", Config: map[string]any{"type": "string", "validation": "domain"}}
	webhook := appField{Name: "webhook_hostname", Tab: "access", Config: map[string]any{"type": "string", "validation": "domain"}}
	fields = append(fields, appField{Name: "webhook_ingress_class", Tab: "hooks", Config: map[string]any{"type": "dynamic-select", "data_source": "cluster-gateway-classes"}})
	if g, ok := trafficClassField(hostname, fields); !ok || g.Name != "ingress_class" {
		t.Fatal(g, ok)
	}
	if g, ok := trafficClassField(webhook, fields); !ok || g.Name != "webhook_ingress_class" {
		t.Fatal(g, ok)
	}
	if !isHostnameField(webhook) || isHostnameField(appField{Name: "smtp_host", Config: map[string]any{"validation": "domain"}}) {
		t.Fatal("hostname settings")
	}
}

func TestSettingLinesKeepTheValuesInOneColumn(t *testing.T) {
	short := settingLines([][2]string{{"Name", "whoami-two"}, {"Traffic class", "eg (default)"}})
	if want := "  Name                     whoami-two\n  Traffic class            eg (default)\n"; short != want {
		t.Fatalf("%q", short)
	}
	// A label longer than the column moves every value, not only its own.
	long := settingLines([][2]string{{"Namespace", "notes"}, {"Expose through the gateway", "yes"}})
	if want := "  Namespace                  notes\n  Expose through the gateway yes\n"; long != want {
		t.Fatalf("%q", long)
	}
}

func TestDomainChoices(t *testing.T) {
	domains := []map[string]any{
		{"domain": "*.edka.dev", "is_wildcard": true, "include_apex": true, "ingress_class": "eg"},
		{"domain": "Status.Acme.com.", "is_wildcard": false, "ingress_class": "eg"},
		{"domain": "*.internal.acme.com", "is_wildcard": true, "ingress_class": "eg-ts"},
	}
	got := []string{}
	for _, c := range domainChoices(domains, "eg") {
		got = append(got, c.ID+"="+c.Name)
	}
	if strings.Join(got, " ") != "*.edka.dev=<name>.edka.dev edka.dev=edka.dev status.acme.com=status.acme.com" {
		t.Fatal(got)
	}
	if n := len(domainChoices(domains, "")); n != 4 {
		t.Fatal(n)
	}
}

// A typo gets the question again, and one reader keeps the answers typed ahead.
func TestAskSettingAsksAgainAfterATypo(t *testing.T) {
	var errOut bytes.Buffer
	a := &App{In: strings.NewReader("3x\n4\n"), Err: &errOut}
	replicas := appField{Name: "replicas", Config: map[string]any{"type": "number", "label": "Replicas"}}
	value, label, err := a.askSetting(context.Background(), "c1", replicas, map[string]any{})
	if err != nil || value != float64(4) || label != "4" || !strings.Contains(errOut.String(), `replicas takes a number, not "3x"`) {
		t.Fatal(value, label, err, errOut.String())
	}
}

func TestAskSettingsAsksInTheAppsOrder(t *testing.T) {
	fields := []appField{{Name: "instance", Config: map[string]any{"type": "string"}}, {Name: "database", Config: map[string]any{"type": "string"}}}
	a := &App{In: strings.NewReader("orders\napp\n"), Err: io.Discard}
	configuration := map[string]any{}
	reported := []api.FieldError{{Field: "database", Message: "Required"}, {Field: "instance", Message: "Required"}}
	if asked, err := a.askSettings(context.Background(), "c1", fields, configuration, reported); !asked || err != nil || configuration["instance"] != "orders" || configuration["database"] != "app" {
		t.Fatal(asked, err, configuration)
	}
	// A setting the app does not have ends the questions before the first one.
	a = &App{In: strings.NewReader("app\n"), Err: io.Discard}
	configuration = map[string]any{}
	reported = []api.FieldError{{Field: "database", Message: "Required"}, {Field: "labels", Message: "Unknown field"}}
	if asked, err := a.askSettings(context.Background(), "c1", fields, configuration, reported); asked || err != nil || len(configuration) != 0 {
		t.Fatal(asked, err, configuration)
	}
}
