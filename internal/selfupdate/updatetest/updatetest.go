// Package updatetest publishes releases on a local server that answers as
// GitHub does, for tests of the upgrade.
package updatetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/edkadigital/cli/internal/selfupdate"
)

const repository = "/edkadigital/cli"

// Release is the latest release of a test channel.
type Release struct {
	// Channel reads the release. Its transport trusts the test servers.
	Channel selfupdate.Channel
	version string
	site    *httptest.Server
	mu      sync.Mutex
	files   map[string][]byte
	latest  string
	paths   []string
}

// Publish serves a release whose archives hold binary, as release.yml builds
// them: one for the platform of the test, one for Windows and one for Linux.
func Publish(t testing.TB, version string, binary []byte) *Release {
	t.Helper()
	r := &Release{version: version, files: map[string][]byte{}, latest: repository + "/releases/tag/" + version}
	var sums strings.Builder
	for _, platform := range [][2]string{{runtime.GOOS, runtime.GOARCH}, {"windows", "amd64"}, {"linux", "amd64"}} {
		name, data := Archive(t, version, platform[0], platform[1], binary)
		if r.files[name] == nil {
			fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), name)
		}
		r.files[name] = data
	}
	r.files["checksums.txt"] = []byte(sums.String())
	// GitHub redirects a release's files to another host.
	assets := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		data, ok := r.files[strings.TrimPrefix(req.URL.Path, "/")]
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(assets.Close)
	r.site = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		latest := r.latest
		r.mu.Unlock()
		file, download := strings.CutPrefix(req.URL.Path, repository+"/releases/download/"+version+"/")
		switch {
		case req.URL.Path == repository+"/releases/latest" && latest != "":
			http.Redirect(w, req, latest, http.StatusFound)
		case download:
			http.Redirect(w, req, assets.URL+"/"+file, http.StatusFound)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.site.Close)
	r.Channel = selfupdate.Channel{URL: r.site.URL + repository, Hosts: []string{"127.0.0.1"}, Transport: r.site.Client().Transport}
	return r
}

// Archive builds the archive of a platform and returns it with its name.
func Archive(t testing.TB, version, goos, goarch string, binary []byte) (string, []byte) {
	t.Helper()
	var data bytes.Buffer
	files := [][2]string{{"edka", string(binary)}, {"README.md", "# Edka CLI\n"}}
	if goos == "windows" {
		files[0][0] = "edka.exe"
		archive := zip.NewWriter(&data)
		for _, file := range files {
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
		return fmt.Sprintf("edka_%s_%s_%s.zip", version, goos, goarch), data.Bytes()
	}
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
	return fmt.Sprintf("edka_%s_%s_%s.tar.gz", version, goos, goarch), data.Bytes()
}

// Name is the archive of the release for the platform of the test.
func (r *Release) Name() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("edka_%s_%s_%s.zip", r.version, runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("edka_%s_%s_%s.tar.gz", r.version, runtime.GOOS, runtime.GOARCH)
}

// File returns a file of the release.
func (r *Release) File(name string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.files[name]
}

// Replace changes a file of the release, and removes it when data is nil.
func (r *Release) Replace(name string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if data == nil {
		delete(r.files, name)
		return
	}
	r.files[name] = data
}

// RedirectLatest makes releases/latest redirect to location, and answer 404
// when location is empty, as GitHub does for a private repository.
func (r *Release) RedirectLatest(location string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latest = location
}

// Requests are the paths requested from the repository so far.
func (r *Release) Requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// CA is the certificate of the test servers, for a process that reads the
// release without the channel's transport.
func (r *Release) CA() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.site.Certificate().Raw})
}

// Trusting returns a transport that trusts the certificate CA returned.
func Trusting(ca []byte) http.RoundTripper {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
}
