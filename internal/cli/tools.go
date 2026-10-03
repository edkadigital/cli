package cli

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/edkadigital/cli/internal/auth"
	"github.com/edkadigital/cli/internal/catalog"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

func (a *App) addTools(root *cobra.Command) *cobra.Command {
	apiCmd := &cobra.Command{Use: "api", Short: "Call any Edka API endpoint, by command or by path", GroupID: "tools", Long: "Every Edka API endpoint a CLI token can call, plus raw requests.\n\nFind a command with `edka api operations --search <term>`, or call a path directly\nwith `edka api get /api/...`.", Example: "  edka api clusters nodepools list --cluster production\n  edka api operations --search backup\n  edka api get /api/inventory/resources --json"}
	apiCmd.AddGroup(&cobra.Group{ID: "requests", Title: "Requests:"}, &cobra.Group{ID: "endpoints", Title: "Endpoints:"})
	a.strictGroup(apiCmd)
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		var data string
		var fields, queries []string
		var outputFile string
		cmd := &cobra.Command{Use: strings.ToLower(method) + " <path>", Short: method + " an Edka API path", GroupID: "requests", Args: cobra.ExactArgs(1), Example: "  edka api get /api/clusters --json\n  edka api post /api/clusters --data @cluster.json", RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if !strings.HasPrefix(path, "/api/") {
				return fmt.Errorf("path must start with /api/")
			}
			query, err := parseQuery(queries)
			if err != nil {
				return err
			}
			body, err := a.body(data, fields)
			if err != nil {
				return err
			}
			if method == "GET" && len(body) > 0 {
				return fmt.Errorf("GET does not accept a JSON body")
			}
			if a.catalog.Confirm(method, path) {
				if err := a.confirm(method + " " + path); err != nil {
					return err
				}
			}
			response, err := a.request(cmd.Context(), method, path, query, body)
			if err != nil {
				return err
			}
			if outputFile != "" {
				return a.writeOutput(outputFile, response.Body)
			}
			return a.render(response)
		}}
		cmd.Flags().StringVarP(&data, "data", "d", "", "JSON body, @file.json or @-")
		cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "Body field: key=value or key:=JSON")
		cmd.Flags().StringArrayVarP(&queries, "query", "q", nil, "Query parameter: key=value")
		cmd.Flags().StringVar(&outputFile, "output-file", "", "Save response privately to a file")
		apiCmd.AddCommand(cmd)
	}
	var search string
	operations := &cobra.Command{Use: "operations", Short: "Search every endpoint command", GroupID: "requests", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		c, err := catalog.Load()
		if err != nil {
			return err
		}
		rows := []map[string]any{}
		for _, op := range c.Operations {
			if search != "" && !strings.Contains(strings.ToLower(op.Command+" "+op.Path+" "+op.Summary), strings.ToLower(search)) {
				continue
			}
			rows = append(rows, map[string]any{"command": "edka api " + op.Command, "method": op.Method, "path": op.Path, "summary": op.Summary, "step_up": op.StepUp, "confirm": op.Confirm})
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(rows), a.output, false)
		}
		return ui.Table(a.Out, []ui.Column{
			ui.Field("COMMAND", "command"),
			ui.Field("METHOD", "method"),
			ui.Field("PATH", "path"),
			{Header: "STEP-UP", Value: func(m map[string]any) string {
				if m["step_up"] == true {
					return "yes"
				}
				return ""
			}},
			{Header: "CONFIRM", Value: func(m map[string]any) string {
				if m["confirm"] == true {
					return "yes"
				}
				return ""
			}},
		}, rows, a.color)
	}}
	operations.Flags().StringVar(&search, "search", "", "Filter commands and API paths")
	apiCmd.AddCommand(operations)
	completion := &cobra.Command{Use: "completion <bash|zsh|fish|powershell>", Short: "Generate shell completions", GroupID: "tools", Args: cobra.ExactArgs(1), ValidArgs: []string{"bash", "zsh", "fish", "powershell"}, RunE: func(_ *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return root.GenBashCompletionV2(a.Out, true)
		case "zsh":
			return root.GenZshCompletion(a.Out)
		case "fish":
			return root.GenFishCompletion(a.Out, true)
		case "powershell":
			return root.GenPowerShellCompletionWithDesc(a.Out)
		}
		return fmt.Errorf("supported shells: bash, zsh, fish, powershell")
	}}
	doctor := &cobra.Command{Use: "doctor", Short: "Check CLI connectivity, authentication and context", GroupID: "tools", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		checks := []map[string]string{}
		failed := false
		add := func(name string, err error) {
			status := "ok"
			detail := ""
			if err != nil {
				failed = true
				status = "failed"
				detail = err.Error()
			}
			checks = append(checks, map[string]string{"name": name, "status": status, "detail": detail})
		}
		// Doctor loads the context itself, so a file it can't read is a failed
		// check. A link it can't read is left out and the rest runs without it.
		err := a.loadConfig()
		var linkErr error
		if err == nil {
			linkErr = a.loadLink()
			err = a.resolveContext(cmd)
		}
		add("Configuration and profile", err)
		if a.linkPath != "" || linkErr != nil {
			add("Project link", linkErr)
		}
		if err == nil {
			_, _, err = auth.Discover(cmd.Context(), a.current.APIURL, a.HTTP)
			add("API and OAuth discovery", err)
			_, err = a.request(cmd.Context(), "GET", "/api/cli/whoami", nil, nil)
			add("Identity and organization", err)
			if a.cluster != "" {
				_, err = a.clusterID(cmd.Context())
				add("Cluster context", err)
			}
		}
		if err := ui.Render(a.Out, jsonBody(checks), a.output, a.color); err != nil {
			return err
		}
		// The table has no room for a failure's detail.
		if a.output == "table" {
			for _, check := range checks {
				if check["status"] == "failed" {
					a.message("%s: %s", check["name"], ui.CleanText(check["detail"]))
				}
			}
		}
		if failed {
			return fmt.Errorf("some checks failed; inspect the results above")
		}
		return nil
	}}
	version := &cobra.Command{Use: "version", Short: "Show the version, commit and API revision of this binary", Long: "Show what this binary is: its version, the commit it was built from, its Go\nrelease, and the revision of the Edka API that the `edka api` commands were\ngenerated from. Include the output in a bug report.", GroupID: "tools", Args: cobra.NoArgs, Example: "  edka version\n  edka version --json", RunE: func(_ *cobra.Command, _ []string) error {
		info, _ := debug.ReadBuildInfo()
		details := buildOf(a.Version, info, a.catalog)
		if a.output == "json" {
			return ui.Render(a.Out, jsonBody(details), "json", false)
		}
		if _, err := fmt.Fprintf(a.Out, "edka %s\n", a.Version); err != nil {
			return err
		}
		// Without color, so the output pastes into a bug report as it is.
		return ui.Fields(a.Out, details.fields(), false)
	}}
	markStandalone(operations, completion, doctor, version)
	root.AddCommand(apiCmd, completion, doctor, version)
	return apiCmd
}
