package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/edkadigital/cli/internal/config"
	"github.com/edkadigital/cli/internal/selfupdate"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// Tests point these at their own release channel and binary.
var (
	releases   = selfupdate.GitHub
	executable = selfupdate.Executable
	// updateCheckWait is how long a lookup of the latest release may take. A
	// request to github.com takes about 400 ms.
	updateCheckWait = 2 * time.Second
)

// available tells of a newer release, after `edka upgrade --check` and after
// a command that found one, with the command that installs it.
const available = "edka %s is available. You have %s. Run `%s` to install it."

// brewUpgrade installs a release over a binary Homebrew installed. Homebrew
// tracks the version it installed, so a binary that `edka upgrade` replaced
// would show Homebrew the old version.
const brewUpgrade = "brew upgrade edka"

// upgradeCommand is the command that installs a newer release over this
// binary.
func upgradeCommand() string {
	if path, err := executable(); err == nil && selfupdate.Homebrew(path) {
		return brewUpgrade
	}
	return "edka upgrade"
}

// releaseChannel is where releases are read, with the lines of --debug.
func (a *App) releaseChannel() selfupdate.Channel {
	channel := releases
	if a.HTTP == nil {
		return channel
	}
	if log, ok := a.HTTP.Transport.(*requestLog); ok {
		next := channel.Transport
		if next == nil {
			next = http.DefaultTransport
		}
		channel.Transport = &requestLog{next: next, out: log.out}
	}
	return channel
}

func (a *App) addUpgrade(root *cobra.Command) {
	var check bool
	a.upgradeCmd = &cobra.Command{Use: "upgrade", Short: "Install the latest release of the CLI", Long: "Download the latest release from github.com/edkadigital/cli, check it against the\nSHA-256 in the release's checksums.txt, and replace this binary with it.\n\nA download that does not match replaces nothing. `--check` reports the latest\nrelease without installing it.", GroupID: "tools", Args: cobra.NoArgs, Example: "  edka upgrade\n  edka upgrade --check\n  edka upgrade --check --json", RunE: func(cmd *cobra.Command, _ []string) error {
		released := selfupdate.Released(a.Version)
		if !released && !check {
			return fmt.Errorf("edka %s was built from a checkout, and a release does not replace it; update the checkout and run `make install`", a.Version)
		}
		channel := a.releaseChannel()
		lookup, cancel := context.WithTimeout(cmd.Context(), a.timeout)
		defer cancel()
		var latest string
		err := a.working(lookup, "Looking for the latest release…", func() (err error) {
			latest, err = channel.Latest(lookup)
			return err
		})
		if err != nil {
			return err
		}
		newer := selfupdate.Newer(latest, a.Version)
		result := map[string]any{"version": a.Version, "latest": latest, "update_available": newer, "updated": false}
		switch {
		case !released:
			a.message("The latest release is %s. edka %s was built from a checkout.", latest, a.Version)
		case !newer && !selfupdate.Newer(a.Version, latest):
			a.message("✓ edka %s is the latest release", a.Version)
		case !newer:
			a.message("✓ edka %s is newer than the latest release, %s", a.Version, latest)
		case check:
			a.message(available, latest, a.Version, upgradeCommand())
		default:
			path, err := executable()
			if err != nil {
				return err
			}
			if selfupdate.Homebrew(path) {
				return fmt.Errorf("edka %s is available, and Homebrew installed this binary; run `%s` to install it", latest, brewUpgrade)
			}
			// A release is about 10 MB, and a slow connection takes minutes for it.
			download, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			if err := a.working(download, "Downloading edka "+latest+"…", func() error { return channel.Install(download, latest, path) }); err != nil {
				return err
			}
			result["updated"] = true
			a.message("✓ Upgraded edka %s to %s", a.Version, latest)
		}
		if a.output == "json" {
			return ui.Render(a.Out, jsonBody(result), "json", false)
		}
		return nil
	}}
	a.upgradeCmd.Flags().BoolVar(&check, "check", false, "Report the latest release without installing it")
	markStandalone(a.upgradeCmd)
	root.AddCommand(a.upgradeCmd)
}

const (
	// updateCheckFile holds the time of the last lookup, in the config directory.
	updateCheckFile  = "update-check.json"
	updateCheckEvery = 24 * time.Hour
)

type updateState struct {
	CheckedAt time.Time `json:"checked_at"`
}

// updateCheck is a lookup of the latest release that runs while a command works.
type updateCheck struct {
	dir    string
	cancel context.CancelFunc
	done   chan updateResult
}

type updateResult struct {
	latest string
	err    error
}

// checksForUpdates reports whether a command looks for a newer release: a
// released binary at a terminal, outside CI. `edka upgrade` looks itself.
func (a *App) checksForUpdates(cmd *cobra.Command) bool {
	return selfupdate.Released(a.Version) && cmd != a.upgradeCmd && os.Getenv("CI") == "" && !envSet("EDKA_NO_UPDATE_CHECK") && ui.IsTerminal(a.Out) && ui.IsTerminal(a.Err)
}

// startUpdateCheck looks for the latest release while the command works,
// once a day. The lookup gives up after updateCheckWait.
func (a *App) startUpdateCheck(ctx context.Context, dir string) {
	var state updateState
	if data, err := os.ReadFile(filepath.Join(dir, updateCheckFile)); err == nil && json.Unmarshal(data, &state) == nil {
		// A time in the future is a clock that was set back since.
		if since := time.Since(state.CheckedAt); since >= 0 && since < updateCheckEvery {
			return
		}
	}
	ctx, cancel := context.WithTimeout(ctx, updateCheckWait)
	a.updateCheck = &updateCheck{dir: dir, cancel: cancel, done: make(chan updateResult, 1)}
	channel, done := a.releaseChannel(), a.updateCheck.done
	go func() {
		latest, err := channel.Latest(ctx)
		done <- updateResult{latest, err}
	}()
}

// finishUpdateCheck prints the notice of a newer release when the lookup
// found one. A command that ends before the lookup waits for it, since a
// request to Edka takes a quarter of the time of one to GitHub. A lookup is
// the last one for a day, failed or not, so that a network without GitHub
// holds one command a day. An interrupted one is dropped.
func (a *App) finishUpdateCheck() {
	check := a.updateCheck
	if check == nil {
		return
	}
	a.updateCheck = nil
	result := <-check.done
	check.cancel()
	if errors.Is(result.err, context.Canceled) {
		return
	}
	// The notice is a courtesy, so a config directory that can't be written
	// costs a lookup with each command and nothing else.
	_ = config.WriteJSON(filepath.Join(check.dir, updateCheckFile), updateState{CheckedAt: time.Now().UTC()})
	if result.err == nil && selfupdate.Newer(result.latest, a.Version) {
		a.message("\n"+available, result.latest, a.Version, upgradeCommand())
	}
}
