package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/auth"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// previewNumber is a preview's pull request as it is typed: 12.
func previewNumber(m map[string]any) string { return fmt.Sprint(number(m["pr_number"])) }

// previewAddress is where a preview answers, or empty when Edka names no address.
func previewAddress(m map[string]any) string {
	if address := text(m, "preview_url"); address != "" {
		return address
	}
	if host := text(m, "hostname"); host != "" {
		return "https://" + host
	}
	return ""
}

var previewColumns = []ui.Column{
	{Header: "PR", Value: func(m map[string]any) string { return "#" + previewNumber(m) }},
	ui.Field("STATUS", "status"),
	ui.Field("TITLE", "pr_title"),
	ui.Field("BRANCH", "head_ref"),
	ui.Field("AUTHOR", "pr_author"),
	{Header: "URL", Value: previewAddress},
	{Header: "EXPIRES", Value: func(m map[string]any) string { return when(m["expires_at"]) }},
}

// previewDeployment selects the Git deployment whose previews a command reads:
// the one named, else the one of --deployment or the link.
func (a *App) previewDeployment(ctx context.Context, target string) (*candidate, string, error) {
	c, err := a.resolveGitDeployment(ctx, target)
	if err != nil {
		return nil, "", err
	}
	if git, known := gitSource(c.Record); known && !git {
		return nil, "", notGit(c.Name)
	}
	id, err := safeID(c.ID)
	return c, "/api/deployments/" + id + "/previews", err
}

// notGit is the error for a deployment that has no previews because Edka
// builds it from no repository.
func notGit(name string) error {
	return fmt.Errorf("%s is an image deployment; previews belong to a deployment built from a GitHub repository", ui.Clean(name))
}

// previewRows lists the previews at path. The list of deployments does not
// always say which ones Edka builds, so the route's own answer counts too.
func (a *App) previewRows(ctx context.Context, c *candidate, path string, query url.Values) ([]map[string]any, error) {
	rows, err := a.objectsQuery(ctx, path, query)
	var refused *api.Error
	if errors.As(err, &refused) && refused.Status == 404 && refused.Reason == "GitHub deployment not found" {
		return nil, notGit(c.Name)
	}
	return rows, err
}

// previewCandidates lists previews to pick from, by pull request number.
func previewCandidates(rows []map[string]any) []candidate {
	candidates := []candidate{}
	for _, row := range rows {
		if row["pr_number"] == nil {
			continue
		}
		pr := previewNumber(row)
		candidates = append(candidates, candidate{ID: pr, Name: pr, Detail: details(text(row, "pr_title"), text(row, "status"), text(row, "head_ref")), Record: row})
	}
	return candidates
}

// resolvePreview selects a preview of the context deployment by its pull
// request number, written 12 or #12. It returns the deployment, the preview as
// Edka lists it, and the preview's path.
func (a *App) resolvePreview(ctx context.Context, target string) (*candidate, map[string]any, string, error) {
	target = strings.TrimPrefix(strings.TrimSpace(target), "#")
	if target != "" && strings.Trim(target, "0123456789") != "" {
		return nil, nil, "", fmt.Errorf("a preview is named by its pull request number, such as 12; list them with `edka previews list`")
	}
	c, path, err := a.previewDeployment(ctx, "")
	if err != nil {
		return nil, nil, "", err
	}
	rows, err := a.previewRows(ctx, c, path, nil)
	if err != nil {
		return nil, nil, "", err
	}
	candidates := previewCandidates(rows)
	switch {
	case len(candidates) == 0:
		return nil, nil, "", fmt.Errorf("%s has no previews; a pull request gets one when previews are on for the deployment", ui.Clean(c.Name))
	case target == "" && len(candidates) > 1 && a.noInput:
		return nil, nil, "", fmt.Errorf("%s has %d previews; name a pull request from `edka previews list`", ui.Clean(c.Name), len(candidates))
	}
	preview, err := a.pick(ctx, "preview", candidates, target)
	if errors.Is(err, errNotFound) {
		return nil, nil, "", fmt.Errorf("%s has no preview of pull request #%s; list them with `edka previews list`", ui.Clean(c.Name), target)
	}
	if err != nil {
		return nil, nil, "", err
	}
	return c, preview.Record, path + "/" + preview.ID, nil
}

// previewChoices completes a pull request number from the previews of the
// context deployment.
func (a *App) previewChoices(ctx context.Context) ([]candidate, error) {
	c, path, err := a.previewDeployment(ctx, "")
	if err != nil {
		return nil, err
	}
	rows, err := a.previewRows(ctx, c, path, nil)
	return previewCandidates(rows), err
}

func (a *App) addPreviews(root *cobra.Command) {
	previews := &cobra.Command{Use: "previews", Aliases: []string{"preview"}, Short: "List, inspect and delete pull request previews", Long: "A Git deployment with previews on gets one preview for each open pull request.\nThese commands read the previews of the linked deployment, or of the one\n--deployment names, and name a preview by its pull request number.", GroupID: "resources", Example: "  edka previews list\n  edka previews status 12\n  edka previews logs 12 --follow\n  edka previews open 12\n  edka previews delete 12 --deployment api"}
	a.strictGroup(previews)
	var deleted bool
	list := &cobra.Command{Use: "list [deployment]", Short: "List a deployment's previews", Args: cobra.MaximumNArgs(1), Example: "  edka previews list\n  edka previews list api --deleted --json", RunE: func(cmd *cobra.Command, args []string) error {
		c, path, err := a.previewDeployment(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		query := url.Values{}
		if deleted {
			query.Set("includeDeleted", "true")
		}
		rows, err := a.previewRows(cmd.Context(), c, path, query)
		if err != nil {
			return err
		}
		return a.renderRows(rows, previewColumns)
	}}
	list.Flags().BoolVar(&deleted, "deleted", false, "Include deleted previews")
	status := &cobra.Command{Use: "status [pr]", Short: "Show a preview, its replicas and pods", Long: "Show a preview as Edka records it, and what runs for it in the cluster. A\npreview that is still building or that failed runs nothing yet; the command\nthen shows the record with its error and says so on stderr.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, preview, path, err := a.resolvePreview(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		var runtime map[string]any
		if response, err := a.request(cmd.Context(), "GET", path+"/status", nil, nil); err != nil {
			reason, _, _ := strings.Cut(err.Error(), "\n")
			a.message("Could not read what runs for preview #%s: %s", previewNumber(preview), ui.Clean(reason))
		} else if runtime, err = identityData(response.Body); err != nil {
			return err
		}
		if a.output != "table" {
			record := map[string]any{"runtime": runtime}
			for key, value := range preview {
				record[key] = value
			}
			return ui.Render(a.Out, jsonBody(map[string]any{"data": record}), a.output, false)
		}
		state := text(preview, "status")
		if message := text(preview, "error_message"); message != "" {
			state += ": " + message
		}
		branch := text(preview, "head_ref")
		if base := text(preview, "base_ref"); branch != "" && base != "" {
			branch += " → " + base
		}
		replicas := ""
		if r, ok := runtime["replicas"].(map[string]any); ok {
			replicas = fmt.Sprintf("%d/%d ready, %d updated, %d available", number(r["ready"]), number(r["desired"]), number(r["updated"]), number(r["available"]))
		}
		if err := ui.Fields(a.Out, [][2]string{
			{"Preview", strings.TrimSpace("#" + previewNumber(preview) + " " + text(preview, "pr_title"))},
			{"Status", state},
			{"URL", previewAddress(preview)},
			{"Branch", branch},
			{"Commit", shortSHA(text(preview, "head_sha"))},
			{"Author", text(preview, "pr_author")},
			{"Namespace", text(preview, "namespace")},
			{"Image", text(runtime, "running_image")},
			{"Replicas", replicas},
			{"Expires", when(preview["expires_at"])},
		}, a.color); err != nil {
			return err
		}
		if pods := podsOf(runtime); len(pods) > 0 {
			fmt.Fprintln(a.Out)
			return ui.Table(a.Out, []ui.Column{ui.Field("POD", "name"), ui.Field("STATUS", "status"), ui.Field("READY", "ready"), ui.Field("RESTARTS", "restartCount")}, pods, a.color)
		}
		return nil
	}}
	logs := a.logsCommand("logs [pr]", "Read a preview's logs; follow with --follow", func(ctx context.Context, target string) (string, error) {
		_, _, path, err := a.resolvePreview(ctx, target)
		return path + "/logs", err
	})
	open := &cobra.Command{Use: "open [pr]", Short: "Open a preview in the browser", Long: "Print a preview's URL to stderr and open it in the browser. With --json, or\nwithout a terminal, the command prints the URL on stdout and opens nothing.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, preview, _, err := a.resolvePreview(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		address := previewAddress(preview)
		// The address comes from Edka; only a web address goes to the browser.
		parsed, err := url.Parse(address)
		if err != nil || address == "" || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
			return fmt.Errorf("preview #%s has no web address; its status is %s", previewNumber(preview), ui.Clean(first(text(preview, "status"), "unknown")))
		}
		a.message("%s", ui.Clean(address))
		if a.noInput || a.output == "json" {
			return ui.Render(a.Out, jsonBody(map[string]string{"url": address}), "json", false)
		}
		return auth.OpenBrowser(address)
	}}
	remove := &cobra.Command{Use: "delete <pr>", Short: "Delete a preview and what runs for it", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, preview, path, err := a.resolvePreview(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if err := a.confirm(fmt.Sprintf("Delete the preview of pull request #%s of deployment %s", previewNumber(preview), c.Name)); err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "DELETE", path, nil, nil)
		if err != nil {
			return err
		}
		a.message("✓ Deleting the preview of pull request #%s", previewNumber(preview))
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}}
	a.completes(a.gitDeploymentChoices, list)
	diagnose := a.previewDiagnoseCommand()
	a.completes(a.previewChoices, status, diagnose, logs, open, remove)
	previews.AddCommand(list, status, diagnose, logs, open, remove)
	root.AddCommand(previews)
}
