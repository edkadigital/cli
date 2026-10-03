package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// site answers as github.com does for a repository: releases/latest
// redirects to latest, and a file of a release to the host of assets.
func site(t *testing.T, latest string, assets map[string][]byte) Channel {
	t.Helper()
	files := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := assets[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(files.Close)
	repository := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, download := strings.CutPrefix(r.URL.Path, "/edkadigital/cli/releases/download/v1.3.0/")
		switch {
		case r.URL.Path == "/edkadigital/cli/releases/latest" && latest != "":
			// http.Redirect would clean the path.
			w.Header().Set("Location", latest)
			w.WriteHeader(http.StatusFound)
		case download && strings.HasPrefix(file, "elsewhere-"):
			http.Redirect(w, r, "https://downloads.example/"+file, http.StatusFound)
		case download:
			http.Redirect(w, r, files.URL+"/"+file, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(repository.Close)
	return Channel{URL: repository.URL + "/edkadigital/cli", Hosts: []string{"127.0.0.1"}, Transport: repository.Client().Transport}
}

// tarball is the archive release.yml builds for every system but Windows.
func tarball(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var data bytes.Buffer
	compressed := gzip.NewWriter(&data)
	archive := tar.NewWriter(compressed)
	for _, file := range files {
		if err := archive.WriteHeader(&tar.Header{Name: file[0], Mode: 0o755, Size: int64(len(file[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(file[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

// installed writes the binary an upgrade replaces and returns its path.
func installed(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "edka")
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// only fails unless the directory of the binary holds the binary alone, with
// the content want.
func only(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("binary %q: %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("%v: %v", entries, err)
	}
}

func TestVersionsCompareByNumber(t *testing.T) {
	for _, test := range []struct {
		latest, current string
		newer           bool
	}{
		{"v1.3.0", "v1.2.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.3.0", "1.2.0", true},
		{"v1.3.0", "v1.3.0", false},
		{"v1.3.0", "v1.4.0", false},
		// A pre-release comes before its release, and after the release before it.
		{"v1.3.0", "v1.3.0-rc.1", true},
		{"v1.3.0", "v1.4.0-rc.1", false},
		{"v1.3.0", "v1.3.0+build.5", false},
		// A build from a checkout is no release.
		{"v1.3.0", "dev", false},
		{"v1.3.0", "", false},
		{"v1.3", "v1.2.0", false},
		{"v1.3.x", "v1.2.0", false},
	} {
		if got := Newer(test.latest, test.current); got != test.newer {
			t.Errorf("Newer(%q, %q) = %v", test.latest, test.current, got)
		}
	}
	for version, released := range map[string]bool{"v1.2.3": true, "1.2.3": true, "v1.2.3-rc.1": true, "dev": false, "test": false, "v1.2": false, "v1.-2.3": false} {
		if Released(version) != released {
			t.Errorf("Released(%q) = %v", version, !released)
		}
	}
}

func TestLatestReadsTheTagGitHubRedirectsTo(t *testing.T) {
	latest, err := site(t, "/edkadigital/cli/releases/tag/v1.3.0", nil).Latest(context.Background())
	if err != nil || latest != "v1.3.0" {
		t.Fatalf("%q: %v", latest, err)
	}
	for name, test := range map[string]struct{ location, want string }{
		// GitHub answers 404 for a private repository.
		"no repository": {"", "no release found at 127.0.0.1"},
		// A repository without a release redirects to its list of releases.
		"no release":      {"/edkadigital/cli/releases", "has no release yet"},
		"another host":    {"https://github.example/edkadigital/cli/releases/tag/v1.3.0", "has no release yet"},
		"another project": {"/someone/else/releases/tag/v1.3.0", "has no release yet"},
		"a pre-release":   {"/edkadigital/cli/releases/tag/v1.3.0-rc.1", `is "v1.3.0-rc.1", which is not a version`},
		"a path":          {"/edkadigital/cli/releases/tag/v1.3.0/assets", "which is not a version"},
		"a way out":       {"/edkadigital/cli/releases/tag/v1.3.0/../../x", "has no release yet"},
		"an escape":       {"/edkadigital/cli/releases/tag/v1.3.0%1b[2J", "which is not a version"},
	} {
		latest, err := site(t, test.location, nil).Latest(context.Background())
		if err == nil || latest != "" || !strings.Contains(err.Error(), test.want) || strings.ContainsRune(err.Error(), 0x1b) {
			t.Errorf("%s: %q: %v", name, latest, err)
		}
	}
	if _, err := (Channel{URL: "http://github.com/edkadigital/cli"}).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTPS only") {
		t.Fatal(err)
	}
}

func TestInstallReplacesTheBinaryWithTheReleasedOne(t *testing.T) {
	archive := tarball(t, [2]string{"README.md", "# Edka CLI\n"}, [2]string{"edka", "new binary"})
	channel := site(t, "", map[string][]byte{
		"edka_v1.3.0_linux_arm64.tar.gz": archive,
		// sha256sum lists the archives of every platform.
		"checksums.txt": fmt.Appendf(nil, "%064x  edka_v1.3.0_darwin_arm64.tar.gz\n%x  edka_v1.3.0_linux_arm64.tar.gz\n", 1, sha256.Sum256(archive)),
	})
	path := installed(t)
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := channel.install(context.Background(), "v1.3.0", path, "linux", "arm64"); err != nil {
		t.Fatal(err)
	}
	only(t, path, "new binary")
	// The new binary keeps the permissions of the old one.
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o750 {
		t.Fatalf("%v: %v", info.Mode(), err)
	}
}

// Windows gets a zip, and the old binary moves aside while it runs.
func TestInstallUnpacksAZipForWindows(t *testing.T) {
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for _, file := range [][2]string{{"README.md", "# Edka CLI\n"}, {"edka.exe", "new binary"}} {
		entry, err := archive.Create(file[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(file[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	const name = "edka_v1.3.0_windows_amd64.zip"
	channel := site(t, "", map[string][]byte{name: data.Bytes(), "checksums.txt": fmt.Appendf(nil, "%x *%s\n", sha256.Sum256(data.Bytes()), name)})
	path := filepath.Join(t.TempDir(), "edka.exe")
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// What the upgrade before this one left behind.
	if err := os.WriteFile(path+".old", []byte("older binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := channel.install(context.Background(), "v1.3.0", path, "windows", "amd64"); err != nil {
		t.Fatal(err)
	}
	only(t, path, "new binary")
}

func TestInstallReplacesNothingItCannotVerify(t *testing.T) {
	good := tarball(t, [2]string{"edka", "new binary"})
	sum := func(data []byte, name string) []byte { return fmt.Appendf(nil, "%x  %s\n", sha256.Sum256(data), name) }
	const name = "edka_v1.3.0_linux_arm64.tar.gz"
	for title, test := range map[string]struct {
		assets map[string][]byte
		want   string
	}{
		"a changed archive":    {map[string][]byte{name: tarball(t, [2]string{"edka", "another binary"}), "checksums.txt": sum(good, name)}, "does not match its SHA-256"},
		"no checksums":         {map[string][]byte{name: good}, "download checksums.txt of v1.3.0: HTTP 404"},
		"no checksum of it":    {map[string][]byte{name: good, "checksums.txt": sum(good, "edka_v1.3.0_linux_amd64.tar.gz")}, "was not built for this platform"},
		"a short checksum":     {map[string][]byte{name: good, "checksums.txt": []byte("abc123  " + name + "\n")}, "has no SHA-256 for"},
		"no archive":           {map[string][]byte{"checksums.txt": sum(good, name)}, "download " + name + ": HTTP 404"},
		"an archive of text":   {map[string][]byte{name: []byte("not an archive"), "checksums.txt": sum([]byte("not an archive"), name)}, "unpack " + name},
		"no binary in it":      {map[string][]byte{name: tarball(t, [2]string{"README.md", "x"}), "checksums.txt": sum(tarball(t, [2]string{"README.md", "x"}), name)}, "it holds no edka"},
		"a binary in a folder": {map[string][]byte{name: tarball(t, [2]string{"bin/edka", "x"}), "checksums.txt": sum(tarball(t, [2]string{"bin/edka", "x"}), name)}, "it holds no edka"},
		"an empty binary":      {map[string][]byte{name: tarball(t, [2]string{"edka", ""}), "checksums.txt": sum(tarball(t, [2]string{"edka", ""}), name)}, "is empty"},
	} {
		path := installed(t)
		err := site(t, "", test.assets).install(context.Background(), "v1.3.0", path, "linux", "arm64")
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: %v", title, err)
		}
		only(t, path, "old binary")
	}
	path := installed(t)
	// A version is part of the URL and of the file name.
	for _, version := range []string{"", "latest", "v1.3.0-rc.1", "v1.3.0/../v1.2.0", "1.3.0"} {
		if err := site(t, "", nil).install(context.Background(), version, path, "linux", "arm64"); err == nil || !strings.Contains(err.Error(), "is not a version") {
			t.Errorf("%q: %v", version, err)
		}
	}
	only(t, path, "old binary")
}

// A download follows GitHub's redirect to its file host, and no other.
func TestDownloadsFollowRedirectsToTheChannelsHostsOnly(t *testing.T) {
	archive := tarball(t, [2]string{"edka", "new binary"})
	assets := map[string][]byte{
		"edka_v1.3.0_linux_arm64.tar.gz": archive,
		"checksums.txt":                  fmt.Appendf(nil, "%x  edka_v1.3.0_linux_arm64.tar.gz\n", sha256.Sum256(archive)),
	}
	channel := site(t, "", assets)
	path := installed(t)
	// The files are on another port of 127.0.0.1, which the channel names.
	channel.Hosts = []string{"files.example"}
	if err := channel.install(context.Background(), "v1.3.0", path, "linux", "arm64"); err == nil || !strings.Contains(err.Error(), "refusing a redirect to 127.0.0.1") {
		t.Fatal(err)
	}
	only(t, path, "old binary")
	channel.Hosts = []string{"127.0.0.1"}
	resp, err := channel.request(context.Background(), channel.URL+"/releases/download/v1.3.0/elsewhere-edka.tar.gz", true)
	if err == nil || !strings.Contains(err.Error(), "refusing a redirect to downloads.example") {
		t.Fatal(resp, err)
	}
	for address, allowed := range map[string]bool{
		"https://github.com/edkadigital/cli/releases/download/v1.3.0/checksums.txt": true,
		"https://release-assets.githubusercontent.com/x":                            true,
		"https://objects.githubusercontent.com/x":                                   true,
		"https://GitHub.com/x":                                                      true,
		"http://github.com/x":                                                       false,
		"https://user@github.com/x":                                                 false,
		"https://githubusercontent.com.example/x":                                   false,
		"https://evilgithubusercontent.com/x":                                       false,
		"https://github.com.example/x":                                              false,
		"https://example.com/github.com":                                            false,
	} {
		req, err := http.NewRequest(http.MethodGet, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		if GitHub.allowed(req.URL) != allowed {
			t.Errorf("%s: allowed is %v", address, !allowed)
		}
	}
}

// Windows keeps a running program at its path until the upgrade moves it
// aside, and a failed upgrade puts it back.
func TestSwapMovesTheOldBinaryAsideAndBack(t *testing.T) {
	path := installed(t)
	if err := swap(filepath.Join(filepath.Dir(path), "missing"), path, true); err == nil {
		t.Fatal("swapped in a missing file")
	}
	only(t, path, "old binary")
}

// The upgrade downloads what the release workflow uploads.
func TestReleaseWorkflowBuildsTheArchivesAnUpgradeDownloads(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	for _, want := range []string{
		`archive="edka_${GITHUB_REF_NAME}_${GOOS}_${GOARCH}"`,
		`zip "../$archive.zip" "$binary" README.md`,
		`tar -czf "dist/$archive.tar.gz" -C dist/package "$binary" README.md`,
		"sha256sum *.tar.gz *.zip > checksums.txt",
		// GitHub's latest release leaves pre-releases out.
		"--prerelease",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release.yml has no %s", want)
		}
	}
}

func TestHomebrewKnowsItsCellar(t *testing.T) {
	for path, want := range map[string]bool{
		"/opt/homebrew/Cellar/edka/1.3.0/bin/edka":              true,
		"/usr/local/Cellar/edka/1.3.0/bin/edka":                 true,
		"/home/linuxbrew/.linuxbrew/Cellar/edka/1.3.0/bin/edka": true,
		"/usr/local/bin/edka":                                   false,
		"/home/me/.local/bin/edka":                              false,
		"/opt/homebrew/Cellar/kubectl/1.33.0/bin/kubectl":       false,
		`C:\Users\me\AppData\Local\Programs\edka\edka.exe`:      false,
	} {
		if got := Homebrew(path); got != want {
			t.Errorf("%s: %v", path, got)
		}
	}
}
