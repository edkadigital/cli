package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/catalog"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

func (a *App) addOperations(api *cobra.Command, c *catalog.Catalog) {
	for _, op := range c.Operations {
		parts := strings.Fields(op.Command)
		parent := api
		for _, part := range parts[:len(parts)-1] {
			var child *cobra.Command
			for _, candidate := range parent.Commands() {
				if candidate.Name() == part {
					child = candidate
					break
				}
			}
			if child == nil {
				child = &cobra.Command{Use: part, Short: "Manage " + strings.ReplaceAll(part, "-", " ")}
				a.strictGroup(child)
				if parent == api {
					child.GroupID = "endpoints"
					switch part {
					case "clusters":
						child.Aliases = []string{"cluster"}
					case "deployments":
						child.Aliases = []string{"deployment"}
					case "databases":
						child.Aliases = []string{"database"}
					case "apps":
						child.Aliases = []string{"app"}
					case "cronjobs":
						child.Aliases = []string{"cronjob"}
					}
				}
				parent.AddCommand(child)
			}
			parent = child
		}
		a.addOperation(parent, parts[len(parts)-1], op)
	}
}
func (a *App) addOperation(parent *cobra.Command, name string, op catalog.Operation) {
	var data string
	var fields, queries []string
	var outputFile string
	var dryRun, schema bool
	values := map[string]*string{}
	positional := positionalParam(op)
	use, args := name, cobra.NoArgs
	if positional != nil {
		use, args = name+" ["+positional.Flag+"]", cobra.MaximumNArgs(1)
	}
	// Routes without a summary in Edka's source show their method and path once.
	route := op.Method + " " + op.Path
	short, long := route, route
	if op.Summary != "" {
		short, long = op.Summary, op.Summary+"\n\n"+route
	}
	cmd := &cobra.Command{Use: use, Short: short, Long: long + "\n\nUse --query key=value for filters. For writes, pass --data @file.json\nor --field name=value. Use name:=value for typed JSON fields.", Args: args}
	if op.StepUp {
		cmd.Long += "\n\nThis operation retains Edka's console step-up policy."
	}
	if op.Confirm {
		cmd.Long += "\n\nThis operation asks for confirmation before it runs, or takes --yes."
	}
	if len(op.Body) > 0 {
		cmd.Long += "\n\n--schema prints the JSON Schema of the request body, with an example."
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if schema {
			return a.printDocument(op.Body)
		}
		// An explicit context flag the route cannot use would otherwise be ignored.
		usesCluster := false
		for _, param := range op.Params {
			usesCluster = usesCluster || param.Flag == "cluster"
		}
		if cmd.Flags().Changed("cluster") && !usesCluster {
			return fmt.Errorf("--cluster does not apply to %s %s", op.Method, op.Path)
		}
		if cmd.Flags().Changed("deployment") {
			return fmt.Errorf("--deployment does not apply to %s %s; pass IDs with the flags in --help", op.Method, op.Path)
		}
		if len(args) > 0 {
			if positional.Flag == "cluster" {
				if cmd.Flags().Changed("cluster") && a.clusterFlag != args[0] {
					return fmt.Errorf("pass the cluster as an argument or with --cluster, not both")
				}
				a.cluster = args[0]
			} else if value := *values[positional.Name]; value != "" && value != args[0] {
				return fmt.Errorf("pass %s as an argument or with --%s, not both", positional.Flag, positional.Flag)
			}
		}
		path := op.Path
		for _, param := range op.Params {
			value := ""
			if param.Flag == "cluster" {
				var err error
				value, err = a.clusterID(cmd.Context())
				if err != nil {
					return err
				}
			} else {
				value = *values[param.Name]
				if value == "" && len(args) > 0 && param.Name == positional.Name {
					value = args[0]
				}
				if value == "" {
					return fmt.Errorf("provide --%s for %s", param.Flag, op.Path)
				}
				escaped, err := safeID(value)
				if err != nil {
					return err
				}
				value = escaped
			}
			path = strings.ReplaceAll(path, ":"+param.Name, value)
		}
		query, err := parseQuery(queries)
		if err != nil {
			return err
		}
		body, err := a.body(data, fields)
		if err != nil {
			return err
		}
		if (op.Method == "GET" || op.Method == "HEAD") && len(body) > 0 {
			return fmt.Errorf("this read operation does not accept a JSON body")
		}
		if dryRun {
			return ui.Render(a.Out, jsonBody(map[string]any{"method": op.Method, "path": path, "query": query, "body": json.RawMessage(defaultBody(body))}), "json", false)
		}
		if op.Confirm {
			if err := a.confirm(op.Method + " " + path); err != nil {
				return err
			}
		}
		response, err := a.request(cmd.Context(), op.Method, path, query, body)
		if err != nil {
			return err
		}
		if outputFile != "" {
			return a.writeOutput(outputFile, response.Body)
		}
		return a.render(response)
	}
	for _, param := range op.Params {
		if param.Flag == "cluster" {
			continue
		}
		value := new(string)
		values[param.Name] = value
		cmd.Flags().StringVar(value, param.Flag, "", "Resource path parameter: "+param.Name)
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "JSON request body, @file.json, or @- for stdin")
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "Body field: key=value (string), key:=JSON (typed)")
	cmd.Flags().StringArrayVarP(&queries, "query", "q", nil, "Query parameter: key=value (repeatable)")
	cmd.Flags().StringVar(&outputFile, "output-file", "", "Save response privately to a file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the request without sending the operation")
	if len(op.Body) > 0 {
		cmd.Flags().BoolVar(&schema, "schema", false, "Print the JSON Schema of the request body and send nothing")
	}
	parent.AddCommand(cmd)
}

// printDocument prints a schema or an example of the catalog with its fields
// in the order Edka declares them, which groups the fields that belong together.
func (a *App) printDocument(document json.RawMessage) error {
	var indented bytes.Buffer
	if err := json.Indent(&indented, document, "", "  "); err != nil {
		return err
	}
	_, err := fmt.Fprintln(a.Out, indented.String())
	return err
}

// positionalParam is the path parameter an optional argument fills: the last
// non-cluster parameter, or the cluster when the route has no other.
func positionalParam(op catalog.Operation) *catalog.Param {
	var result *catalog.Param
	for i := range op.Params {
		if op.Params[i].Flag != "cluster" {
			result = &op.Params[i]
		}
	}
	if result == nil && len(op.Params) > 0 {
		result = &op.Params[len(op.Params)-1]
	}
	return result
}
func defaultBody(body []byte) []byte {
	if len(body) == 0 {
		return []byte("null")
	}
	return body
}
func parseQuery(fields []string) (url.Values, error) {
	q := url.Values{}
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("query must use key=value: %q", field)
		}
		q.Add(key, value)
	}
	return q, nil
}
func (a *App) body(data string, fields []string) ([]byte, error) {
	var raw []byte
	if strings.HasPrefix(data, "@") {
		var err error
		if data == "@-" {
			raw, err = api.ReadBody(a.In)
		} else {
			f, openErr := os.Open(strings.TrimPrefix(data, "@"))
			if openErr != nil {
				return nil, openErr
			}
			raw, err = api.ReadBody(f)
			_ = f.Close()
		}
		if err != nil {
			return nil, err
		}
	} else {
		raw = []byte(data)
	}
	if len(raw) > 0 && !json.Valid(raw) {
		return nil, fmt.Errorf("request body must be valid JSON")
	}
	if len(fields) == 0 {
		return raw, nil
	}
	body := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil || body == nil {
			return nil, fmt.Errorf("--field requires a JSON object body")
		}
	}
	for _, field := range fields {
		key, value, typed := strings.Cut(field, ":=")
		if !typed {
			var ok bool
			key, value, ok = strings.Cut(field, "=")
			if !ok {
				return nil, fmt.Errorf("field must use key=value or key:=JSON: %q", field)
			}
		}
		if key == "" {
			return nil, fmt.Errorf("field name cannot be empty")
		}
		var v any = value
		if typed {
			if err := json.Unmarshal([]byte(value), &v); err != nil {
				return nil, fmt.Errorf("invalid JSON for field %s: %w", key, err)
			}
		}
		parts := strings.Split(key, ".")
		target := body
		for _, part := range parts[:len(parts)-1] {
			if part == "" {
				return nil, fmt.Errorf("invalid field path %s", key)
			}
			child, exists := target[part]
			if !exists {
				child = map[string]any{}
				target[part] = child
			}
			m, ok := child.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("field %s conflicts with a non-object value", key)
			}
			target = m
		}
		if parts[len(parts)-1] == "" {
			return nil, fmt.Errorf("invalid field path %s", key)
		}
		target[parts[len(parts)-1]] = v
	}
	return json.Marshal(body)
}
