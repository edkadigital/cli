package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

func (a *App) addUp(root *cobra.Command) {
	var data string
	var fields []string
	var wait, diff bool
	var expected int
	var waitTimeout time.Duration
	up := &cobra.Command{Use: "up [deployment]", Short: "Deploy image changes or redeploy a successful Git build", Long: "Create an image deployment with --data, update an existing deployment's settings,\nor redeploy its latest successful Git build. An image deployment without new\nsettings is restarted. To build a new Git revision, run `edka build --wait`; with\nauto-deploy on, it also rolls the build out.\n\nWith --wait, progress goes to stderr: pods updated and ready, and pods that fail.\n\nWith --diff, the command compares the settings it would send with the deployment\nand sends nothing. It lists each field that changes, and for a list such as\n`config.env_variables` each entry the request adds, changes or removes, since a\nrequest replaces the lists it sends. Secret values are never shown. Edka refuses\na change sent with --expected-generation when the deployment is at another\ngeneration than the one --diff printed.", GroupID: "work", Args: cobra.MaximumNArgs(1), Example: "  edka up api --wait\n  edka up api --field config.image_tag=v2 --wait\n  edka up api --field config.image_tag=v2 --diff\n  edka up api --field config.image_tag=v2 --expected-generation 5 --wait\n  edka up --cluster production --data @deployment.json", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		body, err := a.body(data, fields)
		if err != nil {
			return err
		}
		creates := len(body) > 0 && len(args) == 0 && a.deployment == ""
		if diff || cmd.Flags().Changed("expected-generation") {
			flag := "--expected-generation"
			if diff {
				flag = "--diff"
			}
			if len(body) == 0 {
				return fmt.Errorf("%s is for a settings change; pass --data or --field", flag)
			}
			if creates {
				return fmt.Errorf("%s is for a change to an existing deployment; name one, or see a new one with `edka deployments create --dry-run`", flag)
			}
			if expected < 1 && !diff {
				return fmt.Errorf("expected-generation must be positive")
			}
		}
		if diff {
			return a.diffUp(cmd.Context(), argument(args), body)
		}
		if cmd.Flags().Changed("expected-generation") {
			if body, err = withExpectedGeneration(body, expected); err != nil {
				return err
			}
		}
		method := "POST"
		path := ""
		var id, name string
		if creates {
			cluster, err := a.clusterID(cmd.Context())
			if err != nil {
				return err
			}
			path = "/api/clusters/" + cluster + "/deployments"
		} else {
			c, err := a.resolveDeployment(cmd.Context(), argument(args))
			if err != nil {
				return err
			}
			if id, err = safeID(c.ID); err != nil {
				return err
			}
			name = c.Name
			if len(body) > 0 {
				method = "PATCH"
				path = "/api/deployments/" + id + "/settings"
			} else {
				detail, err := a.request(cmd.Context(), "GET", "/api/deployments/"+id, nil, nil)
				if err != nil {
					return err
				}
				record, err := identityData(detail.Body)
				if err != nil {
					return err
				}
				if git, ok := record["github_deployment_id"].(string); ok && git != "" {
					path = "/api/deployments/" + id + "/deploy"
				} else {
					path = "/api/deployments/" + id + "/restart"
				}
			}
		}
		response, err := a.request(cmd.Context(), method, path, nil, body)
		if err != nil {
			var refused *api.Error
			if cmd.Flags().Changed("expected-generation") && errors.As(err, &refused) && refused.Status == 409 && refused.Reason == "Change refused" {
				return fmt.Errorf("%w\n%s is no longer at generation %d; compare the change again with --diff", err, ui.Clean(name), expected)
			}
			return err
		}
		if !wait {
			return a.render(response)
		}
		record, err := identityData(response.Body)
		if err != nil {
			return err
		}
		if id == "" {
			if id, err = safeID(text(record, "id")); err != nil {
				return fmt.Errorf("deployment submitted but no ID returned; inspect `edka deployments list`")
			}
		}
		name = first(name, text(record, "name"), id)
		generation := submittedGeneration(record)
		if generation <= 0 {
			return fmt.Errorf("deployment submitted but no revision generation returned; inspect `edka status`")
		}
		a.message("Deployment submitted. Waiting for generation %d…", generation)
		return a.awaitGeneration(cmd.Context(), id, name, generation, waitTimeout)
	}}
	up.Flags().StringVarP(&data, "data", "d", "", "Deployment JSON, @file.json, or @-")
	up.Flags().StringArrayVarP(&fields, "field", "f", nil, "Body field: key=value or key:=JSON")
	up.Flags().BoolVar(&diff, "diff", false, "Show what the settings would change, and send nothing")
	up.Flags().IntVar(&expected, "expected-generation", 0, "Have Edka refuse the change unless the deployment is at this generation")
	up.Flags().BoolVar(&wait, "wait", false, "Wait until the submitted revision becomes healthy")
	up.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "Maximum rollout wait")
	a.completes(a.deploymentChoices, up)
	build := a.buildCommand()
	build.GroupID = "work"
	root.AddCommand(up, build)
}

// diffUp prints what a settings body would change in a deployment.
func (a *App) diffUp(ctx context.Context, target string, body []byte) error {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return fmt.Errorf("--diff needs a JSON object body")
	}
	c, err := a.resolveDeployment(ctx, target)
	if err != nil {
		return err
	}
	id, err := safeID(c.ID)
	if err != nil {
		return err
	}
	response, err := a.request(ctx, "GET", "/api/deployments/"+id, nil, nil)
	if err != nil {
		return err
	}
	record, err := identityData(response.Body)
	if err != nil {
		return err
	}
	top, config := a.settingsFields()
	return a.renderSettingsDiff(diffSettings(record, request, top, config), true)
}

// withExpectedGeneration names the generation a settings body was compared at.
func withExpectedGeneration(body []byte, generation int) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return nil, fmt.Errorf("--expected-generation needs a JSON object body")
	}
	if sent, set := request["expected_generation"]; set && number(sent) != generation {
		return nil, fmt.Errorf("the body names expected_generation %s and --expected-generation %d; pass one", ui.Text(sent), generation)
	}
	request["expected_generation"] = generation
	return json.Marshal(request)
}

// buildCommand builds a Git deployment's current branch, optionally waiting.
func (a *App) buildCommand() *cobra.Command {
	var ref, commit string
	var buildWait bool
	var buildTimeout time.Duration
	build := &cobra.Command{Use: "build [deployment]", Short: "Build a Git deployment from its current branch", Long: "Build a Git deployment from its current branch, or from --ref and --commit.\n\nWith --wait, the build log and each build step go to stderr. When the deployment\nhas auto-deploy on, --wait also waits for the rollout of the new image, so the\ncommand ends when the build runs. With auto-deploy off, deploy the build with\n`edka up --wait`.", Args: cobra.MaximumNArgs(1), Example: "  edka build api --wait\n  edka build api --ref refs/heads/main --commit abc1234", RunE: func(cmd *cobra.Command, args []string) error {
		if buildWait && buildTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		c, err := a.resolveGitDeployment(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		id, err := safeID(c.ID)
		if err != nil {
			return err
		}
		current, err := a.request(cmd.Context(), "GET", "/api/deployments/"+id, nil, nil)
		if err != nil {
			return err
		}
		deployment, err := identityData(current.Body)
		if err != nil {
			return err
		}
		if git, known := gitSource(deployment); known && !git {
			name := ui.Clean(c.Name)
			return fmt.Errorf("%s is an image deployment with no GitHub repository to build; deploy image changes with `edka up %s --wait`", name, name)
		}
		// A rollout the build starts has a later generation than the current one.
		before := number(deployment["spec_generation"])
		body := map[string]string{}
		if ref != "" {
			body["github_ref"] = ref
		}
		if commit != "" {
			body["commit_sha"] = commit
		}
		response, err := a.request(cmd.Context(), "POST", "/api/deployments/"+id+"/builds", nil, jsonBody(body))
		if err != nil {
			return err
		}
		if !buildWait {
			return a.render(response)
		}
		record, err := identityData(response.Body)
		if err != nil {
			return err
		}
		buildID, err := safeID(text(record, "id"))
		if err != nil {
			return fmt.Errorf("build submitted but no ID returned; inspect `edka api deployments builds list --id %s`", id)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), buildTimeout)
		defer cancel()
		a.message("Build submitted. Waiting for it to finish…")
		return a.followBuild(ctx, id, c.Name, buildID, before)
	}}
	build.Flags().StringVar(&ref, "ref", "", "GitHub ref override")
	build.Flags().StringVar(&commit, "commit", "", "Commit SHA override")
	build.Flags().BoolVar(&buildWait, "wait", false, "Stream the build log until it finishes, then follow an auto-deploy")
	build.Flags().DurationVar(&buildTimeout, "wait-timeout", 15*time.Minute, "Maximum wait for the build and its rollout")
	a.completes(a.gitDeploymentChoices, build)
	return build
}

// followBuild streams a submitted build until it finishes, follows the rollout
// auto-deploy starts for it, and prints the build. before is the deployment's
// generation when the build started.
func (a *App) followBuild(ctx context.Context, id, name, buildID string, before int) error {
	detail, build, err := a.waitBuild(ctx, id, buildID)
	if err != nil {
		return err
	}
	if status := text(build, "status"); status != "success" {
		if message := text(build, "error_message"); message != "" {
			return fmt.Errorf("build %s: %s", status, message)
		}
		return fmt.Errorf("build %s", status)
	}
	built := "✓ Built " + ui.Clean(first(text(build, "image_tag"), shortSHA(text(build, "commit_sha")), buildID))
	if took := buildDuration(build); took != "" {
		built += " in " + took
	}
	a.message("%s", built)
	github, err := a.request(ctx, "GET", "/api/deployments/"+id+"/github", nil, nil)
	if err != nil {
		return err
	}
	settings, err := identityData(github.Body)
	if err != nil {
		return err
	}
	generation := 0
	if autoDeploy, _ := settings["auto_deploy_enabled"].(bool); autoDeploy {
		if generation, err = a.buildRollout(ctx, id, before, text(build, "image_tag")); err != nil {
			return err
		}
		if generation == 0 {
			return fmt.Errorf("the build succeeded, but no rollout of it started; deploy it with `edka up %s --wait`", ui.Clean(name))
		}
		a.message("Auto-deploy started generation %d…", generation)
		if _, err := a.waitDeployment(ctx, id, name, generation); err != nil {
			return err
		}
	} else {
		a.message("  Auto-deploy is off. Next: edka up %s --wait", ui.Clean(name))
	}
	if a.output != "table" {
		return a.render(detail)
	}
	commitLine, _, _ := strings.Cut(text(build, "commit_message"), "\n")
	deployed := ""
	if generation > 0 {
		deployed = fmt.Sprintf("generation %d", generation)
	}
	return ui.Fields(a.Out, [][2]string{
		{"Deployment", name},
		{"Build", buildID},
		{"Status", text(build, "status")},
		{"Commit", strings.TrimSpace(shortSHA(text(build, "commit_sha")) + " " + commitLine)},
		{"Image", text(build, "image_uri")},
		{"Duration", buildDuration(build)},
		{"Deployed", deployed},
	}, a.color)
}

// submittedGeneration is the revision a deployment write started, or 0 when
// the response names none.
func submittedGeneration(record map[string]any) int {
	if generation := number(record["generation"]); generation != 0 {
		return generation
	}
	return number(record["spec_generation"])
}

// awaitGeneration waits until generation is healthy, then shows the runtime status.
func (a *App) awaitGeneration(ctx context.Context, id, name string, generation int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runtime, err := a.waitDeployment(ctx, id, name, generation)
	if err != nil {
		return err
	}
	return a.renderDeploymentStatus(runtime)
}
func shortSHA(sha string) string { return sha[:min(len(sha), 7)] }

// buildDuration is how long a build ran, or empty when the build reports none.
func buildDuration(build map[string]any) string {
	if seconds := number(build["duration_seconds"]); seconds > 0 {
		return (time.Duration(seconds) * time.Second).String()
	}
	return ""
}
func number(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
