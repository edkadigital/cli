package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
)

// pollInterval is how often a wait reads the API again.
var pollInterval = 2 * time.Second

// rolloutStartWait is how long a finished build may take to start its
// auto-deploy. Edka starts it as soon as it saves the build.
var rolloutStartWait = 30 * time.Second

// pollRetry is how long a wait keeps reading while Edka can't be reached or
// answers with a server error, before it stops with that error.
var pollRetry = 2 * time.Minute

// progress prints a wait's state to stderr. Each kind of line prints only when
// its text changes, so a terminal and a CI log show the same lines.
type progress struct {
	w    io.Writer
	last map[string]string
}

// startProgress turns off the request animation, which would draw between the
// progress lines on every poll.
func (a *App) startProgress() *progress {
	a.polling = true
	return &progress{w: a.Err, last: map[string]string{}}
}

func (p *progress) say(kind, line string) {
	line = ui.Clean(line)
	if p.last[kind] == line {
		return
	}
	p.last[kind] = line
	if line != "" {
		fmt.Fprintln(p.w, line)
	}
}

// poll is a wait's read. A failure that may pass, such as a dropped connection
// or a 502, doesn't end the wait: poll reads again, less often each time, until
// the reads have failed for pollRetry.
func (a *App) poll(ctx context.Context, p *progress, path string, query url.Values) (*api.Response, error) {
	var failing time.Time
	delay := pollInterval
	for {
		response, err := a.request(ctx, "GET", path, query, nil)
		if err == nil {
			p.say("retry", "")
			return response, nil
		}
		if !api.Transient(err) {
			return nil, err
		}
		reason, _, _ := strings.Cut(err.Error(), "\n")
		// A read that Edka answered just as the wait ended reports its own
		// failure, and the caller needs to know that the wait ended.
		if ended := ctx.Err(); ended != nil {
			if errors.Is(err, ended) {
				return nil, err
			}
			return nil, fmt.Errorf("%w; the last read failed: %s", ended, reason)
		}
		if failing.IsZero() {
			failing = time.Now()
		} else if time.Since(failing) >= pollRetry {
			return nil, err
		}
		p.say("retry", "Could not read from Edka, trying again: "+reason)
		if ended := pause(ctx, delay); ended != nil {
			return nil, fmt.Errorf("%w; the last read failed: %s", ended, reason)
		}
		delay = min(2*delay, 8*pollInterval)
	}
}

// rollout reports how many pods run the new revision and are ready, and each
// pod that fails.
func (p *progress) rollout(state map[string]any) {
	if replicas, ok := state["replicas"].(map[string]any); ok {
		desired := number(replicas["desired"])
		p.say("replicas", fmt.Sprintf("Rolling out: %d/%d updated, %d/%d ready", number(replicas["updated"]), desired, number(replicas["ready"]), desired))
	}
	pods, _ := state["pods"].([]any)
	for _, pod := range pods {
		m, ok := pod.(map[string]any)
		if !ok || text(m, "status") != "failed" {
			continue
		}
		line := fmt.Sprintf("Pod %s: %s", text(m, "name"), first(text(m, "reason"), "failed"))
		switch restarts := number(m["restartCount"]); {
		case restarts == 1:
			line += ", 1 restart"
		case restarts > 1:
			line += fmt.Sprintf(", %d restarts", restarts)
		}
		p.say("pod "+text(m, "name"), line)
	}
}

// logStream prints what a growing log adds. It holds a line back until the
// line ends, so a line read half-written prints once, whole.
type logStream struct{ printed string }

func (s *logStream) write(w io.Writer, log string, final bool) {
	if !final {
		log = log[:strings.LastIndex(log, "\n")+1]
	} else if s.printed == "" && !strings.Contains(log, "\n") {
		// One unfinished line is Edka's note that no log exists, such as "Waiting for logs...".
		return
	}
	if log == "" {
		return
	}
	delta := logDelta(s.printed, log)
	s.printed = log
	if delta != "" {
		fmt.Fprint(w, delta)
		if !strings.HasSuffix(delta, "\n") {
			fmt.Fprintln(w)
		}
	}
}

// buildSteps describes each state of a build that is still running.
var buildSteps = map[string]string{
	"queued":    "Waiting for the build to start…",
	"cloning":   "Cloning the repository…",
	"building":  "Building the image…",
	"pushing":   "Pushing the image…",
	"deploying": "Deploying…",
}

// waitBuild streams a build's log to stderr until the build finishes, and
// returns the finished build.
func (a *App) waitBuild(ctx context.Context, id, buildID string) (*api.Response, map[string]any, error) {
	p := a.startProgress()
	path := "/api/deployments/" + id + "/builds/" + buildID
	var log logStream
	for {
		response, err := a.poll(ctx, p, path+"/logs", nil)
		if err != nil {
			return nil, nil, err
		}
		v, err := identityData(response.Body)
		if err != nil {
			return nil, nil, err
		}
		status := text(v, "status")
		finished := status == "success" || status == "failed" || status == "cancelled"
		if !finished {
			p.say("step", first(buildSteps[status], status))
		}
		logs, _ := v["logs"].(string)
		log.write(a.Err, ui.CleanText(logs), finished)
		if finished {
			break
		}
		if err := pause(ctx, pollInterval); err != nil {
			return nil, nil, fmt.Errorf("build did not finish: %w", err)
		}
	}
	response, err := a.poll(ctx, p, path, nil)
	if err != nil {
		return nil, nil, err
	}
	build, err := identityData(response.Body)
	return response, build, err
}

// buildRollout finds the generation auto-deploy started for a finished build:
// a revision after the generation before the build, from a build of the same
// image. It returns 0 when the revisions show none after rolloutStartWait.
func (a *App) buildRollout(ctx context.Context, id string, before int, imageTag string) (int, error) {
	p := a.startProgress()
	path := "/api/deployments/" + id + "/revisions"
	deadline := time.Now().Add(rolloutStartWait)
	for {
		response, err := a.poll(ctx, p, path, url.Values{"limit": {"10"}})
		if err != nil && ctx.Err() != nil {
			return 0, fmt.Errorf("the build succeeded, but its rollout did not start: %w", ctx.Err())
		}
		if err != nil {
			return 0, err
		}
		rows, err := rowsOf(path, response.Body)
		if err != nil {
			return 0, err
		}
		for _, row := range rows {
			if number(row["generation"]) > before && text(row, "source") == "build" && (imageTag == "" || text(row, "image_tag") == imageTag) {
				return number(row["generation"]), nil
			}
		}
		if !time.Now().Before(deadline) {
			return 0, nil
		}
		if err := pause(ctx, pollInterval); err != nil {
			return 0, fmt.Errorf("the build succeeded, but its rollout did not start: %w", err)
		}
	}
}

// waitDeployment waits until generation rolls out healthy and returns the
// runtime status. Progress goes to stderr: pods updated and ready, pods that
// fail, and messages from Edka such as an unreachable cluster.
func (a *App) waitDeployment(ctx context.Context, id, name string, generation int) (*api.Response, error) {
	p := a.startProgress()
	for {
		response, err := a.poll(ctx, p, "/api/deployments/"+id, nil)
		if err != nil {
			return nil, err
		}
		record, err := identityData(response.Body)
		if err != nil {
			return nil, err
		}
		current := number(record["spec_generation"])
		if current > generation {
			return nil, a.replaced(ctx, id, name, generation, current)
		}
		status := text(record, "status")
		if current == generation && (status == "failed" || status == "error") {
			return nil, rolloutFailed(name, generation, text(record, "status_message"))
		}
		p.say("message", text(record, "status_message"))
		if current == generation && number(record["applied_generation"]) == generation {
			runtime, state, err := a.runtimeState(ctx, p, id)
			if err == nil {
				p.rollout(state)
			}
			// Old healthy pods are never proof that our queued revision succeeded.
			if number(record["healthy_generation"]) == generation {
				if err != nil {
					return nil, err
				}
				switch strings.ToLower(text(state, "status")) {
				case "deployed":
					a.message("✓ %s is running generation %d", ui.Clean(name), generation)
					return runtime, nil
				case "failed":
					return nil, rolloutFailed(name, generation, text(state, "message"))
				}
			}
		} else {
			p.say("step", fmt.Sprintf("Applying generation %d…", generation))
		}
		if err := pause(ctx, pollInterval); err != nil {
			return nil, fmt.Errorf("generation %d did not finish: %w; inspect `edka deployments status %s`", generation, err, name)
		}
	}
}

// runtimeState reads what runs in the cluster for a deployment.
func (a *App) runtimeState(ctx context.Context, p *progress, id string) (*api.Response, map[string]any, error) {
	response, err := a.poll(ctx, p, "/api/deployments/"+id+"/status", nil)
	if err != nil {
		return nil, nil, err
	}
	state, err := identityData(response.Body)
	return response, state, err
}

func rolloutFailed(name string, generation int, reason string) error {
	message := fmt.Sprintf("generation %d failed", generation)
	if reason != "" {
		message += ": " + reason
	}
	return fmt.Errorf("%s\nFind the cause with `edka diagnose %s`", message, name)
}

// replaced explains a generation that a later one replaced before it rolled
// out, such as the automatic rollback after it failed. A diagnosis explains
// one that failed; the revisions show which change replaced the other.
func (a *App) replaced(ctx context.Context, id, name string, generation, current int) error {
	if response, err := a.request(ctx, "GET", fmt.Sprintf("/api/deployments/%s/revisions/%d", id, generation), nil, nil); err == nil {
		if revision, err := identityData(response.Body); err == nil && text(revision, "status") == "failed" {
			message := fmt.Sprintf("generation %d failed", generation)
			if reason := text(revision, "status_message"); reason != "" {
				message += ": " + reason
			}
			return fmt.Errorf("%s\nGeneration %d replaced it\nFind the cause with `edka diagnose %s`", message, current, name)
		}
	}
	return fmt.Errorf("generation %d was replaced by generation %d; inspect `edka deployments revisions %s`", generation, current, name)
}
