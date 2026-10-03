// Package catalog embeds the Edka API endpoints a CLI token can call.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

type Param struct {
	Name string `json:"name"`
	Flag string `json:"flag"`
}
type Operation struct {
	Command string  `json:"command"`
	Method  string  `json:"method"`
	Path    string  `json:"path"`
	Summary string  `json:"summary"`
	Params  []Param `json:"params"`
	StepUp  bool    `json:"step_up"`
	// Confirm marks a route that destroys, replaces or revokes something, so
	// the CLI asks before it sends the request. The generator decides it.
	Confirm bool `json:"confirm"`
	// Body is the JSON Schema of the request body, with one example under
	// "examples". Edka writes it from the schema the route parses its body
	// with, and a route Edka has not described has none.
	Body json.RawMessage `json:"body,omitempty"`
}
type Catalog struct {
	// SourceSHA is the revision of the Edka API the catalog was generated from.
	SourceSHA string `json:"source_sha"`
	// ConfirmWords are path segments that ask for confirmation on a route the
	// catalog doesn't have.
	ConfirmWords []string    `json:"confirm_words"`
	Operations   []Operation `json:"operations"`
}

//go:embed operations.json
var source []byte

func Load() (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(source, &c); err != nil {
		return nil, fmt.Errorf("read embedded command catalog: %w", err)
	}
	return &c, nil
}

// Find returns the operation of a method and a route path as Edka registers
// it, or nil.
func (c *Catalog) Find(method, path string) *Operation {
	for i := range c.Operations {
		if c.Operations[i].Method == method && c.Operations[i].Path == path {
			return &c.Operations[i]
		}
	}
	return nil
}

// Example is the request body the route's schema gives as an example, or nil
// when the route has no schema.
func (op Operation) Example() json.RawMessage {
	var schema struct {
		Examples []json.RawMessage `json:"examples"`
	}
	if len(op.Body) == 0 || json.Unmarshal(op.Body, &schema) != nil || len(schema.Examples) == 0 {
		return nil
	}
	return schema.Examples[0]
}

// Confirm reports whether a request typed as a raw path asks for confirmation.
// The route it names decides. A path that names no route in the catalog asks
// when it is a DELETE or has one of ConfirmWords as a segment.
func (c *Catalog) Confirm(method, path string) bool {
	if method == "GET" || method == "HEAD" {
		return false
	}
	// A raw path may carry a query string and percent-encoding, and Edka matches
	// routes without regard to case.
	path, _, _ = strings.Cut(path, "?")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, segment := range segments {
		if decoded, err := url.PathUnescape(segment); err == nil {
			segment = decoded
		}
		segments[i] = strings.ToLower(segment)
	}
	known := false
	for _, op := range c.Operations {
		if op.Method != method || !op.matches(segments) {
			continue
		}
		// A path can name two routes, one of them through a parameter.
		if op.Confirm {
			return true
		}
		known = true
	}
	if known {
		return false
	}
	return method == "DELETE" || slices.ContainsFunc(segments, func(segment string) bool { return slices.Contains(c.ConfirmWords, segment) })
}

// matches reports whether lowercased path segments name the operation's route.
func (op Operation) matches(segments []string) bool {
	route := strings.Split(strings.Trim(op.Path, "/"), "/")
	if len(route) != len(segments) {
		return false
	}
	for i, part := range route {
		if strings.HasPrefix(part, ":") {
			if segments[i] == "" {
				return false
			}
		} else if strings.ToLower(part) != segments[i] {
			return false
		}
	}
	return true
}
