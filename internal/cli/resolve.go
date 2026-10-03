package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/edkadigital/cli/internal/ui"
	"golang.org/x/sync/errgroup"
)

// candidate is a resource that its ID or one of its exact names can select.
type candidate struct {
	ID     string
	Name   string
	Detail string
	Names  []string
	Record map[string]any
}

func text(m map[string]any, key string) string { s, _ := m[key].(string); return s }

var errNotFound = errors.New("not found")

// notFoundIn explains that a lookup was limited to the context cluster.
func (a *App) notFoundIn(err error) error {
	if errors.Is(err, errNotFound) && a.clusterName != "" {
		return fmt.Errorf("%w in cluster %s; choose another with --cluster", err, a.clusterName)
	}
	return err
}

// pick selects one candidate by ID or exact name. Several matches prompt in a
// terminal and otherwise fail, naming the matches so a script can pass an ID.
func (a *App) pick(ctx context.Context, label string, candidates []candidate, target string) (*candidate, error) {
	matches := []candidate{}
	for _, c := range candidates {
		if target == "" || target == c.ID || target == c.Name || slices.Contains(c.Names, target) {
			matches = append(matches, c)
		}
	}
	switch {
	case len(matches) == 0 && target == "":
		return nil, fmt.Errorf("no %ss available", label)
	case len(matches) == 0:
		return nil, fmt.Errorf("%s %q was %w", label, target, errNotFound)
	case len(matches) == 1:
		return &matches[0], nil
	case a.noInput:
		names := []string{}
		for _, m := range matches[:min(len(matches), 5)] {
			names = append(names, ui.Clean(m.Name)+" ("+m.ID+")")
		}
		if len(matches) > 5 {
			names = append(names, "…")
		}
		return nil, fmt.Errorf("%d %ss match; pass an ID: %s", len(matches), label, strings.Join(names, ", "))
	}
	choices := make([]ui.Choice, len(matches))
	for i, m := range matches {
		choices[i] = ui.Choice{ID: m.ID, Name: m.Name, Detail: m.Detail}
	}
	id, err := ui.Select(ctx, "Choose "+label, choices, a.In, a.Err)
	if err != nil {
		return nil, err
	}
	for i := range matches {
		if matches[i].ID == id {
			return &matches[i], nil
		}
	}
	return nil, fmt.Errorf("selection cancelled")
}

func (a *App) clusterRows(ctx context.Context) ([]map[string]any, error) {
	if a.clusters == nil {
		rows, err := a.objects(ctx, "/api/clusters")
		if err != nil {
			return nil, err
		}
		a.clusters = rows
	}
	return a.clusters, nil
}

// clusterCandidates lists rows to pick from.
func clusterCandidates(rows []map[string]any) []candidate {
	candidates := []candidate{}
	for _, row := range rows {
		if id := text(row, "id"); id != "" {
			candidates = append(candidates, candidate{ID: id, Name: first(text(row, "name"), id), Detail: first(text(row, "status"), id), Record: row})
		}
	}
	return candidates
}

// resolveCluster selects a cluster by argument, flag, environment, link or
// profile, prompting when none is set, and makes it the context cluster.
func (a *App) resolveCluster(ctx context.Context, target string) (*candidate, error) {
	target = first(target, a.cluster)
	if target == "" && a.noInput {
		return nil, fmt.Errorf("choose a cluster with --cluster or run `edka link`")
	}
	rows, err := a.clusterRows(ctx)
	if err != nil {
		return nil, err
	}
	candidates := clusterCandidates(rows)
	c, err := a.pick(ctx, "cluster", candidates, target)
	if err != nil {
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no clusters available; create one with `edka clusters create <name>`")
		}
		return nil, err
	}
	a.cluster, a.clusterName = c.ID, c.Name
	return c, nil
}

// clusterID resolves the context cluster and returns its escaped ID.
func (a *App) clusterID(ctx context.Context) (string, error) {
	c, err := a.resolveCluster(ctx, "")
	if err != nil {
		return "", err
	}
	return safeID(c.ID)
}

// deploymentRows lists deployments in the context cluster, or in every
// cluster when there is none or all is set. Rows always carry cluster_name.
func (a *App) deploymentRows(ctx context.Context, all bool) ([]map[string]any, error) {
	if all || a.cluster == "" {
		return a.objects(ctx, "/api/deployments")
	}
	cluster, err := a.clusterID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := a.objects(ctx, "/api/clusters/"+cluster+"/deployments")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if text(row, "cluster_name") == "" {
			row["cluster_name"] = a.clusterName
		}
	}
	return rows, nil
}

// gitSource reports whether a deployment record names its GitHub source, and
// whether it has one. The organization-wide list names none.
func gitSource(m map[string]any) (git, known bool) {
	v, known := m["github_deployment_id"]
	id, _ := v.(string)
	return id != "", known
}

// sourceOf is a deployment's GitHub repository and branch, or its image.
func sourceOf(m map[string]any) string {
	repository := text(m, "github_repository_full_name")
	if repository == "" {
		return imageOf(m)
	}
	if ref := strings.TrimPrefix(text(m, "github_ref"), "refs/heads/"); ref != "" {
		return repository + " @ " + ref
	}
	return repository
}

// details joins the parts a picker shows under a name, skipping empty ones.
func details(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

// deploymentCandidates lists rows to pick from. With gitOnly, it leaves out
// rows that show a deployment has no GitHub repository.
func deploymentCandidates(rows []map[string]any, gitOnly bool) []candidate {
	candidates := []candidate{}
	for _, row := range rows {
		id := text(row, "id")
		if git, known := gitSource(row); id == "" || gitOnly && known && !git {
			continue
		}
		candidates = append(candidates, candidate{ID: id, Name: first(text(row, "name"), id), Detail: details(text(row, "status"), text(row, "cluster_name"), sourceOf(row)), Record: row})
	}
	return candidates
}

// resolveDeployment selects a deployment by argument, flag, environment or link.
func (a *App) resolveDeployment(ctx context.Context, target string) (*candidate, error) {
	return a.selectDeployment(ctx, target, false)
}

// resolveGitDeployment selects a deployment like resolveDeployment, but the
// picker offers only deployments built from a GitHub repository.
func (a *App) resolveGitDeployment(ctx context.Context, target string) (*candidate, error) {
	return a.selectDeployment(ctx, target, true)
}

func (a *App) selectDeployment(ctx context.Context, target string, gitOnly bool) (*candidate, error) {
	target = first(target, a.deployment)
	if target == "" && a.noInput {
		return nil, fmt.Errorf("choose a deployment with --deployment or `edka link --deployment <name>`")
	}
	rows, err := a.deploymentRows(ctx, false)
	if err != nil {
		return nil, err
	}
	// A named deployment stays selectable, so the caller can say why it doesn't fit.
	gitOnly = gitOnly && target == ""
	candidates := deploymentCandidates(rows, gitOnly)
	c, err := a.pick(ctx, "deployment", candidates, target)
	if err != nil {
		switch {
		case len(candidates) == 0 && gitOnly && len(rows) > 0 && a.clusterName != "":
			return nil, fmt.Errorf("no Git deployments in cluster %s; choose another with --cluster", a.clusterName)
		case len(candidates) == 0 && gitOnly && len(rows) > 0:
			return nil, fmt.Errorf("no Git deployments available")
		case len(candidates) == 0:
			return nil, fmt.Errorf("no deployments available; create one with `edka up --data @deployment.json`")
		}
		return nil, a.notFoundIn(err)
	}
	a.deployment = c.ID
	return c, nil
}
func (a *App) deploymentID(ctx context.Context, target string) (string, error) {
	c, err := a.resolveDeployment(ctx, target)
	if err != nil {
		return "", err
	}
	return safeID(c.ID)
}

// clusterReads is how many clusters clusterScopedRows reads at a time.
const clusterReads = 6

// clusterScopedRows reads a cluster's list at /api/clusters/:id/<suffix> for the
// context cluster, or for every cluster when there is none or all is set; these
// resources have no organization-wide list with the same fields. Rows carry
// cluster_id and cluster_name, and keep the order of the clusters.
func (a *App) clusterScopedRows(ctx context.Context, all bool, suffix string) ([]map[string]any, error) {
	return a.clusterScopedLists(ctx, all, suffix, rowsOf)
}

// clusterScopedLists is clusterScopedRows for a response that holds its records
// in more than one list: records returns the rows of the body a path answered with.
func (a *App) clusterScopedLists(ctx context.Context, all bool, suffix string, records func(path string, body []byte) ([]map[string]any, error)) ([]map[string]any, error) {
	clusters, err := a.clusterRows(ctx)
	if err != nil {
		return nil, err
	}
	if !all && a.cluster != "" {
		if _, err := a.clusterID(ctx); err != nil {
			return nil, err
		}
		selected := []map[string]any{}
		for _, c := range clusters {
			if text(c, "id") == a.cluster {
				selected = append(selected, c)
			}
		}
		clusters = selected
	}
	paths := make([]string, len(clusters))
	for i, c := range clusters {
		id, err := safeID(text(c, "id"))
		if err != nil {
			return nil, err
		}
		paths[i] = "/api/clusters/" + id + "/" + suffix
	}
	// The reads run together, clusterReads at a time, and the first failure
	// stops the rest. They share one client, so the credentials are read once.
	lists := make([][]map[string]any, len(clusters))
	err = a.busy(ctx, func() error {
		client, err := a.client(ctx)
		if err != nil {
			return err
		}
		group, ctx := errgroup.WithContext(ctx)
		group.SetLimit(clusterReads)
		for i, path := range paths {
			group.Go(func() error {
				response, err := client.Do(ctx, "GET", path, nil, nil)
				if err != nil {
					return err
				}
				lists[i], err = records(path, response.Body)
				return err
			})
		}
		return group.Wait()
	})
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for i, c := range clusters {
		for _, row := range lists[i] {
			row["cluster_name"] = text(c, "name")
			if text(row, "cluster_id") == "" {
				row["cluster_id"] = text(c, "id")
			}
		}
		rows = append(rows, lists[i]...)
	}
	return rows, nil
}

// resolveScoped selects one clusterScopedRows record by ID or exact name.
// names lists the names that select a record; the first is shown.
func (a *App) resolveScoped(ctx context.Context, label, suffix, target, empty string, names func(map[string]any) []string, detail func(map[string]any) string) (*candidate, error) {
	if target == "" && a.noInput {
		return nil, fmt.Errorf("name the %s; list them with `edka %ss list`", label, label)
	}
	rows, err := a.clusterScopedRows(ctx, false, suffix)
	if err != nil {
		return nil, err
	}
	return a.pickScoped(ctx, label, rows, target, empty, names, detail)
}

// scopedCandidates lists rows to pick from. names lists the names that select
// a record; the first is shown.
func scopedCandidates(rows []map[string]any, names func(map[string]any) []string, detail func(map[string]any) string) []candidate {
	candidates := []candidate{}
	for _, row := range rows {
		id := text(row, "id")
		if id == "" {
			continue
		}
		all := []string{}
		for _, name := range names(row) {
			if name != "" && !slices.Contains(all, name) {
				all = append(all, name)
			}
		}
		if len(all) == 0 {
			all = []string{id}
		}
		candidates = append(candidates, candidate{ID: id, Name: all[0], Names: all[1:], Detail: detail(row), Record: row})
	}
	return candidates
}

// pickScoped selects one of rows, which clusterScopedRows read, by ID or exact name.
func (a *App) pickScoped(ctx context.Context, label string, rows []map[string]any, target, empty string, names func(map[string]any) []string, detail func(map[string]any) string) (*candidate, error) {
	candidates := scopedCandidates(rows, names, detail)
	c, err := a.pick(ctx, label, candidates, target)
	if err != nil && len(candidates) == 0 {
		if a.clusterName != "" {
			return nil, fmt.Errorf("no %ss in cluster %s; choose another with --cluster", label, a.clusterName)
		}
		return nil, errors.New(empty)
	}
	if err != nil {
		return nil, a.notFoundIn(err)
	}
	return c, nil
}

// clusterItemPath is /api/clusters/:cluster/<collection>/:id for a resolved record.
func clusterItemPath(c *candidate, collection string) (string, error) {
	cluster, err := safeID(text(c.Record, "cluster_id"))
	if err != nil {
		return "", err
	}
	id, err := safeID(c.ID)
	if err != nil {
		return "", err
	}
	return "/api/clusters/" + cluster + "/" + collection + "/" + id, nil
}
