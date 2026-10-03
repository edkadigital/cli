package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/auth"
	"github.com/edkadigital/cli/internal/config"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

func identityData(body []byte) (map[string]any, error) {
	v, err := api.Data(body)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid identity response")
	}
	return m, nil
}
func (a *App) addWorkflows(root *cobra.Command) {
	status := &cobra.Command{Use: "status", Short: "Inspect your linked cluster and deployment", GroupID: "work", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cluster, err := a.clusterID(cmd.Context())
		if err != nil {
			return err
		}
		if a.deployment != "" {
			id, err := a.deploymentID(cmd.Context(), "")
			if err != nil {
				return err
			}
			response, err := a.request(cmd.Context(), "GET", "/api/deployments/"+id+"/status", nil, nil)
			if err != nil {
				return err
			}
			return a.renderDeploymentStatus(response)
		}
		return a.showCluster(cmd.Context(), cluster)
	}}
	a.addUp(root)

	logs := a.logsCommand("logs [deployment]", "Read deployment logs; follow with --follow", a.deploymentLogs)
	logs.GroupID = "work"
	a.completes(a.deploymentChoices, logs)
	open := &cobra.Command{Use: "open", Short: "Open the linked cluster in the Edka console", GroupID: "work", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cluster, err := a.clusterID(cmd.Context())
		if err != nil {
			return err
		}
		identity, err := a.request(cmd.Context(), "GET", "/api/cli/whoami", nil, nil)
		if err != nil {
			return err
		}
		v, err := identityData(identity.Body)
		if err != nil {
			return err
		}
		org, _ := v["organization"].(map[string]any)
		slug, _ := org["slug"].(string)
		if slug == "" {
			return fmt.Errorf("organization has no console slug")
		}
		base := a.current.ConsoleURL
		if base == "" {
			server, _, err := auth.Discover(cmd.Context(), a.current.APIURL, a.HTTP)
			if err != nil {
				return err
			}
			base = server.ConsoleURL
		}
		normalized, err := api.NormalizeBase(base)
		if err != nil {
			return err
		}
		address := normalized + "/accounts/" + url.PathEscape(slug) + "/clusters/" + cluster
		a.message("%s", address)
		if a.noInput || a.output == "json" {
			return ui.Render(a.Out, jsonBody(map[string]string{"url": address}), "json", false)
		}
		return auth.OpenBrowser(address)
	}}
	// Run sets context for local tooling without fetching application secrets.
	var withKubeconfig bool
	run := &cobra.Command{Use: "run -- <command> [args…]", Short: "Run a local command with EDKA_* context variables set", Long: "Run a local command with the current Edka context in its environment, as\nEDKA_PROFILE, EDKA_API_URL, EDKA_ORGANIZATION, EDKA_CLUSTER and EDKA_DEPLOYMENT.\n\n`edka` commands inside it read these variables ahead of the directory link and\nthe active profile, so a script keeps the same target after it changes\ndirectory. A variable with no value is set empty, and those commands fall back\nto the link and profile for it. When run has a cluster, an empty EDKA_DEPLOYMENT\ninstead means no deployment, and no link's deployment applies.\n\nWith --kubeconfig, run also sets KUBECONFIG to a private file with your own\nkubeconfig for the cluster. It expires after an hour, and run deletes the file\nwhen the command exits. The one-time kubeconfig download stays available.\n\nrun passes no API token or application secrets, and exits with the command's\nexit code. Ctrl+C and SIGTERM reach the command, and run waits for it to exit.", GroupID: "work", Args: cobra.MinimumNArgs(1), DisableFlagParsing: false, Example: "  edka run --cluster staging -- ./scripts/release.sh\n  edka run --kubeconfig -- kubectl get pods -A\n  edka run -- sh -c 'echo \"$EDKA_CLUSTER $EDKA_DEPLOYMENT\"'", RunE: func(cmd *cobra.Command, args []string) error {
		var kubeconfig string
		if withKubeconfig {
			path, cleanup, err := a.temporaryKubeconfig(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			kubeconfig = "KUBECONFIG=" + path
		}
		command := exec.Command(args[0], args[1:]...)
		command.Stdin = a.In
		command.Stdout = a.Out
		command.Stderr = a.Err
		command.Env = append(os.Environ(), "EDKA_PROFILE="+a.profile, "EDKA_API_URL="+a.current.APIURL, "EDKA_ORGANIZATION="+a.organization, "EDKA_CLUSTER="+a.cluster, "EDKA_DEPLOYMENT="+a.deployment)
		if kubeconfig != "" {
			command.Env = append(command.Env, kubeconfig)
		}
		return a.runChild(cmd.Context(), command)
	}}
	run.Flags().BoolVar(&withKubeconfig, "kubeconfig", false, "Set KUBECONFIG to your own kubeconfig for the cluster, valid for an hour")
	run.ValidArgsFunction = files
	root.AddCommand(status, logs, open, run)
}

// temporaryKubeconfig writes an hour-long kubeconfig for the context cluster
// to a new private directory. cleanup removes the directory; the kubeconfig
// stays valid until it expires.
func (a *App) temporaryKubeconfig(ctx context.Context) (path string, cleanup func(), err error) {
	c, err := a.resolveCluster(ctx, "")
	if err != nil {
		return "", nil, err
	}
	id, err := safeID(c.ID)
	if err != nil {
		return "", nil, err
	}
	response, err := a.request(ctx, "POST", "/api/clusters/"+id+"/user-kubeconfig/temporary", nil, nil)
	var apiError *api.Error
	if errors.As(err, &apiError) && apiError.Status == 409 {
		return "", nil, fmt.Errorf("%w\nRotating revokes your previous kubeconfig: edka api clusters user-credentials rotate-own --cluster %s", err, ui.Clean(c.Name))
	}
	if err != nil {
		return "", nil, err
	}
	data, err := api.Data(response.Body)
	if err != nil {
		return "", nil, err
	}
	m, _ := data.(map[string]any)
	content := text(m, "kubeconfig")
	if content == "" {
		return "", nil, fmt.Errorf("the response had no kubeconfig")
	}
	dir, err := os.MkdirTemp("", "edka-kubeconfig-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, "kubeconfig")
	if err := config.WritePrivate(path, []byte(content)); err != nil {
		cleanup()
		return "", nil, err
	}
	valid := "for an hour"
	if expires, err := time.Parse(time.RFC3339, text(m, "expires_at")); err == nil {
		valid = "until " + expires.Local().Format("15:04")
	}
	a.message("✓ Temporary kubeconfig for %s, valid %s", ui.Clean(c.Name), valid)
	return path, cleanup, nil
}

// ExitError carries the exit code of a command started by `edka run`. The
// command reported its own failure, so edka exits with the code silently.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("command exited with status %d", e.Code) }

// runChild starts a command for `edka run` and waits for it to exit. It never
// kills the command: an interrupt or SIGTERM reaches the command, which stops
// in its own time, and edka exits with its exit code.
func (a *App) runChild(ctx context.Context, command *exec.Cmd) error {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	// A signal that arrived before now cancelled the context.
	if err := ctx.Err(); err != nil {
		return err
	}
	// A terminal sends Ctrl+C to the command as well, and Windows sends it every
	// console event, so passing those on would interrupt it twice.
	terminal := ui.InputTerminal(a.In) || ui.IsTerminal(a.Out) || ui.IsTerminal(a.Err)
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	for {
		select {
		case sig := <-signals:
			if runtime.GOOS != "windows" && !(sig == os.Interrupt && terminal) {
				_ = command.Process.Signal(sig)
			}
		case err := <-done:
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return err
			}
			// A command a signal ended exits as it would under a shell: 128 plus the signal.
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return &ExitError{Code: 128 + int(status.Signal())}
			}
			if exit.ExitCode() > 0 {
				return &ExitError{Code: exit.ExitCode()}
			}
			return err
		}
	}
}

func (a *App) deploymentLogs(ctx context.Context, target string) (string, error) {
	id, err := a.deploymentID(ctx, target)
	if err != nil {
		return "", err
	}
	return "/api/deployments/" + id + "/logs", nil
}

// logsCommand reads pod logs from the path that locate resolves for its
// argument. Deployment, app and cronjob log routes share these parameters.
func (a *App) logsCommand(use, short string, locate func(context.Context, string) (string, error)) *cobra.Command {
	var follow, previous bool
	var tail int
	var interval time.Duration
	var pod, container string
	cmd := &cobra.Command{Use: use, Short: short, Long: short + ".\n\n--follow reads the log again every --interval and prints the new lines until\nCtrl+C. Unless --pod names a pod, Edka picks one for each read, so after a\nrollout the command follows a new pod. It names the pod it follows on stderr,\nand says when lines may be missing because more than --tail lines arrived\nbetween two reads.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		// Edka returns at most 2000 lines and silently clamps larger requests.
		if tail < 1 || tail > 2000 {
			return fmt.Errorf("tail must be between 1 and 2000")
		}
		if interval < minLogInterval {
			return fmt.Errorf("interval must be at least one second")
		}
		if follow && a.output == "json" {
			return fmt.Errorf("use --output raw with --follow, or --json for one log snapshot")
		}
		if follow && previous {
			return fmt.Errorf("logs from a previous container do not change; omit --follow")
		}
		target := ""
		if len(args) > 0 {
			target = args[0]
		}
		path, err := locate(cmd.Context(), target)
		if err != nil {
			return err
		}
		query := url.Values{"tailLines": {fmt.Sprint(tail)}}
		if pod != "" {
			query.Set("podName", pod)
		}
		if container != "" {
			query.Set("container", container)
		}
		if previous {
			query.Set("previous", "true")
		}
		if !follow {
			response, err := a.request(cmd.Context(), "GET", path, query, nil)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.render(response)
			}
			log, _, note, err := readLog(response.Body)
			if err != nil {
				return err
			}
			if note != "" {
				a.message("%s", ui.Clean(note))
			}
			printLog(a.Out, log)
			return nil
		}
		p := a.startProgress()
		logs := logFollower{last: map[string]string{}}
		for {
			response, err := a.poll(cmd.Context(), p, path, query)
			var apiError *api.Error
			switch {
			case errors.As(err, &apiError) && apiError.Status == 404 && strings.HasPrefix(apiError.Reason, "No pods"):
				// A deployment with a volume that one node mounts stops its pod
				// before the new one starts.
				p.say("note", "No pod is running; waiting for one…")
			case err != nil:
				return err
			default:
				log, from, note, err := readLog(response.Body)
				if err != nil {
					return err
				}
				logs.read(a.Out, p, from, log, note)
			}
			if err := pause(cmd.Context(), interval); err != nil {
				return err
			}
		}
	}}
	cmd.Flags().BoolVarP(&follow, "follow", "F", false, "Poll new log lines until Ctrl+C")
	cmd.Flags().IntVarP(&tail, "tail", "n", 100, "Number of log lines (at most 2000)")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "Log polling interval")
	cmd.Flags().StringVar(&pod, "pod", "", "Pod name (default: the failing pod with most restarts, else the newest)")
	cmd.Flags().StringVar(&container, "container", "", "Container name")
	cmd.Flags().BoolVar(&previous, "previous", false, "Read the previous container instance, for example after a crash")
	return cmd
}

// minLogInterval is the shortest --interval between two reads of a log.
var minLogInterval = time.Second

func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func logText(body []byte) (string, error) {
	data, err := api.Data(body)
	if err != nil {
		return "", err
	}
	if text, ok := data.(string); ok {
		return ui.CleanText(text), nil
	}
	if m, ok := data.(map[string]any); ok {
		if text, ok := m["logs"].(string); ok {
			return ui.CleanText(text), nil
		}
		if rows, ok := m["logs"].([]any); ok {
			data = rows
		}
	}
	if rows, ok := data.([]any); ok {
		lines := []string{}
		for _, row := range rows {
			if s, ok := row.(string); ok {
				lines = append(lines, s)
			} else if m, ok := row.(map[string]any); ok {
				for _, key := range []string{"message", "log", "line"} {
					if s, ok := m[key].(string); ok {
						lines = append(lines, s)
						break
					}
				}
			}
		}
		return ui.CleanText(strings.Join(lines, "\n")), nil
	}
	return "", fmt.Errorf("unrecognized log response; rerun with --json to inspect it")
}

// readLog reads a log response: the log, the pod it came from, and the note
// Edka sends in place of a log, such as "Container is still starting and has
// not produced logs yet." A note is one line with no line break.
func readLog(body []byte) (log, pod, note string, err error) {
	log, err = logText(body)
	if err != nil {
		return "", "", "", err
	}
	data, _ := api.Data(body)
	m, _ := data.(map[string]any)
	pod = text(m, "podName")
	if raw := text(m, "logs"); raw != "" && !strings.Contains(raw, "\n") {
		return "", pod, log, nil
	}
	return log, pod, "", nil
}

// printLog prints a log and ends its last line.
func printLog(w io.Writer, log string) {
	if log == "" {
		return
	}
	fmt.Fprint(w, log)
	if !strings.HasSuffix(log, "\n") {
		fmt.Fprintln(w)
	}
}

// logFollower follows a log that Edka reads from one pod at a time. Edka picks
// the pod again for each read, so after a rollout a read can come from another
// pod. Each read is compared with the last one from the same pod.
type logFollower struct {
	pod  string
	last map[string]string
}

// read prints on out what a read adds to its pod's log. The progress names the
// pod it follows, Edka's notes, and lines that may be missing.
func (f *logFollower) read(out io.Writer, p *progress, pod, log, note string) {
	if pod != "" && pod != f.pod {
		f.pod = pod
		p.say("pod", "Following pod "+pod)
	}
	p.say("note", note)
	if note != "" {
		return
	}
	previous, seen := f.last[pod]
	delta, continued := logDelta(previous, log)
	f.last[pod] = log
	if seen && !continued {
		p.say("gap", fmt.Sprintf("Lines of pod %s may be missing: its log does not continue from the last read. Raise --tail or lower --interval to keep up, or read --previous if the container restarted.", pod))
	} else {
		p.say("gap", "")
	}
	printLog(out, delta)
}

// logDelta returns what current adds to previous, two reads of the end of a
// log, and whether current continues previous. It doesn't when the log grew by
// more than a read holds between the reads, or started again.
func logDelta(previous, current string) (string, bool) {
	if previous == "" {
		return current, true
	}
	if strings.HasPrefix(current, previous) {
		return strings.TrimPrefix(current, previous), true
	}
	old := strings.Split(strings.TrimSuffix(previous, "\n"), "\n")
	now := strings.Split(strings.TrimSuffix(current, "\n"), "\n")
	for n := min(len(old), len(now)); n > 0; n-- {
		if strings.Join(old[len(old)-n:], "\n") == strings.Join(now[:n], "\n") {
			return strings.Join(now[n:], "\n"), true
		}
	}
	return current, false
}
