package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// The routes that create a deployment: from a container image, and from a
// GitHub repository that Edka builds.
const (
	createImageRoute = "/api/clusters/:id/deployments"
	createGitRoute   = "/api/clusters/:clusterId/deployments/git"
)

// createCommand creates a deployment from a JSON body, which --example and
// --schema describe.
func (a *App) createCommand() *cobra.Command {
	var data string
	var fields []string
	var git, example, schema, dryRun, link, wait bool
	var waitTimeout time.Duration
	create := &cobra.Command{Use: "create", Short: "Create a deployment from an image or a GitHub repository", Long: "Create a deployment in the linked or selected cluster from a JSON body. The body\nnames a container image, or with --git a GitHub repository that Edka builds. Its\n`github_repository_id` is Edka's ID of a connected repository, or its GitHub ID.\n\n--example prints a body to start from, and --schema prints the JSON Schema of\nevery field. Neither sends a request.\n\nWith --link, this directory is linked to the new deployment. With --wait, the\ncommand follows the first rollout, and with --git the first build before it.\nProgress goes to stderr.", Args: cobra.NoArgs, Example: "  edka deployments create --example > deployment.json\n  edka deployments create --data @deployment.json --link --wait\n  edka deployments create --data @deployment.json --field image_tag=1.30 --dry-run\n  edka deployments create --schema\n  edka deployments create --git --example", RunE: func(cmd *cobra.Command, _ []string) error {
		route := createImageRoute
		if git {
			route = createGitRoute
		}
		op := a.catalog.Find("POST", route)
		if op == nil {
			return fmt.Errorf("this binary has no POST %s", route)
		}
		if schema || example {
			if schema && example {
				return fmt.Errorf("pass --schema or --example, not both")
			}
			document := op.Body
			if example {
				document = op.Example()
			}
			if len(document) == 0 {
				return fmt.Errorf("this binary describes no body for POST %s", route)
			}
			return a.printDocument(document)
		}
		// The new deployment is named in the body, so a deployment from a flag or
		// a link has no part in it.
		if cmd.Flags().Changed("deployment") {
			return fmt.Errorf("--deployment names an existing deployment; the body names the new one")
		}
		if !cmd.Flags().Changed("wait-timeout") && git {
			waitTimeout = 15 * time.Minute
		}
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		body, err := a.body(data, fields)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return fmt.Errorf("provide the deployment with --data or --field; `%s --example` prints one to start from", commandLine(git))
		}
		c, err := a.resolveCluster(cmd.Context(), "")
		if err != nil {
			return err
		}
		cluster, err := safeID(c.ID)
		if err != nil {
			return err
		}
		path := "/api/clusters/" + cluster + "/deployments"
		if git {
			path += "/git"
		}
		if dryRun {
			return ui.Render(a.Out, jsonBody(map[string]any{"method": "POST", "path": path, "query": url.Values{}, "body": json.RawMessage(body)}), "json", false)
		}
		response, err := a.request(cmd.Context(), "POST", path, nil, body)
		if err != nil {
			return err
		}
		record, err := identityData(response.Body)
		if err != nil {
			return err
		}
		id, err := safeID(text(record, "id"))
		if err != nil {
			return fmt.Errorf("deployment submitted but no ID returned; inspect `edka deployments list`")
		}
		name := first(text(record, "name"), id)
		created := fmt.Sprintf("✓ Created deployment %s in cluster %s", ui.Clean(name), ui.Clean(c.Name))
		// The directory is linked before the wait, so a rollout that fails still
		// leaves it linked to the deployment that exists.
		if link {
			_, summary, err := a.saveLink(cmd.Context(), c, &candidate{ID: text(record, "id"), Name: name})
			if err != nil {
				return fmt.Errorf("deployment %s was created, but this directory was not linked: %w; run `edka link --deployment %s`", ui.Clean(name), err, ui.Clean(name))
			}
			created += "\n" + summary
		}
		if !wait {
			a.message("%s\n  Next: edka deployments status %s", created, ui.Clean(name))
			return a.render(response)
		}
		a.message("%s", created)
		if git {
			return a.awaitFirstBuild(cmd.Context(), id, name, record, waitTimeout)
		}
		generation := submittedGeneration(record)
		if generation <= 0 {
			return fmt.Errorf("deployment created but no revision generation returned; inspect `edka deployments status %s`", ui.Clean(name))
		}
		a.message("Waiting for generation %d…", generation)
		return a.awaitGeneration(cmd.Context(), id, name, generation, waitTimeout)
	}}
	create.Flags().StringVarP(&data, "data", "d", "", "Deployment JSON, @file.json, or @-")
	create.Flags().StringArrayVarP(&fields, "field", "f", nil, "Body field: key=value or key:=JSON")
	create.Flags().BoolVar(&git, "git", false, "Create a deployment that Edka builds from a GitHub repository")
	create.Flags().BoolVar(&example, "example", false, "Print an example body and send nothing")
	create.Flags().BoolVar(&schema, "schema", false, "Print the JSON Schema of the body and send nothing")
	create.Flags().BoolVar(&dryRun, "dry-run", false, "Print the request without sending it")
	create.Flags().BoolVar(&link, "link", false, "Link this directory to the new deployment")
	create.Flags().BoolVar(&wait, "wait", false, "Wait until the first rollout is healthy; with --git, follow the first build too")
	create.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "Maximum wait; 15m by default with --git")
	return create
}

// commandLine is `edka deployments create` as the user ran it, for hints.
func commandLine(git bool) string {
	if git {
		return "edka deployments create --git"
	}
	return "edka deployments create"
}

// awaitFirstBuild follows the build Edka starts for a new Git deployment, and
// the rollout of its image.
func (a *App) awaitFirstBuild(ctx context.Context, id, name string, record map[string]any, timeout time.Duration) error {
	build, _ := record["initial_build"].(map[string]any)
	buildID, err := safeID(text(build, "id"))
	if err != nil {
		return fmt.Errorf("deployment %s was created, but its first build did not start; start one with `edka build %s --wait`", ui.Clean(name), ui.Clean(name))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	a.message("Waiting for its first build…")
	return a.followBuild(ctx, id, name, buildID, number(record["spec_generation"]))
}
