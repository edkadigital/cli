// Package selfupdate finds the latest release of the CLI and replaces the
// running binary with it.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	maxChecksums = 1 << 20
	maxArchive   = 256 << 20
	maxBinary    = 512 << 20
	maxRedirects = 5
)

// Channel is where releases are published: a GitHub repository, whose release
// workflow uploads an archive for each platform beside checksums.txt.
type Channel struct {
	// URL is the repository, such as https://github.com/edkadigital/cli.
	URL string
	// Hosts are the hosts a download may be redirected to. A name that starts
	// with a dot stands for every host below it.
	Hosts []string
	// Transport sends the requests, http.DefaultTransport when nil.
	Transport http.RoundTripper
}

// GitHub is the release channel of the CLI. GitHub serves a release's files
// from a host of githubusercontent.com.
var GitHub = Channel{URL: "https://github.com/edkadigital/cli", Hosts: []string{"github.com", ".githubusercontent.com"}}

// stable is the tag of a release that `releases/latest` may name. The tag
// becomes part of a URL and of a file name, so it holds nothing else.
var stable = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// parse reads a version such as v1.2.3 or v1.2.3-rc.1. pre reports a
// pre-release, which comes before the release with the same numbers.
func parse(version string) (numbers [3]uint64, pre, ok bool) {
	version, _, _ = strings.Cut(strings.TrimPrefix(version, "v"), "+")
	version, _, pre = strings.Cut(version, "-")
	parts := strings.Split(version, ".")
	if len(parts) != len(numbers) {
		return numbers, false, false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return numbers, false, false
		}
		numbers[i] = n
	}
	return numbers, pre, true
}

// Released reports whether version names a release. A binary built from a
// checkout is `dev`, and no release replaces it.
func Released(version string) bool {
	_, _, ok := parse(version)
	return ok
}

// Newer reports whether latest is a later release than current.
func Newer(latest, current string) bool {
	l, lpre, lok := parse(latest)
	c, cpre, cok := parse(current)
	if !lok || !cok {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return cpre && !lpre
}

// Executable is the file of the running binary, behind any symbolic link.
func Executable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// Homebrew reports whether executable is a binary Homebrew installed, which
// `brew upgrade` replaces. Homebrew keeps each version of a formula under its
// Cellar and links the binary from its prefix, so the path behind the link
// names the Cellar.
func Homebrew(executable string) bool {
	return strings.Contains(filepath.ToSlash(executable), "/Cellar/edka/")
}

// name is the channel as a message names it, such as github.com/edkadigital/cli.
func (c Channel) name() string {
	u, err := url.Parse(c.URL)
	if err != nil {
		return c.URL
	}
	return u.Host + u.Path
}

// allowed reports whether a download may be read from u.
func (c Channel) allowed(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, name := range c.Hosts {
		if host == name || strings.HasPrefix(name, ".") && strings.HasSuffix(host, name) {
			return true
		}
	}
	return false
}

// request sends a GET without credentials. follow says whether it follows a
// redirect, and then only over HTTPS to one of the channel's hosts.
func (c Channel) request(ctx context.Context, address string, follow bool) (*http.Response, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("releases are read over HTTPS only")
	}
	client := &http.Client{Transport: c.Transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		if !c.allowed(next.URL) {
			return fmt.Errorf("refusing a redirect to %s", next.URL.Host)
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// Latest returns the tag of the latest release. GitHub answers
// releases/latest with a redirect to that release, and leaves pre-releases
// and drafts out.
func (c Channel) Latest(ctx context.Context) (string, error) {
	base, err := url.Parse(c.URL)
	if err != nil {
		return "", err
	}
	resp, err := c.request(ctx, c.URL+"/releases/latest", false)
	if err != nil {
		return "", fmt.Errorf("read the latest release of %s: %w", c.name(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return "", fmt.Errorf("no release found at %s (HTTP %d)", c.name(), resp.StatusCode)
	}
	location, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("read the latest release of %s: %w", c.name(), err)
	}
	tag, found := strings.CutPrefix(location.Path, base.Path+"/releases/tag/")
	if location.Scheme != base.Scheme || location.Host != base.Host || !found {
		return "", fmt.Errorf("%s has no release yet", c.name())
	}
	if !stable.MatchString(tag) {
		return "", fmt.Errorf("the latest release of %s is %q, which is not a version such as v1.2.3", c.name(), tag)
	}
	return tag, nil
}

// Install replaces executable with the binary of a release, for the platform
// of the running binary. It downloads the release's archive, checks it against
// checksums.txt and unpacks the binary beside executable before it replaces
// anything.
func (c Channel) Install(ctx context.Context, version, executable string) error {
	return c.install(ctx, version, executable, runtime.GOOS, runtime.GOARCH)
}

func (c Channel) install(ctx context.Context, version, executable, goos, goarch string) error {
	if !stable.MatchString(version) {
		return fmt.Errorf("%q is not a version such as v1.2.3", version)
	}
	current, err := os.Stat(executable)
	if err != nil {
		return err
	}
	archive, binary := fmt.Sprintf("edka_%s_%s_%s.tar.gz", version, goos, goarch), "edka"
	if goos == "windows" {
		archive, binary = fmt.Sprintf("edka_%s_%s_%s.zip", version, goos, goarch), "edka.exe"
	}
	files := c.URL + "/releases/download/" + version + "/"
	want, err := c.checksum(ctx, files+"checksums.txt", version, archive)
	if err != nil {
		return err
	}
	// The new binary is written in the directory of the old one, so that the
	// rename stays within one file system.
	replacement, err := os.CreateTemp(filepath.Dir(executable), ".edka-upgrade-*")
	if err != nil {
		return fmt.Errorf("write the new binary beside %s: %w", executable, err)
	}
	defer func() {
		_ = replacement.Close()
		// After the rename there is no temporary file left to remove.
		_ = os.Remove(replacement.Name())
	}()
	download, err := os.CreateTemp("", "edka-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = download.Close()
		_ = os.Remove(download.Name())
	}()
	size, got, err := c.download(ctx, files+archive, download)
	if err != nil {
		return fmt.Errorf("download %s: %w", archive, err)
	}
	if got != want {
		return fmt.Errorf("%s does not match its SHA-256 in checksums.txt; nothing was replaced", archive)
	}
	if err := unpack(download, size, binary, goos == "windows", replacement); err != nil {
		return fmt.Errorf("unpack %s: %w", archive, err)
	}
	if err := replacement.Chmod(current.Mode().Perm()); err != nil {
		return err
	}
	if err := replacement.Sync(); err != nil {
		return err
	}
	if err := replacement.Close(); err != nil {
		return err
	}
	return swap(replacement.Name(), executable, goos == "windows")
}

// checksum returns the SHA-256 that the release's checksums.txt lists for
// archive, in the format of sha256sum.
func (c Channel) checksum(ctx context.Context, address, version, archive string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	resp, err := c.request(ctx, address, true)
	if err != nil {
		return sum, fmt.Errorf("download checksums.txt: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return sum, fmt.Errorf("download checksums.txt of %s: HTTP %d", version, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksums+1))
	if err != nil {
		return sum, fmt.Errorf("download checksums.txt: %w", err)
	}
	if len(data) > maxChecksums {
		return sum, errors.New("checksums.txt exceeds 1 MiB")
	}
	for _, line := range strings.Split(string(data), "\n") {
		// sha256sum marks a file it read in binary mode with an asterisk.
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != archive {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return sum, fmt.Errorf("checksums.txt of %s has no SHA-256 for %s", version, archive)
		}
		copy(sum[:], decoded)
		return sum, nil
	}
	return sum, fmt.Errorf("release %s has no %s; it was not built for this platform", version, archive)
}

// download saves a file of the release and returns its size and SHA-256.
func (c Channel) download(ctx context.Context, address string, to io.Writer) (int64, [sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	resp, err := c.request(ctx, address, true)
	if err != nil {
		return 0, sum, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, sum, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(to, hash), io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return 0, sum, err
	}
	if size > maxArchive {
		return 0, sum, errors.New("the archive exceeds 256 MiB")
	}
	copy(sum[:], hash.Sum(nil))
	return size, sum, nil
}

// unpack writes the file called name at the top of an archive: a zip for
// Windows, a gzipped tar otherwise.
func unpack(archive *os.File, size int64, name string, zipped bool, to io.Writer) error {
	var binary io.Reader
	if zipped {
		reader, err := zip.NewReader(archive, size)
		if err != nil {
			return err
		}
		for _, file := range reader.File {
			if file.Name != name || !file.Mode().IsRegular() {
				continue
			}
			opened, err := file.Open()
			if err != nil {
				return err
			}
			defer func() { _ = opened.Close() }()
			binary = opened
			break
		}
	} else {
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return err
		}
		compressed, err := gzip.NewReader(archive)
		if err != nil {
			return err
		}
		defer func() { _ = compressed.Close() }()
		files := tar.NewReader(compressed)
		for binary == nil {
			header, err := files.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if header.Name == name && header.Typeflag == tar.TypeReg {
				binary = files
			}
		}
	}
	if binary == nil {
		return fmt.Errorf("it holds no %s", name)
	}
	written, err := io.Copy(to, io.LimitReader(binary, maxBinary+1))
	if err != nil {
		return err
	}
	if written == 0 || written > maxBinary {
		return fmt.Errorf("its %s is empty or exceeds 512 MiB", name)
	}
	return nil
}

// swap puts the new binary at the path of the running one. Windows refuses to
// replace a running program and lets it be renamed, so there the old binary
// moves aside first. It stays as edka.exe.old until the next upgrade, since a
// running program cannot be removed either.
func swap(replacement, executable string, aside bool) error {
	if !aside {
		return os.Rename(replacement, executable)
	}
	old := executable + ".old"
	_ = os.Remove(old)
	if err := os.Rename(executable, old); err != nil {
		return err
	}
	if err := os.Rename(replacement, executable); err != nil {
		// Without this the command would be gone from its path.
		_ = os.Rename(old, executable)
		return err
	}
	_ = os.Remove(old)
	return nil
}
