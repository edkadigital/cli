// Package cli wires human workflows and the complete delegated API catalog.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/auth"
	"github.com/edkadigital/cli/internal/catalog"
	"github.com/edkadigital/cli/internal/config"
	"github.com/edkadigital/cli/internal/credential"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

type App struct {
	In               io.Reader
	Out              io.Writer
	Err              io.Writer
	HTTP             *http.Client
	Version          string
	dir              string
	cfg              *config.Config
	profile          string
	current          config.Profile
	link             *config.Link
	linkPath         string
	profileFlag      string
	apiFlag          string
	clusterFlag      string
	deploymentFlag   string
	organizationFlag string
	storeMode        string
	colorMode        string
	output           string
	json             bool
	yes              bool
	noInput          bool
	timeout          time.Duration
	color            bool
	cluster          string
	deployment       string
	organization     string
	clusterName      string
	clusters         []map[string]any
	apiCmd           *cobra.Command
	catalog          *catalog.Catalog
	// polling turns off the request animation while a wait prints progress or
	// --debug prints requests.
	polling bool
	debug   bool
	// lines reads typed answers. One reader serves every prompt, so it never
	// drops an answer it read ahead.
	lines *bufio.Reader
	// flagCompletions register the completions of flag values.
	flagCompletions []func()
	upgradeCmd      *cobra.Command
	// updateCheck is the lookup of a newer release this command started.
	updateCheck *updateCheck
	// verifying is set while a passkey check runs, so its own requests never
	// start another.
	verifying bool
}

func New(version string, in io.Reader, out, errOut io.Writer) *cobra.Command {
	a := &App{In: in, Out: out, Err: errOut, Version: version}
	root := &cobra.Command{Use: "edka", Short: "Your infrastructure. One terminal.", Long: "Edka CLI — manage clusters, ship applications, and inspect infrastructure.\n\nStart with `edka login`, then `edka link` inside your project.\nEvery command supports --json for scripts and --help for examples.", Version: version, SilenceErrors: true, SilenceUsage: true, CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true}}
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error { return a.initialize(cmd) }
	// A command that fails prints its error and no notice.
	root.PersistentPostRun = func(*cobra.Command, []string) { a.finishUpdateCheck() }
	f := root.PersistentFlags()
	f.StringVar(&a.profileFlag, "profile", "", "Saved profile (EDKA_PROFILE)")
	f.StringVar(&a.apiFlag, "api-url", "", "API origin (EDKA_API_URL)")
	f.StringVar(&a.clusterFlag, "cluster", "", "Cluster ID or name (EDKA_CLUSTER)")
	f.StringVar(&a.deploymentFlag, "deployment", "", "Deployment ID or name (EDKA_DEPLOYMENT)")
	f.StringVar(&a.organizationFlag, "organization", "", "Authorized organization ID (EDKA_ORGANIZATION)")
	f.StringVar(&a.storeMode, "credential-store", "auto", "Credential storage: auto, keyring, file (EDKA_CREDENTIAL_STORE)")
	f.StringVar(&a.output, "output", "table", "Output format: table, json, raw")
	f.BoolVar(&a.json, "json", false, "Print machine-readable JSON")
	f.BoolVarP(&a.yes, "yes", "y", false, "Confirm destructive operations")
	f.BoolVar(&a.noInput, "no-input", false, "Never prompt (EDKA_NO_INPUT; also enabled in CI)")
	f.DurationVar(&a.timeout, "timeout", 30*time.Second, "HTTP request timeout")
	f.StringVar(&a.colorMode, "color", "auto", "Terminal color: auto, always, never")
	f.BoolVar(&a.debug, "debug", false, "Print each request to stderr (EDKA_DEBUG)")
	a.completesFlag(root, "profile", a.profileNames)
	a.completesFlag(root, "cluster", a.offers(a.clusterChoices))
	a.completesFlag(root, "deployment", a.offers(a.deploymentChoices))
	a.completesFlag(root, "credential-store", values("auto", "keyring", "file"))
	a.completesFlag(root, "output", values("table", "json", "raw"))
	a.completesFlag(root, "color", values("auto", "always", "never"))
	root.AddGroup(&cobra.Group{ID: "start", Title: "Start here:"}, &cobra.Group{ID: "work", Title: "Everyday workflows:"}, &cobra.Group{ID: "resources", Title: "Resources:"}, &cobra.Group{ID: "tools", Title: "Tools:"})
	a.addAuth(root)
	a.addContext(root)
	a.addWorkflows(root)
	a.addLifecycle(root)
	a.addDiagnose(root)
	a.addEnv(root)
	a.addApps(root)
	a.addAddons(root)
	a.addDeployments(root)
	a.addClusters(root)
	a.addNodePools(root)
	a.addDomains(root)
	a.addRegistries(root)
	a.addSecrets(root)
	a.addDatabases(root)
	a.addCronjobs(root)
	a.addPreviews(root)
	a.apiCmd = a.addTools(root)
	a.addUpgrade(root)
	c, err := catalog.Load()
	if err != nil {
		panic(err)
	}
	a.catalog = c
	a.addOperations(a.apiCmd, c)
	a.addEndpointHints(root)
	help := &cobra.Command{Use: "help [command]", Short: "Help for any command", GroupID: "tools", ValidArgsFunction: commandNames, RunE: func(cmd *cobra.Command, args []string) error {
		target, _, err := root.Find(args)
		if err != nil {
			return err
		}
		return target.Help()
	}}
	root.SetHelpCommand(help)
	shortenGlobalFlags(root)
	root.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	markStandalone(root, help)
	return root
}

// rareGlobalFlags are the global flags that only `edka --help` lists.
var rareGlobalFlags = []string{"api-url", "color", "credential-store", "debug", "organization", "timeout"}

// shortenGlobalFlags leaves rareGlobalFlags out of the help of every command
// below the root. The flags still work there.
func shortenGlobalFlags(root *cobra.Command) {
	help := root.HelpFunc()
	hide := func(hidden bool) {
		for _, name := range rareGlobalFlags {
			root.PersistentFlags().Lookup(name).Hidden = hidden
		}
	}
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd == root {
			help(cmd, args)
			return
		}
		hide(true)
		defer hide(false)
		help(cmd, args)
		fmt.Fprintln(cmd.OutOrStdout(), "\nUse `edka --help` for the other global flags.")
	})
}

// localAnnotation marks commands that work without a saved profile, so a stale
// profile reference cannot block the commands that repair it.
const localAnnotation = "edka:local"

// standaloneAnnotation marks commands that read no config, project link or
// credentials, so a corrupt file cannot block help, `version`, or the `unlink`
// that removes it.
const standaloneAnnotation = "edka:standalone"

// configAnnotation marks commands that read the config and nothing else, so a
// project link that can't be read does not block the `profile` commands.
const configAnnotation = "edka:config"

// strictGroup makes a command group reject unknown subcommands; otherwise
// Cobra prints help and exits successfully, hiding typos in scripts. Groups
// only print help or errors, so they are standalone, and they receive raw
// arguments so a hint can repeat a moved command with its flags.
func (a *App) strictGroup(commands ...*cobra.Command) {
	markStandalone(commands...)
	for _, c := range commands {
		c.Args = cobra.ArbitraryArgs
		c.DisableFlagParsing = true
		c.RunE = func(cmd *cobra.Command, args []string) error {
			words := 0
			for words < len(args) && !strings.HasPrefix(args[words], "-") {
				words++
			}
			if words == 0 {
				return cmd.Help()
			}
			return a.unknownCommand(cmd, args, words)
		}
	}
}

// unknownCommand explains args, whose first words are command names.
func (a *App) unknownCommand(cmd *cobra.Command, args []string, words int) error {
	message := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	// Endpoint commands generated from the API catalog live under `edka api`.
	endpoint, positional := "", -1
	if a.apiCmd != nil && !strings.HasPrefix(cmd.CommandPath(), a.apiCmd.CommandPath()) {
		prefix := strings.Fields(cmd.CommandPath())[1:]
		if target, rest, err := a.apiCmd.Find(slices.Concat(prefix, args[:words])); err == nil && !target.HasSubCommands() && len(rest) <= 1 {
			endpoint, positional = shellJoin(slices.Concat(prefix, args)), len(rest)
		}
	}
	// Resources nested in an API path, such as `clusters apps`, have their own
	// commands; `edka clusters apps list` means `edka apps list`. A trailing
	// argument carries over only when it is the endpoint's argument too.
	if resource, _, err := cmd.Root().Find(args[:1]); err == nil && resource != cmd && resource.GroupID == "resources" {
		suggestion := resource.Name() + " --help"
		if target, rest, err := cmd.Root().Find(args[:words]); err == nil && target != resource && !target.HasSubCommands() && (len(rest) == 0 || len(rest) == 1 && positional == 1) {
			suggestion = shellJoin(slices.Concat([]string{resource.Name()}, args[1:]))
		}
		message += fmt.Sprintf("\n%s have their own commands: edka %s", strings.ToUpper(resource.Name()[:1])+resource.Name()[1:], suggestion)
	}
	if endpoint != "" {
		return fmt.Errorf("%s\nAPI endpoint commands are under `edka api`: edka api %s", message, endpoint)
	}
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2 // Cobra's root default
	}
	if suggestions := cmd.SuggestionsFor(args[0]); len(suggestions) > 0 {
		message += "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t")
	}
	return errors.New(message)
}

// addEndpointHints registers hidden top-level names for endpoint groups that
// have no curated command, pointing `edka clusters …` to `edka api clusters …`.
func (a *App) addEndpointHints(root *cobra.Command) {
	taken := map[string]bool{}
	for _, c := range root.Commands() {
		taken[c.Name()] = true
		for _, alias := range c.Aliases {
			taken[alias] = true
		}
	}
	for _, group := range a.apiCmd.Commands() {
		if group.GroupID != "endpoints" || taken[group.Name()] {
			continue
		}
		name := group.Name()
		hint := &cobra.Command{Use: name, Aliases: group.Aliases, Hidden: true, DisableFlagParsing: true, RunE: func(_ *cobra.Command, args []string) error {
			return fmt.Errorf("API endpoint commands are under `edka api`: edka api %s", shellJoin(append([]string{name}, args...)))
		}}
		markStandalone(hint)
		root.AddCommand(hint)
	}
}

// shellJoin quotes arguments that a POSIX shell would split or expand.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = arg
		if arg == "" || strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%_+=:,./-", r))
		}) >= 0 {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}
func mark(annotation string, commands ...*cobra.Command) {
	for _, c := range commands {
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		c.Annotations[annotation] = "true"
	}
}
func markLocal(commands ...*cobra.Command)      { mark(localAnnotation, commands...) }
func markStandalone(commands ...*cobra.Command) { mark(standaloneAnnotation, commands...) }
func markConfigOnly(commands ...*cobra.Command) { mark(configAnnotation, commands...) }
func local(cmd *cobra.Command) bool             { return cmd.Annotations[localAnnotation] == "true" }
func standalone(cmd *cobra.Command) bool {
	return cmd.Annotations[standaloneAnnotation] == "true" || cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd
}
func argument(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// when formats an API timestamp in local time for tables.
func when(v any) string {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func (a *App) initialize(cmd *cobra.Command) error {
	if a.json {
		a.output = "json"
	}
	if a.output != "table" && a.output != "json" && a.output != "raw" {
		return fmt.Errorf("output must be table, json or raw")
	}
	if a.timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if os.Getenv("CI") != "" || envSet("EDKA_NO_INPUT") || !ui.InputTerminal(a.In) {
		a.noInput = true
	}
	switch a.colorMode {
	case "auto":
		a.color = ui.IsTerminal(a.Out) && os.Getenv("NO_COLOR") == ""
	case "always":
		a.color = true
	case "never":
		a.color = false
	default:
		return fmt.Errorf("color must be auto, always or never")
	}
	if a.HTTP == nil {
		a.HTTP = api.HTTPClient(a.timeout)
	}
	if a.debug || envSet("EDKA_DEBUG") {
		a.logRequests()
	}
	if cmd.Name() == cobra.ShellCompRequestCmd {
		a.registerCompletions(cmd.Root())
	}
	if a.checksForUpdates(cmd) {
		if dir, err := config.Dir(); err == nil {
			a.startUpdateCheck(cmd.Context(), dir)
		}
	}
	if standalone(cmd) {
		return nil
	}
	if err := a.loadConfig(); err != nil || cmd.Annotations[configAnnotation] == "true" {
		return err
	}
	if err := a.loadLink(); err != nil {
		return err
	}
	return a.resolveContext(cmd)
}
func (a *App) loadConfig() error {
	var err error
	a.dir, err = config.Dir()
	if err != nil {
		return err
	}
	a.cfg, err = config.Load(a.dir)
	return err
}

// loadLink finds the project link. A link that can't be read leaves a.link nil
// and a.linkPath naming its file.
func (a *App) loadLink() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	a.link, a.linkPath, err = config.FindLink(cwd)
	if err != nil && a.linkPath != "" {
		return fmt.Errorf("%w; fix the file, or remove it with `edka unlink` in %s", err, filepath.Dir(a.linkPath))
	}
	return err
}

// resolveContext chooses the profile, API origin and resources from flags,
// environment, the project link and the config, in that order.
func (a *App) resolveContext(cmd *cobra.Command) error {
	var err error
	linkedProfile := ""
	if a.link != nil {
		linkedProfile = a.link.Profile
	}
	a.profile = first(a.profileFlag, os.Getenv("EDKA_PROFILE"), linkedProfile, a.cfg.Active, "default")
	if !config.ValidName(a.profile) {
		return fmt.Errorf("invalid profile name; use letters, numbers, hyphens or underscores")
	}
	current, exists := a.cfg.Profiles[a.profile]
	if !exists && !local(cmd) {
		if a.profileFlag == "" && os.Getenv("EDKA_PROFILE") == "" && a.profile == linkedProfile {
			return fmt.Errorf("profile %q from %s does not exist; run `edka login --profile %s`, or `edka unlink` in %s", a.profile, a.linkPath, a.profile, filepath.Dir(a.linkPath))
		}
		return fmt.Errorf("profile %q does not exist; run `edka login --profile %s`", a.profile, a.profile)
	}
	a.current = current
	base := first(a.apiFlag, os.Getenv("EDKA_API_URL"), current.APIURL, config.DefaultAPI)
	a.current.APIURL, err = api.NormalizeBase(base)
	if err != nil {
		return err
	}
	linkCluster, linkDeployment, linkOrganization := "", "", ""
	if a.link != nil && a.link.Profile == a.profile && a.link.APIURL == a.current.APIURL {
		linkCluster = a.link.Cluster
		linkDeployment = a.link.Deployment
		linkOrganization = a.link.Organization
	}
	a.cluster = first(a.clusterFlag, os.Getenv("EDKA_CLUSTER"), linkCluster, current.Cluster)
	// `edka run` passes EDKA_DEPLOYMENT, even empty, beside EDKA_CLUSTER. Together
	// they pin the deployment, so a link can't add its own to another cluster.
	if _, set := os.LookupEnv("EDKA_DEPLOYMENT"); set && os.Getenv("EDKA_CLUSTER") != "" {
		linkDeployment = ""
	}
	a.deployment = first(a.deploymentFlag, os.Getenv("EDKA_DEPLOYMENT"), linkDeployment)
	a.organization = first(a.organizationFlag, os.Getenv("EDKA_ORGANIZATION"), linkOrganization, current.Organization)
	return a.resolveStoreMode(cmd)
}

// resolveStoreMode applies EDKA_CREDENTIAL_STORE unless --credential-store is set.
func (a *App) resolveStoreMode(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("credential-store") {
		a.storeMode = first(os.Getenv("EDKA_CREDENTIAL_STORE"), "auto")
	}
	if a.storeMode != "auto" && a.storeMode != "keyring" && a.storeMode != "file" {
		return fmt.Errorf("credential-store must be auto, keyring or file")
	}
	return nil
}

// envSet reports whether a variable is set to anything but 0 or false.
func envSet(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}
func (a *App) store() credential.Store { return credential.Store{Dir: a.dir, Mode: a.storeMode} }
func (a *App) client(ctx context.Context) (*api.Client, error) {
	token := os.Getenv("EDKA_TOKEN")
	if token == "" {
		var err error
		token, err = auth.Token(ctx, a.store(), a.profile, a.current.APIURL, a.HTTP)
		if err != nil {
			return nil, err
		}
	}
	return &api.Client{BaseURL: a.current.APIURL, Token: token, Organization: a.organization, Version: a.Version, HTTP: a.HTTP}, nil
}

// request sends an API request. A request that needs a passkey check runs the
// check, and after the user confirms it, request sends it once more.
func (a *App) request(ctx context.Context, method, path string, query url.Values, body []byte) (*api.Response, error) {
	response, err := a.send(ctx, method, path, query, body)
	var apiError *api.Error
	if a.verifying || !errors.As(err, &apiError) || apiError.Status != http.StatusPreconditionRequired || apiError.Reason != stepUpRequired {
		return response, err
	}
	if err := a.stepUp(ctx, apiError); err != nil {
		return nil, err
	}
	// A request that still needs the check fails with that answer.
	return a.send(ctx, method, path, query, body)
}

// send sends an API request once.
func (a *App) send(ctx context.Context, method, path string, query url.Values, body []byte) (*api.Response, error) {
	var response *api.Response
	err := a.busy(ctx, func() error {
		c, err := a.client(ctx)
		if err != nil {
			return err
		}
		response, err = c.Do(ctx, method, path, query, body)
		return err
	})
	return response, err
}

// busy runs fn behind the request animation. A wait that prints progress and a
// terminal without color run it without one.
func (a *App) busy(ctx context.Context, fn func() error) error {
	return a.working(ctx, "Connecting to Edka…", fn)
}

// working is busy with a label of its own.
func (a *App) working(ctx context.Context, label string, fn func() error) error {
	if a.polling || a.colorMode == "never" || os.Getenv("NO_COLOR") != "" {
		return fn()
	}
	return ui.Busy(ctx, a.Err, label, fn)
}
func (a *App) render(response *api.Response) error {
	return ui.Render(a.Out, response.Body, a.output, a.color)
}
func (a *App) message(format string, args ...any) { fmt.Fprintf(a.Err, format+"\n", args...) }
func (a *App) confirm(label string) error         { return a.confirmTyped(label, "yes") }

// confirmTyped asks for an exact answer; deletes that destroy data ask for the
// resource name so a reflexive "yes" cannot remove the wrong one.
func (a *App) confirmTyped(label, expected string) error {
	if a.yes {
		return nil
	}
	if a.noInput || !ui.IsTerminal(a.Err) {
		return fmt.Errorf("%s requires confirmation; rerun with --yes", label)
	}
	fmt.Fprintf(a.Err, "%s\nType %s to continue: ", ui.Clean(label), ui.Clean(expected))
	answer, err := a.readLine()
	if err != nil && answer == "" {
		return fmt.Errorf("confirmation cancelled")
	}
	if strings.TrimSpace(answer) != expected {
		return fmt.Errorf("operation cancelled")
	}
	return nil
}

// readLine reads one typed line from stdin.
func (a *App) readLine() (string, error) {
	if a.lines == nil {
		a.lines = bufio.NewReader(a.In)
	}
	return a.lines.ReadString('\n')
}
func (a *App) objects(ctx context.Context, path string) ([]map[string]any, error) {
	return a.objectsQuery(ctx, path, nil)
}
func (a *App) objectsQuery(ctx context.Context, path string, query url.Values) ([]map[string]any, error) {
	response, err := a.request(ctx, "GET", path, query, nil)
	if err != nil {
		return nil, err
	}
	return rowsOf(path, response.Body)
}

// rowsOf returns the records in the list response that path answered with.
func rowsOf(path string, body []byte) ([]map[string]any, error) {
	data, err := api.Data(body)
	if err != nil {
		return nil, err
	}
	if m, ok := data.(map[string]any); ok {
		for _, key := range []string{"items", "rows", "clusters", "deployments", "apps", "databases", "resources", "namespaces"} {
			if list, ok := m[key].([]any); ok {
				data = list
				break
			}
		}
	}
	list, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a resource list from %s", path)
	}
	result := make([]map[string]any, 0, len(list))
	for _, v := range list {
		if m, ok := v.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result, nil
}
func safeID(id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00\r\n") {
		return "", fmt.Errorf("invalid resource identifier")
	}
	return url.PathEscape(id), nil
}

// record returns the object under data, or under key for routes with their
// own envelope such as {"success": true, "database": {...}}.
func record(body []byte, key string) (map[string]any, error) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	v, ok := envelope["data"]
	if !ok {
		v = envelope[key]
	}
	if list, ok := v.([]any); ok && len(list) == 1 {
		v = list[0]
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response; rerun with --json to inspect it")
	}
	return m, nil
}

// writeOutput saves a response for --output-file to a private file.
func (a *App) writeOutput(path string, body []byte) error {
	if err := config.WritePrivate(path, body); err != nil {
		return err
	}
	a.message("✓ Saved %s", path)
	return nil
}
func jsonBody(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
func (a *App) addAuth(root *cobra.Command) {
	var readOnly, noBrowser bool
	var port int
	login := &cobra.Command{Use: "login", Short: "Sign in securely through your browser", GroupID: "start", Args: cobra.NoArgs, Example: "  edka login\n  edka login --read-only\n  edka login --profile local --api-url http://localhost:8080", RunE: func(cmd *cobra.Command, _ []string) error {
		if a.noInput && !ui.InputTerminal(a.In) {
			return fmt.Errorf("browser login needs a terminal; use EDKA_TOKEN for CI")
		}
		if os.Getenv("EDKA_TOKEN") != "" {
			return fmt.Errorf("unset EDKA_TOKEN before browser login")
		}
		if port < 0 || port > 65535 {
			return fmt.Errorf("callback port must be between 0 and 65535")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
		defer cancel()
		session, server, err := auth.Login(ctx, auth.LoginOptions{BaseURL: a.current.APIURL, Profile: a.profile, Dir: a.dir, ReadOnly: readOnly, NoBrowser: noBrowser, Port: port, Output: a.Err, HTTP: a.HTTP})
		if err != nil {
			return err
		}
		client := api.Client{BaseURL: a.current.APIURL, Token: session.AccessToken, Version: a.Version, HTTP: a.HTTP}
		identity, err := client.Do(ctx, "GET", "/api/cli/whoami", nil, nil)
		if err != nil {
			return fmt.Errorf("verify authorized identity: %w", err)
		}
		data, err := api.Data(identity.Body)
		if err != nil {
			return err
		}
		user, ok := data.(map[string]any)
		if !ok {
			return errors.New("invalid identity response")
		}
		organization, ok := user["organization"].(map[string]any)
		if !ok {
			return errors.New("identity response has no organization")
		}
		a.current.Organization, _ = organization["id"].(string)
		a.current.OrganizationName, _ = organization["name"].(string)
		a.current.ConsoleURL = server.ConsoleURL
		if a.current.Organization == "" {
			return errors.New("authorization has no organization")
		}
		location, err := a.store().Save(a.profile, session)
		if err != nil {
			return err
		}
		a.cfg.Profiles[a.profile] = a.current
		a.cfg.Active = a.profile
		if err := config.Save(a.dir, a.cfg); err != nil {
			return err
		}
		if location == "file" && a.storeMode == "auto" {
			a.message("System keyring unavailable. Credentials saved in a private file in %s.", a.dir)
		}
		a.message("✓ Signed in to %s as %s\n  Next: edka link", ui.Clean(a.current.OrganizationName), ui.Clean(fmt.Sprint(user["email"])))
		if a.output == "json" {
			return a.render(identity)
		}
		return nil
	}}
	login.Flags().BoolVar(&readOnly, "read-only", false, "Request read access only")
	login.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the authorization URL without opening it")
	login.Flags().IntVar(&port, "callback-port", 0, "Loopback callback port (0 chooses an available port)")
	var local bool
	logout := &cobra.Command{Use: "logout", Short: "Revoke access and remove saved credentials", GroupID: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		session, err := a.store().Load(a.profile)
		if err != nil && !errors.Is(err, credential.ErrNotFound) {
			return err
		}
		if session != nil && !local {
			if err := auth.Revoke(cmd.Context(), a.HTTP, session); err != nil {
				return err
			}
		}
		if err := a.store().Delete(a.profile); err != nil {
			return err
		}
		a.message("✓ Signed out of %s", a.profile)
		if a.output == "json" {
			return ui.Render(a.Out, []byte(`{"signed_out":true}`), "json", false)
		}
		return nil
	}}
	logout.Flags().BoolVar(&local, "local", false, "Remove local credentials without server revocation")
	whoami := &cobra.Command{Use: "whoami", Short: "Show your identity and authorized organization", GroupID: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := a.request(cmd.Context(), "GET", "/api/cli/whoami", nil, nil)
		if err != nil {
			return err
		}
		return a.render(response)
	}}
	markLocal(login)
	root.AddCommand(login, logout, whoami, a.verifyCommand())
}
