package catalog

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestConfirmFollowsTheRoute(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/api/clusters/c1/unprotect", true},
		{"POST", "/api/clusters/c1/scale-down", true},
		{"POST", "/api/clusters/c1/cleanup-orphaned-nodes", true},
		{"POST", "/api/clusters/c1/explorer/actions/drain-node", true},
		{"POST", "/api/clusters/c1/explorer/actions/delete-pod", true},
		{"POST", "/api/cronjobs/j1/restore-version", true},
		{"PUT", "/api/clusters/c1/nodepools", true},
		{"PUT", "/api/clusters/c1/nodepools/", true},
		{"DELETE", "/api/deployments/d1", true},
		{"GET", "/api/clusters/c1/nodepools", false},
		{"POST", "/api/clusters/c1/protect", false},
		{"PATCH", "/api/clusters/c1/archive", false},
		{"GET", "/api/clusters/c1/scale-down", false},
		// A raw path names the same route with a query string, encoding or other case.
		{"POST", "/api/clusters/c1/unprotect?reason=test", true},
		{"POST", "/api/clusters/c1/unprotect/?reason=test", true},
		{"POST", "/api/clusters/c1/%75nprotect", true},
		{"POST", "/api/clusters/c1/Unprotect", true},
		{"PUT", "/api/clusters/c1/NodePools?dry=1", true},
		{"POST", "/api/clusters/c1/protect?next=unprotect", false},
		// The route decides, not a word in a resource's name.
		{"POST", "/api/deployments/delete/restart", false},
		{"POST", "/api/deployments/rollback/builds", false},
		// A path outside the catalog asks when it deletes or has one of the words.
		{"DELETE", "/api/unlisted/u1", true},
		{"POST", "/api/unlisted/u1/purge", true},
		{"POST", "/api/unlisted/u1/Purge?now=1", true},
		{"POST", "/api/unlisted/u1/refresh", false},
		{"GET", "/api/unlisted/u1/purge", false},
	} {
		if got := c.Confirm(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s: %v", tc.method, tc.path, got)
		}
	}
}

// The generator follows Edka's delegated route policy: organization routes are
// read-only, and the billing portal is refused though it is a GET.
func TestGeneratedRoutesFollowDelegatedPolicy(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	organization := 0
	for _, op := range c.Operations {
		if op.Path == "/api/organization/customer-portal" {
			t.Errorf("%s %s is refused to CLI tokens", op.Method, op.Path)
		}
		if op.Path == "/api/organization" || strings.HasPrefix(op.Path, "/api/organization/") {
			organization++
			if op.Method != "GET" {
				t.Errorf("%s %s: organization routes are read-only", op.Method, op.Path)
			}
		}
	}
	if organization == 0 {
		t.Fatal("the catalog has no organization routes")
	}
}

// Every route's own path gives its flag, so no route hides another's through a
// parameter, and the generator's rules hold in the embedded catalog.
func TestGeneratedConfirmFlags(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ConfirmWords) == 0 {
		t.Fatal("the catalog has no confirm words")
	}
	parameter := regexp.MustCompile(`:[A-Za-z]+`)
	confirmed := 0
	for _, op := range c.Operations {
		if op.Confirm {
			confirmed++
		}
		if op.Method == "GET" && op.Confirm || op.Method == "DELETE" && !op.Confirm {
			t.Errorf("%s %s: confirm=%v", op.Method, op.Path, op.Confirm)
		}
		if got := c.Confirm(op.Method, parameter.ReplaceAllString(op.Path, "x1")); got != op.Confirm {
			t.Errorf("%s %s: its path gives %v, the catalog says %v", op.Method, op.Path, got, op.Confirm)
		}
	}
	if confirmed < 50 {
		t.Fatalf("only %d routes ask for confirmation", confirmed)
	}
}

// A body schema describes the fields of its route, and its example sends only
// those. The routes that create, change and scale a deployment have one, and
// so do the routes the CLI suggests for a database or a cronjob.
func TestBodySchemasDescribeTheirExample(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range [][2]string{{"POST", "/api/clusters/:id/deployments"}, {"POST", "/api/clusters/:clusterId/deployments/git"}, {"PATCH", "/api/deployments/:id/settings"}, {"PATCH", "/api/deployments/:id/scale"}, {"POST", "/api/clusters/:clusterId/databases"}, {"POST", "/api/clusters/:id/cronjobs"}} {
		if op := c.Find(route[0], route[1]); op == nil || len(op.Body) == 0 {
			t.Errorf("%s %s has no body schema", route[0], route[1])
		}
	}
	for _, op := range c.Operations {
		if len(op.Body) == 0 {
			if op.Example() != nil {
				t.Errorf("%s %s has an example and no schema", op.Method, op.Path)
			}
			continue
		}
		if op.Method == "GET" {
			t.Errorf("GET %s takes no body", op.Path)
		}
		var schema struct {
			Type       string         `json:"type"`
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		var example map[string]any
		if err := json.Unmarshal(op.Body, &schema); err != nil || schema.Type != "object" || len(schema.Properties) == 0 {
			t.Errorf("%s %s: the body schema describes no object: %v", op.Method, op.Path, err)
			continue
		}
		if err := json.Unmarshal(op.Example(), &example); err != nil || len(example) == 0 {
			t.Errorf("%s %s: no example: %v", op.Method, op.Path, err)
		}
		for field := range example {
			if _, described := schema.Properties[field]; !described {
				t.Errorf("%s %s: the example sends %s, which the schema does not describe", op.Method, op.Path, field)
			}
		}
		for _, field := range schema.Required {
			if _, sent := example[field]; !sent {
				t.Errorf("%s %s: the example lacks the required %s", op.Method, op.Path, field)
			}
			// The CLI fills a path parameter itself.
			for _, param := range op.Params {
				if field == param.Name || field == "cluster_id" {
					t.Errorf("%s %s: the body requires %s, which the path names", op.Method, op.Path, field)
				}
			}
		}
	}
}
