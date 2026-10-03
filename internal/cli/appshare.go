package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// The public repository community packages are offered to, as a pull request.
const communityRepository = "edkadigital/apps"

// runTool runs a program on this machine and returns what it printed. Tests
// replace it.
var runTool = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.Bytes(), errors.New(message)
	}
	return stdout.Bytes(), nil
}

// githubAPI calls the GitHub API as the account `gh` is signed in to. The CLI
// holds no GitHub credential of its own.
func githubAPI(ctx context.Context, method, endpoint string, body any) (map[string]any, error) {
	args := []string{"api", "--method", method, endpoint}
	var stdin []byte
	if body != nil {
		stdin = jsonBody(body)
		args = append(args, "--input", "-")
	}
	out, err := runTool(ctx, stdin, "gh", args...)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if len(bytes.TrimSpace(out)) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("GitHub answered %s %s with something that is not an object", method, endpoint)
	}
	return result, nil
}

// githubStatus says whether an error of githubAPI carries this HTTP status.
func githubStatus(err error, status int) bool {
	return err != nil && strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status))
}

// sharedPackage is what the pull request says about a package, read from its template.
type sharedPackage struct {
	Name        string `yaml:"name"`
	Slug        string `yaml:"slug"`
	Version     string `yaml:"version"`
	AppVersion  string `yaml:"app_version"`
	Description string `yaml:"description"`
	Upstream    struct {
		Repository string `yaml:"repository"`
		License    string `yaml:"license"`
	} `yaml:"upstream"`
	Maintainers []struct {
		Github string `yaml:"github"`
	} `yaml:"maintainers"`
}

func readSharedPackage(files map[string]any) (sharedPackage, error) {
	var pkg sharedPackage
	template, _ := files["template.yaml"].(string)
	if err := yaml.Unmarshal([]byte(template), &pkg); err != nil {
		return pkg, fmt.Errorf("template.yaml cannot be read: %w", err)
	}
	if pkg.Slug == "" || pkg.Version == "" {
		return pkg, fmt.Errorf("template.yaml names no slug and version")
	}
	return pkg, nil
}

// unpackPackage reads the files of a published version from its archive: a
// gzipped tar with every file under a directory named after the slug. Only
// plain files are taken, and nothing is written to disk.
func unpackPackage(archive []byte, slug string) (map[string]any, error) {
	zipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("Edka returned files this command cannot read")
	}
	reader := tar.NewReader(io.LimitReader(zipped, 4*maxPackageBytes))
	files := map[string]any{}
	total := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("Edka returned files this command cannot read")
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name, found := strings.CutPrefix(path.Clean(header.Name), slug+"/")
		if !found || name == "" || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("Edka returned a file outside the package: %q", header.Name)
		}
		if len(files) == maxPackageFiles {
			return nil, fmt.Errorf("the published version holds more than %d files", maxPackageFiles)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("Edka returned files this command cannot read")
		}
		total += len(data)
		if total > maxPackageBytes {
			return nil, fmt.Errorf("the published version holds more than 1 MB")
		}
		if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
			files[name] = string(data)
		} else {
			files[name] = map[string]string{"base64": base64.StdEncoding.EncodeToString(data)}
		}
	}
	if _, ok := files["template.yaml"]; !ok {
		return nil, fmt.Errorf("the published version holds no template.yaml")
	}
	return files, nil
}

// sharedFiles returns the package to share: a directory on this machine, or
// the latest published version of a custom app of the organization.
func (a *App) sharedFiles(ctx context.Context, target string) (map[string]any, error) {
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return readPackageDir(target)
	}
	slug := url.PathEscape(target)
	response, err := a.request(ctx, "GET", "/api/custom-apps/"+slug, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("%s is neither a directory nor a published custom app: %w", target, err)
	}
	detail, err := identityData(response.Body)
	if err != nil {
		return nil, err
	}
	version := ""
	versions, _ := detail["versions"].([]any)
	for _, item := range versions {
		if entry, ok := item.(map[string]any); ok {
			if current, _ := entry["current"].(bool); current {
				version = text(entry, "version")
			}
		}
	}
	if version == "" {
		return nil, fmt.Errorf("%s has no published version", target)
	}
	archive, err := a.request(ctx, "GET", "/api/custom-apps/"+slug+"/versions/"+url.PathEscape(version)+"/archive", nil, nil)
	if err != nil {
		return nil, err
	}
	return unpackPackage(archive.Body, target)
}

func fileBytes(content any) []byte {
	switch value := content.(type) {
	case string:
		return []byte(value)
	case map[string]string:
		data, _ := base64.StdEncoding.DecodeString(value["base64"])
		return data
	case map[string]any:
		encoded, _ := value["base64"].(string)
		data, _ := base64.StdEncoding.DecodeString(encoded)
		return data
	}
	return nil
}

// capabilityLines describes what the validator read from the package, for the
// pull request.
func capabilityLines(capabilities map[string]any) []string {
	var lines []string
	add := func(label string, values []string) {
		if len(values) > 0 {
			lines = append(lines, fmt.Sprintf("- %s: %s", label, strings.Join(values, ", ")))
		}
	}
	items := func(key string) []map[string]any {
		var out []map[string]any
		list, _ := capabilities[key].([]any)
		for _, item := range list {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	}
	var images, objects, endpoints, storage, databases []string
	for _, image := range items("images") {
		reference := text(image, "repository")
		if tag := text(image, "tag"); tag != "" {
			reference += ":" + tag
		}
		if digest := text(image, "digest"); digest != "" {
			reference += "@" + digest
		}
		images = append(images, "`"+reference+"`")
	}
	for _, object := range items("objects") {
		kind := text(object, "kind")
		if count := number(object["count"]); count > 1 {
			kind = fmt.Sprintf("%s × %d", kind, count)
		}
		objects = append(objects, kind)
	}
	for _, endpoint := range items("endpoints") {
		endpoints = append(endpoints, text(endpoint, "name"))
	}
	for _, volume := range items("storage") {
		entry := text(volume, "field")
		if size := text(volume, "default"); size != "" {
			entry += " (" + size + ")"
		}
		storage = append(storage, entry)
	}
	list, _ := capabilities["databases"].([]any)
	for _, database := range list {
		if name, ok := database.(string); ok {
			databases = append(databases, name)
		}
	}
	add("Images", images)
	add("Objects", objects)
	add("Endpoints", endpoints)
	add("Storage", storage)
	add("Databases", databases)
	return lines
}

func pullRequestBody(pkg sharedPackage, capabilities map[string]any) string {
	var body strings.Builder
	fmt.Fprintf(&body, "## %s %s\n\n", pkg.Name, pkg.Version)
	if pkg.Description != "" {
		fmt.Fprintf(&body, "%s\n\n", pkg.Description)
	}
	body.WriteString("| | |\n|---|---|\n")
	if pkg.Upstream.Repository != "" {
		fmt.Fprintf(&body, "| Upstream | %s |\n", pkg.Upstream.Repository)
	}
	if pkg.Upstream.License != "" {
		fmt.Fprintf(&body, "| License | %s |\n", pkg.Upstream.License)
	}
	if pkg.AppVersion != "" {
		fmt.Fprintf(&body, "| App version | %s |\n", pkg.AppVersion)
	}
	var handles []string
	for _, maintainer := range pkg.Maintainers {
		if maintainer.Github != "" {
			handles = append(handles, "@"+maintainer.Github)
		}
	}
	if len(handles) > 0 {
		fmt.Fprintf(&body, "| Maintainers | %s |\n", strings.Join(handles, ", "))
	}
	if lines := capabilityLines(capabilities); len(lines) > 0 {
		body.WriteString("\n### What it runs and creates\n\n")
		body.WriteString(strings.Join(lines, "\n"))
		body.WriteString("\n")
	}
	body.WriteString("\n### Checks\n\n")
	body.WriteString("- [x] `edka apps validate --community` reports no findings\n")
	fmt.Fprintf(&body, "- [ ] `checks/chart.sh apps/%s` passes\n", pkg.Slug)
	fmt.Fprintf(&body, "- [ ] `checks/smoke.sh apps/%s <context>` passes on a fresh cluster\n", pkg.Slug)
	body.WriteString("- [ ] The README names the source of each fact about the app\n")
	body.WriteString("\nOpened with the Edka CLI.\n")
	return body.String()
}

// forkReadyWait is how long a share waits for GitHub to finish creating a fork.
var forkReadyWait = 2 * time.Minute

// waitForFork returns once the fork holds its default branch. GitHub creates a
// fork in the background and answers before it is done. Until then the fork is
// not found, or is a repository without commits, and takes no writes. waiting
// is called once, when the fork is not there on the first read.
func waitForFork(parent context.Context, forkName, branch string, waiting func()) error {
	ctx, cancel := context.WithTimeout(parent, forkReadyWait)
	defer cancel()
	told := false
	for {
		_, err := githubAPI(ctx, "GET", "repos/"+forkName+"/git/ref/heads/"+branch, nil)
		if err == nil {
			return nil
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		if ctx.Err() == nil && !githubStatus(err, 404) && !githubStatus(err, 409) {
			return fmt.Errorf("cannot read the fork %s: %w", forkName, err)
		}
		if !told {
			waiting()
			told = true
		}
		if pause(ctx, pollInterval) != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			return fmt.Errorf("GitHub has not finished creating the fork %s; run the command again in a minute", forkName)
		}
	}
}

// openPullRequest writes the package to a branch of the user's fork and opens
// a pull request against the community repository. Everything goes through
// the GitHub API as the account `gh` is signed in to: no clone, no push.
// waiting is called with the name of the fork when GitHub is still creating it.
func openPullRequest(ctx context.Context, pkg sharedPackage, files map[string]any, body string, waiting func(forkName string)) (pullRequestURL, branch string, err error) {
	// GitHub answers with the fork the account already has when there is one.
	fork, err := githubAPI(ctx, "POST", "repos/"+communityRepository+"/forks", map[string]any{"default_branch_only": true})
	if err != nil {
		return "", "", fmt.Errorf("cannot fork %s: %w", communityRepository, err)
	}
	forkName := text(fork, "full_name")
	owner, _ := fork["owner"].(map[string]any)
	if forkName == "" || text(owner, "login") == "" {
		return "", "", fmt.Errorf("GitHub named no fork of %s", communityRepository)
	}
	forkBranch := first(text(fork, "default_branch"), "main")
	if err := waitForFork(ctx, forkName, forkBranch, func() { waiting(forkName) }); err != nil {
		return "", "", err
	}
	// A fork that fell behind is brought up to date. A fork that cannot be is
	// not needed for it: the branch below starts from the upstream commit.
	_, _ = githubAPI(ctx, "POST", "repos/"+forkName+"/merge-upstream", map[string]any{"branch": forkBranch})

	head, err := githubAPI(ctx, "GET", "repos/"+communityRepository+"/git/ref/heads/main", nil)
	if err != nil {
		return "", "", fmt.Errorf("cannot read %s: %w", communityRepository, err)
	}
	object, _ := head["object"].(map[string]any)
	baseCommit := text(object, "sha")
	commit, err := githubAPI(ctx, "GET", "repos/"+communityRepository+"/git/commits/"+baseCommit, nil)
	if err != nil {
		return "", "", err
	}
	tree, _ := commit["tree"].(map[string]any)
	baseTree := text(tree, "sha")
	if baseCommit == "" || baseTree == "" {
		return "", "", fmt.Errorf("cannot read the main branch of %s", communityRepository)
	}

	directory := "apps/" + pkg.Slug
	_, err = githubAPI(ctx, "GET", "repos/"+communityRepository+"/contents/"+directory+"/template.yaml?ref="+baseCommit, nil)
	exists := err == nil
	if err != nil && !githubStatus(err, 404) {
		return "", "", err
	}

	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	entries := make([]map[string]any, 0, len(paths))
	for _, name := range paths {
		blob, err := githubAPI(ctx, "POST", "repos/"+forkName+"/git/blobs", map[string]any{
			"content":  base64.StdEncoding.EncodeToString(fileBytes(files[name])),
			"encoding": "base64",
		})
		if err != nil {
			return "", "", fmt.Errorf("cannot upload %s: %w", name, err)
		}
		entries = append(entries, map[string]any{"path": name, "mode": "100644", "type": "blob", "sha": text(blob, "sha")})
	}
	// The directory of the package as a tree of its own, so files that left
	// the package leave the repository too.
	packageTree, err := githubAPI(ctx, "POST", "repos/"+forkName+"/git/trees", map[string]any{"tree": entries})
	if err != nil {
		return "", "", err
	}
	rootTree, err := githubAPI(ctx, "POST", "repos/"+forkName+"/git/trees", map[string]any{
		"base_tree": baseTree,
		"tree":      []map[string]any{{"path": directory, "mode": "040000", "type": "tree", "sha": text(packageTree, "sha")}},
	})
	if err != nil {
		return "", "", err
	}

	title := fmt.Sprintf("Add %s %s", pkg.Name, pkg.Version)
	if exists {
		title = fmt.Sprintf("Update %s to %s", pkg.Name, pkg.Version)
	}
	created, err := githubAPI(ctx, "POST", "repos/"+forkName+"/git/commits", map[string]any{
		"message": title,
		"tree":    text(rootTree, "sha"),
		"parents": []string{baseCommit},
	})
	if err != nil {
		return "", "", err
	}

	branch = pkg.Slug + "-" + pkg.Version
	_, err = githubAPI(ctx, "POST", "repos/"+forkName+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": text(created, "sha")})
	if githubStatus(err, 422) {
		// The branch exists from an earlier share of this version. It moves to the new commit.
		_, err = githubAPI(ctx, "PATCH", "repos/"+forkName+"/git/refs/heads/"+branch, map[string]any{"sha": text(created, "sha"), "force": true})
	}
	if err != nil {
		return "", "", fmt.Errorf("cannot write the branch %s: %w", branch, err)
	}

	headRef := text(owner, "login") + ":" + branch
	pull, err := githubAPI(ctx, "POST", "repos/"+communityRepository+"/pulls", map[string]any{
		"title": title, "head": headRef, "base": "main", "body": body, "maintainer_can_modify": true,
	})
	if githubStatus(err, 422) {
		// A pull request for this branch is open. The branch moved, so it shows the new files.
		out, listErr := runTool(ctx, nil, "gh", "api", "--method", "GET", "repos/"+communityRepository+"/pulls?state=open&head="+url.QueryEscape(headRef), "--jq", ".[0].html_url")
		if found := strings.TrimSpace(string(out)); listErr == nil && found != "" {
			return found, branch, nil
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("cannot open the pull request: %w", err)
	}
	return text(pull, "html_url"), branch, nil
}

func (a *App) shareCommand() *cobra.Command {
	return &cobra.Command{Use: "share <directory or app>", Short: "Offer a custom app to the community catalog", Long: "Offer a custom app to the community catalog: check it against the rules\nof the catalog, then open a pull request to " + communityRepository + " from your\nGitHub account. Name a package directory, or a custom app your organization\npublished.\n\nThe pull request is opened with the GitHub CLI, as the account `gh` is signed\nin to. That account is one of the maintainers template.yaml lists. Edka\nmaintainers review the pull request, and a merged app is listed with a\nfollowing Edka release.\n\nThe command asks for confirmation before it opens anything; --yes skips it.", Args: cobra.ExactArgs(1), Example: "  edka apps share ./memos\n  edka apps share memos --yes", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		files, err := a.sharedFiles(ctx, args[0])
		if err != nil {
			return err
		}
		pkg, err := readSharedPackage(files)
		if err != nil {
			return err
		}
		result, _, err := a.checkPackage(ctx, files, "community")
		if err != nil {
			return err
		}
		if findings, errorCount := packageFindings(result); errorCount > 0 {
			if a.output == "table" {
				a.printFindings(findings)
			}
			return fmt.Errorf("%s %s has %d %s to fix before it can be shared", pkg.Slug, pkg.Version, errorCount, plural(errorCount, "error", "errors"))
		}

		out, err := runTool(ctx, nil, "gh", "api", "user", "--jq", ".login")
		login := strings.TrimSpace(string(out))
		if err != nil || login == "" {
			return fmt.Errorf("the pull request is opened with the GitHub CLI; install it from https://cli.github.com and run `gh auth login`")
		}
		listed := false
		for _, maintainer := range pkg.Maintainers {
			listed = listed || strings.EqualFold(maintainer.Github, login)
		}
		if !listed {
			return fmt.Errorf("%s is not under maintainers in template.yaml; a package is shared by one of its maintainers.\nAdd `- github: %q` under maintainers, raise the version and publish again", login, login)
		}

		if err := a.confirm(fmt.Sprintf("Open a pull request to %s with %s %s, from the GitHub account %s", communityRepository, pkg.Slug, pkg.Version, login)); err != nil {
			return err
		}
		capabilities, _ := result["capabilities"].(map[string]any)
		pullRequestURL, branch, err := openPullRequest(ctx, pkg, files, pullRequestBody(pkg, capabilities), func(forkName string) {
			a.message("Waiting for GitHub to finish creating the fork %s", ui.Clean(forkName))
		})
		if err != nil {
			return err
		}

		// The custom app page of the organization links the pull request. A
		// package that was shared from a directory may not be published there.
		recorded := false
		if strings.HasPrefix(pullRequestURL, "https://github.com/"+communityRepository+"/pull/") {
			_, recordErr := a.request(ctx, "POST", "/api/custom-apps/"+url.PathEscape(pkg.Slug)+"/versions/"+url.PathEscape(pkg.Version)+"/share", nil, jsonBody(map[string]any{"pull_request_url": pullRequestURL}))
			recorded = recordErr == nil
		}

		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": map[string]any{"pull_request_url": pullRequestURL, "repository": communityRepository, "branch": branch, "slug": pkg.Slug, "version": pkg.Version, "recorded": recorded}}), a.output, false)
		}
		a.message("✓ Opened %s", ui.Clean(pullRequestURL))
		a.message("Edka maintainers review it. Its checks run on the pull request.")
		return nil
	}}
}
