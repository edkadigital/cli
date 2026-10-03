package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/edkadigital/cli/internal/selfupdate/updatetest"
)

// scriptRelease serves a release as GitHub does, with install.sh and
// install.ps1 among its files: latest/download redirects to the latest tag,
// a tag's download redirects to the file, and each file is
// application/octet-stream.
type scriptRelease struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string][]byte
	requests []string
}

const releasePath = "/edkadigital/cli/releases"

func publishScripts(t *testing.T, version string, binary []byte) *scriptRelease {
	t.Helper()
	r := &scriptRelease{files: map[string][]byte{}}
	var sums strings.Builder
	for _, platform := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		name, data := updatetest.Archive(t, version, platform[0], platform[1], binary)
		r.files[name] = data
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), name)
	}
	r.files["checksums.txt"] = []byte(sums.String())
	for _, script := range []string{"install.sh", "install.ps1"} {
		data, err := os.ReadFile(filepath.Join("..", "..", script))
		if err != nil {
			t.Fatal(err)
		}
		r.files[script] = data
	}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.requests = append(r.requests, req.URL.Path)
		if file, ok := strings.CutPrefix(req.URL.Path, releasePath+"/latest/download/"); ok {
			http.Redirect(w, req, releasePath+"/download/"+version+"/"+file, http.StatusFound)
			return
		}
		if file, ok := strings.CutPrefix(req.URL.Path, releasePath+"/download/"+version+"/"); ok {
			http.Redirect(w, req, "/assets/"+file, http.StatusFound)
			return
		}
		data, ok := r.files[strings.TrimPrefix(req.URL.Path, "/assets/")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *scriptRelease) replace(name string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[name] = data
}

// requested reports whether a path with prefix was requested since the last
// call, and forgets the requests.
func (r *scriptRelease) requested(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	requests := r.requests
	r.requests = nil
	for _, path := range requests {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// runScript runs a one-line install against the release, with the release as
// EDKA_RELEASES_URL and dir as EDKA_INSTALL_DIR.
func runScript(t *testing.T, r *scriptRelease, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(append(os.Environ(), "EDKA_RELEASES_URL="+r.URL+releasePath, "EDKA_INSTALL_DIR="+dir), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is for macOS and Linux")
	}
	for _, tool := range []string{"sh", "curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	binary := []byte("#!/bin/sh\necho edka v1.3.0\n")
	r := publishScripts(t, "v1.3.0", binary)
	dir := filepath.Join(t.TempDir(), "bin")
	oneLiner := "curl -fsSL " + r.URL + releasePath + "/latest/download/install.sh | sh"

	out, err := runScript(t, r, dir, nil, "sh", "-c", oneLiner)
	if err != nil || !strings.Contains(out, "Installed edka v1.3.0 in "+dir) || !strings.Contains(out, dir+" is not on your PATH") {
		t.Fatalf("%v\n%s", err, out)
	}
	installed := filepath.Join(dir, "edka")
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("%v %v", info, err)
	}
	if data, _ := os.ReadFile(installed); string(data) != string(binary) {
		t.Fatalf("%q", data)
	}

	// A pinned version reads no latest release, and a directory on PATH needs
	// no advice.
	r.requested("")
	out, err = runScript(t, r, dir, []string{"EDKA_VERSION=v1.3.0", "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}, "sh", filepath.Join("..", "..", "install.sh"))
	if err != nil || strings.Contains(out, "not on your PATH") || r.requested(releasePath+"/latest/") {
		t.Fatalf("%v\n%s", err, out)
	}

	// An archive that doesn't match checksums.txt installs nothing.
	if err := os.WriteFile(installed, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.replace(fmt.Sprintf("edka_v1.3.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), []byte("tampered"))
	out, err = runScript(t, r, dir, nil, "sh", "-c", oneLiner)
	if err == nil || !strings.Contains(out, "does not match its SHA-256 in checksums.txt. Nothing was installed.") {
		t.Fatalf("%v\n%s", err, out)
	}
	if data, _ := os.ReadFile(installed); string(data) != "old binary" {
		t.Fatalf("%q", data)
	}
}

// The one-liner of install.ps1 runs in Windows PowerShell and in PowerShell 7,
// which read a file GitHub serves as application/octet-stream. CI starts the
// test from PowerShell 7, so Windows PowerShell inherits its module path, as
// it does when a user starts it from a PowerShell 7 terminal.
func TestInstallScriptOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 is for Windows")
	}
	ran := false
	for _, shell := range []string{"powershell", "pwsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		ran = true
		t.Run(shell, func(t *testing.T) { testInstallPowerShell(t, shell) })
	}
	if !ran {
		t.Skip("PowerShell is not installed")
	}
}

func testInstallPowerShell(t *testing.T, shell string) {
	binary := []byte("edka.exe of v1.3.0")
	r := publishScripts(t, "v1.3.0", binary)
	dir := filepath.Join(t.TempDir(), "edka")
	oneLiner := "irm " + r.URL + releasePath + "/latest/download/install.ps1 | iex"
	run := func(env ...string) (string, error) {
		return runScript(t, r, dir, env, shell, "-NoProfile", "-NonInteractive", "-Command", oneLiner)
	}

	out, err := run()
	if err != nil || !strings.Contains(out, "Installed edka v1.3.0 in "+dir) || !strings.Contains(out, dir+" is not on your PATH") {
		t.Fatalf("%v\n%s", err, out)
	}
	installed := filepath.Join(dir, "edka.exe")
	if data, _ := os.ReadFile(installed); string(data) != string(binary) {
		t.Fatalf("%q", data)
	}

	// The binary in place is set aside, as `edka upgrade` does, and a pinned
	// version reads no latest release.
	r.requested("")
	out, err = run("EDKA_VERSION=v1.3.0")
	if err != nil || r.requested(releasePath+"/latest/download/checksums.txt") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(installed + ".old"); err != nil {
		t.Fatal(err)
	}

	// An archive that doesn't match checksums.txt installs nothing.
	if err := os.WriteFile(installed, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.replace(fmt.Sprintf("edka_v1.3.0_windows_%s.zip", runtime.GOARCH), []byte("tampered"))
	out, err = run()
	if err == nil || !strings.Contains(out, "does not match its SHA-256 in checksums.txt. Nothing was installed.") {
		t.Fatalf("%v\n%s", err, out)
	}
	if data, _ := os.ReadFile(installed); string(data) != "old binary" {
		t.Fatalf("%q", data)
	}
}
