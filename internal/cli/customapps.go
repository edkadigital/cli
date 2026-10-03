package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// The package of a custom app is a directory: template.yaml, and optionally a chart
// directory, README.md, an icon and test/answers.yaml. Edka takes it as a map
// of each file's path to its content, and checks it. The limits here only stop
// a wrong directory from being read whole before Edka says so.
const (
	maxPackageFiles = 200
	maxPackageBytes = 1 << 20
)

// Files an operating system or an editor leaves in a directory.
var ignoredPackageNames = map[string]bool{".DS_Store": true, "Thumbs.db": true, "desktop.ini": true}
var ignoredPackageDirs = map[string]bool{".git": true, "node_modules": true, "__MACOSX": true}

// readPackageDir reads the files of a package directory. A text file goes as a
// string and anything else as base64. Links are not followed, so nothing
// outside the directory is read.
func readPackageDir(dir string) (map[string]any, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory; name the directory that holds template.yaml", dir)
	}
	files := map[string]any{}
	total := 0
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != dir && ignoredPackageDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || ignoredPackageNames[name] || strings.HasPrefix(name, "._") {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if len(files) == maxPackageFiles {
			return fmt.Errorf("%s holds more than %d files; a package holds at most %d", dir, maxPackageFiles, maxPackageFiles)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		data, err := readAtMost(file, maxPackageBytes-total)
		_ = file.Close()
		if errors.Is(err, errOverLimit) {
			return fmt.Errorf("%s holds more than 1 MB; a package holds at most 1 MB", dir)
		}
		if err != nil {
			return err
		}
		total += len(data)
		key := filepath.ToSlash(relative)
		if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
			files[key] = string(data)
		} else {
			files[key] = map[string]string{"base64": base64.StdEncoding.EncodeToString(data)}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := files["template.yaml"]; !ok {
		return nil, fmt.Errorf("%s holds no template.yaml; name the directory of the package", dir)
	}
	return files, nil
}

var errOverLimit = errors.New("over the limit")

// readAtMost reads a file that may hold limit bytes. It stops one byte past
// the limit, so a large file in the wrong directory is not loaded to be
// refused.
func readAtMost(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errOverLimit
	}
	return data, nil
}

// writePackageFiles writes the files Edka returned for a new package. A path
// that would leave the directory is refused, whatever the API answered.
func writePackageFiles(dir string, files map[string]any) error {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		content, ok := files[path].(string)
		clean := filepath.Clean(filepath.FromSlash(path))
		if !ok || path == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("Edka returned a file this command cannot write: %q", path)
		}
		target := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func packageDir(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}

// packageFindings are the errors and warnings of a validation.
func packageFindings(result map[string]any) (findings []map[string]any, errorCount int) {
	list, _ := result["diagnostics"].([]any)
	for _, item := range list {
		finding, ok := item.(map[string]any)
		if !ok {
			continue
		}
		findings = append(findings, finding)
		if text(finding, "severity") == "error" {
			errorCount++
		}
	}
	return findings, errorCount
}

// checkPackage sends a package to the validator and returns its result.
func (a *App) checkPackage(ctx context.Context, files map[string]any, profile string) (map[string]any, []byte, error) {
	response, err := a.request(ctx, "POST", "/api/custom-apps/validate", nil, jsonBody(map[string]any{"files": files, "profile": profile}))
	if err != nil {
		return nil, nil, err
	}
	result, err := identityData(response.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid validation response")
	}
	return result, response.Body, nil
}

// printFindings writes each finding with where it is and how to fix it.
func (a *App) printFindings(findings []map[string]any) {
	for _, finding := range findings {
		mark := "✗"
		if text(finding, "severity") != "error" {
			mark = "!"
		}
		location := text(finding, "file")
		if line := number(finding["line"]); location != "" && line > 0 {
			location = fmt.Sprintf("%s:%d", location, line)
		}
		fmt.Fprintf(a.Out, "%s %s\n", mark, ui.Clean(strings.TrimSpace(location+"  "+text(finding, "code"))))
		fmt.Fprintf(a.Out, "  %s\n", ui.Clean(text(finding, "message")))
		if fix := text(finding, "fix"); fix != "" {
			fmt.Fprintf(a.Out, "  Fix: %s\n", ui.Clean(fix))
		}
	}
}

// versionChanges asks what moving an app to a version of its package changes:
// images, kinds of objects, endpoints, storage, databases, secrets, add-ons and
// the settings of the app.
func (a *App) versionChanges(ctx context.Context, appPath, version string) ([]map[string]any, error) {
	response, err := a.request(ctx, "GET", appPath+"/versions/"+url.PathEscape(version), nil, nil)
	if err != nil {
		return nil, err
	}
	preview, err := identityData(response.Body)
	if err != nil {
		return nil, fmt.Errorf("invalid version response")
	}
	list, _ := preview["changes"].([]any)
	changes := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if change, ok := item.(map[string]any); ok {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

// printVersionChanges writes what a version changes before the update is
// confirmed. A change that reaches beyond the namespace of the app or asks for
// input is marked, and so is a setting that keeps its value while its default
// changed.
func (a *App) printVersionChanges(installed, next string, changes []map[string]any) {
	if len(changes) == 0 {
		a.message("Version %s runs and creates the same things as %s.", ui.Clean(next), ui.Clean(installed))
		return
	}
	a.message("Version %s changes:", ui.Clean(next))
	for _, change := range changes {
		mark := "-"
		if text(change, "tone") == "warning" {
			mark = "!"
		}
		a.message("  %s %s", mark, ui.Clean(text(change, "text")))
	}
}

// packageIdentity reads the name and version a package states, for messages.
func packageIdentity(files map[string]any) string {
	template, _ := files["template.yaml"].(string)
	slug, version := "", ""
	for _, line := range strings.Split(template, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "slug":
			slug = value
		case "version":
			version = value
		}
	}
	return strings.TrimSpace(first(slug, "the package") + " " + version)
}

// appStatusLines describes each state of an app whose install or change is
// still running.
var appStatusLines = map[string]string{
	"installing_dependencies": "Installing dependencies…",
	"configuring":             "Preparing the configuration…",
	"deploying":               "Deploying…",
	"installing":              "Installing…",
}

// waitApp prints an app operation's progress to stderr until the app is
// installed or the operation fails, and returns the app as it was last read.
// An install and an update wait the same way.
func (a *App) waitApp(parent context.Context, path, name string, timeout time.Duration) (*api.Response, map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	p := a.startProgress()
	shown, command, status := ui.Clean(name), shellJoin([]string{name}), ""
	unfinished := func(err error) error {
		if ctx.Err() == nil {
			return err
		}
		state := "has not finished"
		if status != "" {
			state = "is still " + ui.Clean(status)
		}
		return fmt.Errorf("%s %s: %w; the operation continues, follow it with `edka apps get %s`", shown, state, ctx.Err(), command)
	}
	for {
		response, err := a.poll(ctx, p, path, nil)
		if err != nil {
			return nil, nil, unfinished(err)
		}
		app, err := identityData(response.Body)
		if err != nil {
			return nil, nil, err
		}
		status = text(app, "status")
		progress, _ := app["progress"].(map[string]any)
		message := text(progress, "message")
		switch status {
		case "installed":
			return response, app, nil
		case "failed", "error":
			if message != "" {
				message = ": " + ui.Clean(message)
			}
			return nil, nil, fmt.Errorf("%s failed%s\nFind the cause with `edka apps diagnose %s`", shown, message, command)
		}
		p.say("status", first(appStatusLines[status], status))
		p.say("message", message)
		if err := pause(ctx, pollInterval); err != nil {
			return nil, nil, unfinished(err)
		}
	}
}

// customAppCommands are the commands that write, check, publish and update
// custom apps. They sit under `edka apps`.
func (a *App) customAppCommands() []*cobra.Command {
	var name, description, image, tag, dataPath, target string
	var port int
	var with []string
	initCmd := &cobra.Command{Use: "init <slug>", Short: "Write the starting files of a new custom app", Long: "Write the starting files of a custom app: a template with the standard\nsettings tabs and a small Helm chart. The files pass `edka apps validate` as\nthey are. Change what the app needs, then validate and publish.\n\n--with adds parts to the app: access for a hostname on the gateway,\nstorage for a data volume, postgres for a PostgreSQL database.", Args: cobra.ExactArgs(1), Example: "  edka apps init memos --image ghcr.io/usememos/memos --tag 0.25.1 --port 5230 --with access,storage\n  edka apps init my-api --image ghcr.io/acme/api --port 8080 --with access,postgres --dir packages/my-api", RunE: func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		dir := first(target, slug)
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			return fmt.Errorf("%s is not empty; choose another directory with --dir", dir)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		body := map[string]any{"slug": slug, "name": first(name, slug), "image": image}
		if len(with) > 0 {
			body["with"] = with
		}
		if description != "" {
			body["description"] = description
		}
		if tag != "" {
			body["tag"] = tag
		}
		if port != 0 {
			body["port"] = port
		}
		if dataPath != "" {
			body["data_path"] = dataPath
		}
		response, err := a.request(cmd.Context(), "POST", "/api/custom-apps/scaffold", nil, jsonBody(body))
		if err != nil {
			return err
		}
		data, err := identityData(response.Body)
		if err != nil {
			return err
		}
		files, ok := data["files"].(map[string]any)
		if !ok || len(files) == 0 {
			return fmt.Errorf("Edka returned no files for the package")
		}
		if err := writePackageFiles(dir, files); err != nil {
			return err
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": map[string]any{"directory": dir, "files": len(files)}}), a.output, false)
		}
		a.message("✓ Wrote %d files to %s", len(files), dir)
		a.message("Next: edit %s, then run `edka apps validate %s`", filepath.Join(dir, "template.yaml"), dir)
		return nil
	}}
	initCmd.Flags().StringVar(&image, "image", "", "Image repository without a tag, such as ghcr.io/usememos/memos")
	initCmd.Flags().StringVar(&tag, "tag", "", "Image tag to start from")
	initCmd.Flags().IntVar(&port, "port", 0, "Port the container listens on")
	initCmd.Flags().StringVar(&name, "name", "", "Name shown in the catalog; defaults to the slug")
	initCmd.Flags().StringVar(&description, "description", "", "One sentence about the app")
	initCmd.Flags().StringVar(&dataPath, "data-path", "", "Where the app keeps its data in the container, with --with storage")
	initCmd.Flags().StringSliceVar(&with, "with", nil, "Parts to add: access, storage, postgres")
	initCmd.Flags().StringVar(&target, "dir", "", "Directory to write; defaults to the slug")
	_ = initCmd.MarkFlagRequired("image")

	var community bool
	validate := &cobra.Command{Use: "validate [directory]", Short: "Check the files of a custom app", Long: "Check the files of a custom app against the rules Edka installs by. Each finding names\nthe file, the line and a fix. Nothing is stored.\n\n--community also applies the rules for listing in the public catalog.", Args: cobra.MaximumNArgs(1), Example: "  edka apps validate ./memos\n  edka apps validate ./memos --community\n  edka apps validate --json", RunE: func(cmd *cobra.Command, args []string) error {
		dir := packageDir(args)
		files, err := readPackageDir(dir)
		if err != nil {
			return err
		}
		profile := "organization"
		if community {
			profile = "community"
		}
		result, body, err := a.checkPackage(cmd.Context(), files, profile)
		if err != nil {
			return err
		}
		findings, errorCount := packageFindings(result)
		if a.output != "table" {
			if err := ui.Render(a.Out, body, a.output, false); err != nil {
				return err
			}
		} else {
			a.printFindings(findings)
		}
		if errorCount > 0 {
			return fmt.Errorf("%s has %d %s to fix", packageIdentity(files), errorCount, plural(errorCount, "error", "errors"))
		}
		if a.output == "table" {
			a.message("✓ %s is valid (%d files)", packageIdentity(files), len(files))
		}
		return nil
	}}
	validate.Flags().BoolVar(&community, "community", false, "Also apply the rules for the public catalog")

	publish := &cobra.Command{Use: "publish [directory]", Short: "Publish a custom app to your organization's catalog", Long: "Publish a custom app, or a new version of one, to the catalog of your\norganization. The files are checked first and stored only when they are valid.\nA new version carries a higher version in template.yaml and chart/Chart.yaml.\n\nPublishing changes no installed app. Each one stays on its version until\n`edka apps update <app>` moves it. Needs the admin role.", Args: cobra.MaximumNArgs(1), Example: "  edka apps publish ./memos\n  edka apps publish ./memos --json", RunE: func(cmd *cobra.Command, args []string) error {
		dir := packageDir(args)
		files, err := readPackageDir(dir)
		if err != nil {
			return err
		}
		result, _, err := a.checkPackage(cmd.Context(), files, "organization")
		if err != nil {
			return err
		}
		if findings, errorCount := packageFindings(result); errorCount > 0 {
			if a.output == "table" {
				a.printFindings(findings)
			}
			return fmt.Errorf("%s has %d %s to fix; nothing was published", packageIdentity(files), errorCount, plural(errorCount, "error", "errors"))
		}
		response, err := a.request(cmd.Context(), "POST", "/api/custom-apps", nil, jsonBody(map[string]any{"files": files, "via": "cli"}))
		if err != nil {
			return err
		}
		if a.output != "table" {
			return a.render(response)
		}
		published, err := identityData(response.Body)
		if err != nil {
			return err
		}
		pkg, _ := published["app"].(map[string]any)
		version, _ := published["version"].(map[string]any)
		slug, number := text(pkg, "slug"), text(version, "version")
		if unchanged, _ := published["unchanged"].(bool); unchanged {
			a.message("✓ %s %s is already published with these files", slug, number)
			return nil
		}
		a.message("✓ Published %s %s", slug, number)
		a.message("It is in the catalog of every cluster in your organization.")
		return nil
	}}

	custom := &cobra.Command{Use: "custom", Short: "List the custom apps your organization published", Args: cobra.NoArgs, Example: "  edka apps custom\n  edka apps custom --json", RunE: func(cmd *cobra.Command, _ []string) error {
		rows, err := a.objects(cmd.Context(), "/api/custom-apps")
		if err != nil {
			return err
		}
		return a.renderRows(rows, []ui.Column{ui.Field("NAME", "name"), ui.Field("SLUG", "slug"), ui.Field("VERSION", "version"), ui.Field("APP VERSION", "app_version"), {Header: "INSTANCES", Value: func(m map[string]any) string { return fmt.Sprint(number(m["instances"])) }}, {Header: "PUBLISHED", Value: func(m map[string]any) string { return when(m["published_at"]) }}})
	}}

	var toVersion string
	var wait bool
	var waitTimeout time.Duration
	update := &cobra.Command{Use: "update <app>", Aliases: []string{"upgrade"}, Short: "Move an installed custom or community app to another version", Long: "Move an installed app to another version: the latest one, or the one\n--version names. A setting you changed keeps its value, the other\nsettings take the defaults of the new version, and the stored secrets stay.\n\nThe command lists what the update changes, such as images, kinds of objects,\nendpoints, storage, secrets and settings, then asks for confirmation; --yes\nskips the question. A line marked \"!\" reaches beyond the namespace of the\napp, asks for input, or is a setting that keeps its value while its default\nchanged. With --wait, progress goes to stderr until the update is installed.", Args: cobra.ExactArgs(1), Example: "  edka apps update memos --wait\n  edka apps update memos --version 1.3.0 --cluster production --yes", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		c, err := a.resolveApp(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		path, err := clusterItemPath(c, "apps")
		if err != nil {
			return err
		}
		response, err := a.request(cmd.Context(), "GET", path, nil, nil)
		if err != nil {
			return err
		}
		app, err := identityData(response.Body)
		if err != nil {
			return err
		}
		catalogApp, _ := app["catalog_app"].(map[string]any)
		installed := text(catalogApp, "version")
		next := first(toVersion, text(catalogApp, "latest_version"))
		if text(catalogApp, "resolved_version_id") == "" {
			return fmt.Errorf("%s is not a custom or community app; its version is a setting, change it in the console", c.Name)
		}
		if next == "" || next == installed {
			if a.output != "table" {
				return ui.Render(a.Out, jsonBody(map[string]any{"data": map[string]any{"app_id": c.ID, "version": installed, "updated": false}}), a.output, false)
			}
			a.message("✓ %s already runs %s", c.Name, installed)
			return nil
		}
		changes, err := a.versionChanges(cmd.Context(), path, next)
		if err != nil {
			return err
		}
		a.printVersionChanges(installed, next, changes)
		if err := a.confirm(fmt.Sprintf("Update %s on cluster %s from %s to %s", c.Name, text(c.Record, "cluster_name"), installed, next)); err != nil {
			return err
		}
		response, err = a.request(cmd.Context(), "PATCH", path, nil, jsonBody(map[string]any{"version": next}))
		if err != nil {
			return err
		}
		if !wait {
			if a.output != "table" {
				return a.render(response)
			}
			a.message("✓ Updating %s to %s; follow it with `edka apps get %s`", c.Name, next, c.Name)
			return nil
		}
		if _, _, err := a.waitApp(cmd.Context(), path, c.Name, waitTimeout); err != nil {
			return err
		}
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"data": map[string]any{"app_id": c.ID, "version": next, "updated": true, "changes": changes}}), a.output, false)
		}
		a.message("✓ %s runs %s", c.Name, next)
		return nil
	}}
	update.Flags().StringVar(&toVersion, "version", "", "Version to move to; defaults to the latest")
	update.Flags().BoolVar(&wait, "wait", false, "Wait until the update is installed")
	update.Flags().DurationVar(&waitTimeout, "wait-timeout", 15*time.Minute, "Maximum wait for the update")

	share := a.shareCommand()
	a.completes(a.appChoices(), update)
	for _, c := range []*cobra.Command{validate, publish, share} {
		c.ValidArgsFunction = directories
	}
	return []*cobra.Command{initCmd, validate, publish, custom, update, share}
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
