package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edkadigital/cli/internal/selfupdate"
	"github.com/edkadigital/cli/internal/selfupdate/updatetest"
)

// asRelease runs the CLI as the release version, with release as the latest
// one. It returns the path of the binary an upgrade replaces.
func asRelease(t *testing.T, version string, release *updatetest.Release, args ...string) (path, out, errOut string, err error) {
	t.Helper()
	return asReleaseAt(t, filepath.Join(t.TempDir(), "edka"), version, release, args...)
}

// asReleaseAt runs the CLI as asRelease does, with its binary at path.
func asReleaseAt(t *testing.T, path, version string, release *updatetest.Release, args ...string) (_, out, errOut string, err error) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	channel, binary := releases, executable
	t.Cleanup(func() { releases, executable = channel, binary })
	releases, executable = release.Channel, func() (string, error) { return path, nil }
	t.Setenv("EDKA_CONFIG_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	root := New(version, strings.NewReader(""), &stdout, &stderr)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return path, stdout.String(), stderr.String(), err
}

func installedBinary(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpgradeInstallsTheLatestRelease(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	path, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade")
	if err != nil || out != "" || errOut != "✓ Upgraded edka v1.2.3 to v1.3.0\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if got := installedBinary(t, path); got != "new binary" {
		t.Fatal(got)
	}
	// The result is on stdout for a script, and the progress on stderr.
	path, out, errOut, err = asRelease(t, "v1.2.3", release, "upgrade", "--json")
	var result struct {
		Version, Latest string
		UpdateAvailable bool `json:"update_available"`
		Updated         bool
	}
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || !strings.Contains(errOut, "✓ Upgraded") {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if result.Version != "v1.2.3" || result.Latest != "v1.3.0" || !result.UpdateAvailable || !result.Updated || installedBinary(t, path) != "new binary" {
		t.Fatalf("%+v", result)
	}
}

func TestUpgradeCheckReportsTheLatestReleaseAndInstallsNothing(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	path, out, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--check")
	if err != nil || out != "" || errOut != "edka v1.3.0 is available. You have v1.2.3. Run `edka upgrade` to install it.\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if got := installedBinary(t, path); got != "old binary" {
		t.Fatal(got)
	}
	if requests := release.Requests(); len(requests) != 1 || !strings.HasSuffix(requests[0], "/releases/latest") {
		t.Fatal(requests)
	}
	_, out, _, err = asRelease(t, "v1.2.3", release, "upgrade", "--check", "--json")
	if err != nil || !strings.Contains(out, `"update_available": true`) || !strings.Contains(out, `"updated": false`) {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
}

func TestUpgradeLeavesTheLatestReleaseInPlace(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	for version, want := range map[string]string{
		"v1.3.0":      "✓ edka v1.3.0 is the latest release\n",
		"1.3.0":       "✓ edka 1.3.0 is the latest release\n",
		"v1.4.0-rc.1": "✓ edka v1.4.0-rc.1 is newer than the latest release, v1.3.0\n",
	} {
		for _, args := range [][]string{{"upgrade"}, {"upgrade", "--check"}} {
			path, out, errOut, err := asRelease(t, version, release, args...)
			if err != nil || out != "" || errOut != want || installedBinary(t, path) != "old binary" {
				t.Errorf("%s %v: stdout=%q stderr=%q err=%v", version, args, out, errOut, err)
			}
		}
	}
	for _, request := range release.Requests() {
		if !strings.HasSuffix(request, "/releases/latest") {
			t.Fatal(release.Requests())
		}
	}
	// A pre-release moves to the release it led to.
	path, _, errOut, err := asRelease(t, "v1.3.0-rc.1", release, "upgrade")
	if err != nil || installedBinary(t, path) != "new binary" {
		t.Fatalf("stderr=%q err=%v", errOut, err)
	}
}

// Homebrew records the version it installed, so `brew upgrade` replaces its
// binary, and the CLI says so instead of replacing the binary itself.
func TestUpgradeLeavesABinaryOfHomebrewToBrew(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	cellar := filepath.Join(t.TempDir(), "Cellar", "edka", "1.2.3", "bin", "edka")
	path, out, errOut, err := asReleaseAt(t, cellar, "v1.2.3", release, "upgrade")
	if err == nil || !strings.Contains(err.Error(), "run `brew upgrade edka`") || out != "" || errOut != "" || installedBinary(t, path) != "old binary" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if requests := release.Requests(); len(requests) != 1 || !strings.HasSuffix(requests[0], "/releases/latest") {
		t.Fatal(requests)
	}
	_, _, errOut, err = asReleaseAt(t, cellar, "v1.2.3", release, "upgrade", "--check")
	if err != nil || errOut != "edka v1.3.0 is available. You have v1.2.3. Run `brew upgrade edka` to install it.\n" {
		t.Fatalf("stderr=%q err=%v", errOut, err)
	}
	// The notice of a newer release names the same command.
	channel, binary := releases, executable
	t.Cleanup(func() { releases, executable = channel, binary })
	releases, executable = release.Channel, func() (string, error) { return cellar, nil }
	var notice bytes.Buffer
	a := &App{Err: &notice, Version: "v1.2.3"}
	lookup(t, a, t.TempDir())
	a.finishUpdateCheck()
	if notice.String() != "\nedka v1.3.0 is available. You have v1.2.3. Run `brew upgrade edka` to install it.\n" {
		t.Fatalf("%q", notice.String())
	}
}

// `make install` builds dev, and a release does not replace a checkout's build.
func TestUpgradeLeavesABuildFromACheckout(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	path, _, _, err := asRelease(t, "dev", release, "upgrade")
	if err == nil || !strings.Contains(err.Error(), "edka dev was built from a checkout") || installedBinary(t, path) != "old binary" {
		t.Fatal(err)
	}
	if len(release.Requests()) != 0 {
		t.Fatal(release.Requests())
	}
	_, out, errOut, err := asRelease(t, "dev", release, "upgrade", "--check", "--json")
	if err != nil || errOut != "The latest release is v1.3.0. edka dev was built from a checkout.\n" || !strings.Contains(out, `"update_available": false`) {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
}

func TestUpgradeKeepsTheBinaryWhenTheDownloadFails(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, tampered := updatetest.Archive(t, "v1.3.0", runtime.GOOS, runtime.GOARCH, []byte("another binary"))
	release.Replace(release.Name(), tampered)
	path, out, _, err := asRelease(t, "v1.2.3", release, "upgrade")
	if err == nil || out != "" || !strings.Contains(err.Error(), "does not match its SHA-256 in checksums.txt; nothing was replaced") {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
	if got := installedBinary(t, path); got != "old binary" {
		t.Fatal(got)
	}
	// GitHub answers 404 while the repository is private.
	release.RedirectLatest("")
	_, _, _, err = asRelease(t, "v1.2.3", release, "upgrade", "--check")
	if err == nil || !strings.Contains(err.Error(), "no release found at 127.0.0.1") || !strings.Contains(err.Error(), "/edkadigital/cli (HTTP 404)") {
		t.Fatal(err)
	}
}

func TestUpgradeDebugPrintsItsRequests(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	_, _, errOut, err := asRelease(t, "v1.2.3", release, "upgrade", "--debug")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/edkadigital/cli/releases/latest 302 ", "/edkadigital/cli/releases/download/v1.3.0/checksums.txt 302 ", "/checksums.txt 200 ", "/" + release.Name() + " 200 "} {
		if !strings.Contains(errOut, want) {
			t.Errorf("no %q in %q", want, errOut)
		}
	}
}

// The upgrade replaces the program that runs it. A copy of this test binary
// stands in for edka, since Windows treats a running program unlike a file.
func TestUpgradeReplacesTheRunningBinary(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "edka")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	source, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, release.CA(), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestUpgradeHelper$")
	cmd.Env = append(os.Environ(), "EDKA_UPGRADE_HELPER="+release.Channel.URL, "EDKA_UPGRADE_HELPER_CA="+ca, "EDKA_CONFIG_DIR="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(output), "✓ Upgraded edka v1.2.3 to v1.3.0") {
		t.Fatalf("%s: %v", output, err)
	}
	if got := installedBinary(t, path); got != "new binary" {
		t.Fatalf("%d bytes", len(got))
	}
}

func TestUpgradeHelper(t *testing.T) {
	address := os.Getenv("EDKA_UPGRADE_HELPER")
	if address == "" {
		return
	}
	ca, err := os.ReadFile(os.Getenv("EDKA_UPGRADE_HELPER_CA"))
	if err != nil {
		t.Fatal(err)
	}
	releases = selfupdate.Channel{URL: address, Hosts: []string{"127.0.0.1"}, Transport: updatetest.Trusting(ca)}
	root := New("v1.2.3", strings.NewReader(""), os.Stdout, os.Stderr)
	root.SetArgs([]string{"upgrade"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// lookup starts the lookup of a command and waits until it has its answer.
func lookup(t *testing.T, a *App, dir string) {
	t.Helper()
	a.startUpdateCheck(context.Background(), dir)
	if a.updateCheck == nil {
		return
	}
	for deadline := time.Now().Add(10 * time.Second); len(a.updateCheck.done) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the lookup did not finish")
		}
	}
}

func TestACommandTellsOfANewerReleaseOnceADay(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	channel := releases
	t.Cleanup(func() { releases = channel })
	releases = release.Channel
	dir := t.TempDir()
	var errOut bytes.Buffer
	a := &App{Err: &errOut, Version: "v1.2.3"}
	lookup(t, a, dir)
	a.finishUpdateCheck()
	if errOut.String() != "\nedka v1.3.0 is available. You have v1.2.3. Run `edka upgrade` to install it.\n" {
		t.Fatalf("%q", errOut.String())
	}
	// The next commands of the day send nothing and print nothing.
	errOut.Reset()
	lookup(t, a, dir)
	a.finishUpdateCheck()
	if errOut.Len() != 0 || len(release.Requests()) != 1 {
		t.Fatalf("%q %v", errOut.String(), release.Requests())
	}
	// A day later the command looks again.
	for _, checked := range []time.Time{time.Now().Add(-25 * time.Hour), time.Now().Add(48 * time.Hour)} {
		errOut.Reset()
		if err := os.WriteFile(filepath.Join(dir, updateCheckFile), jsonBody(updateState{CheckedAt: checked}), 0o600); err != nil {
			t.Fatal(err)
		}
		lookup(t, a, dir)
		a.finishUpdateCheck()
		if !strings.Contains(errOut.String(), "edka v1.3.0 is available") {
			t.Fatalf("%s: %q", checked, errOut.String())
		}
	}
	// The latest release has nothing to tell.
	if err := os.Remove(filepath.Join(dir, updateCheckFile)); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	a.Version = "v1.3.0"
	lookup(t, a, dir)
	a.finishUpdateCheck()
	if _, err := os.Stat(filepath.Join(dir, updateCheckFile)); err != nil || errOut.Len() != 0 {
		t.Fatalf("%q: %v", errOut.String(), err)
	}
}

// A lookup that fails is the last one for a day, so that a network without
// GitHub does not get a request with every command.
func TestAFailedLookupWaitsADay(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	release.RedirectLatest("")
	channel := releases
	t.Cleanup(func() { releases = channel })
	releases = release.Channel
	dir := t.TempDir()
	var errOut bytes.Buffer
	a := &App{Err: &errOut, Version: "v1.2.3"}
	for range 3 {
		lookup(t, a, dir)
		a.finishUpdateCheck()
	}
	if errOut.Len() != 0 || len(release.Requests()) != 1 {
		t.Fatalf("%q %v", errOut.String(), release.Requests())
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A command waits for the lookup for a bounded time. A lookup that gets no
// answer in that time is the last one for a day, and an interrupted one is not.
func TestACommandWaitsForTheLookupForABoundedTime(t *testing.T) {
	channel, wait := releases, updateCheckWait
	t.Cleanup(func() { releases, updateCheckWait = channel, wait })
	updateCheckWait = 50 * time.Millisecond
	requests := make(chan struct{}, 2)
	releases = selfupdate.Channel{URL: "https://github.example/edkadigital/cli", Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	dir := t.TempDir()
	var errOut bytes.Buffer
	a := &App{Err: &errOut, Version: "v1.2.3"}
	// Ctrl-C ends the command and its lookup.
	interrupted, interrupt := context.WithCancel(context.Background())
	a.startUpdateCheck(interrupted, dir)
	<-requests
	interrupt()
	a.finishUpdateCheck()
	if _, err := os.Stat(filepath.Join(dir, updateCheckFile)); !os.IsNotExist(err) || errOut.Len() != 0 || a.updateCheck != nil {
		t.Fatalf("%q: %v", errOut.String(), err)
	}
	started := time.Now()
	a.startUpdateCheck(context.Background(), dir)
	a.finishUpdateCheck()
	if took := time.Since(started); took < updateCheckWait || took > 5*time.Second {
		t.Fatal(took)
	}
	if _, err := os.Stat(filepath.Join(dir, updateCheckFile)); err != nil || errOut.Len() != 0 {
		t.Fatalf("%q: %v", errOut.String(), err)
	}
	a.startUpdateCheck(context.Background(), dir)
	if a.updateCheck != nil || len(requests) != 1 {
		t.Fatal("looked again within the day")
	}
	// A command that started no lookup has none to finish.
	a.finishUpdateCheck()
}

// A script, a pipe and CI get no notice and send no request: the lookup runs
// at a terminal only, and for a release only.
func TestCommandsLookForReleasesAtATerminalOnly(t *testing.T) {
	release := updatetest.Publish(t, "v1.3.0", []byte("new binary"))
	t.Setenv("EDKA_TOKEN", "test-token")
	t.Setenv("EDKA_API_URL", "http://127.0.0.1:1")
	_, out, errOut, err := asRelease(t, "v1.2.3", release, "version")
	if err != nil || !strings.HasPrefix(out, "edka v1.2.3\n") || errOut != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("EDKA_CONFIG_DIR"), updateCheckFile)); !os.IsNotExist(err) || len(release.Requests()) != 0 {
		t.Fatalf("%v: %v", release.Requests(), err)
	}
	root := New("v1.2.3", strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	version, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]*App{
		"a pipe":     {Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Version: "v1.2.3"},
		"a checkout": {Out: os.Stdout, Err: os.Stderr, Version: "dev"},
	} {
		if a.checksForUpdates(version) {
			t.Errorf("%s looks for a release", name)
		}
	}
}
