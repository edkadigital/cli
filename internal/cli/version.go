package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/edkadigital/cli/internal/catalog"
)

// buildDetails says what a binary is, for a bug report: its version, the commit
// it was built from, its Go release, and the revision of the Edka API that the
// endpoint commands were generated from.
type buildDetails struct {
	Version string `json:"version"`
	// Commit and CommitTime come from the Git checkout the binary was built
	// in. A binary built outside one has neither.
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	// Modified reports a build from a checkout with uncommitted changes.
	Modified bool       `json:"modified"`
	Go       string     `json:"go"`
	OS       string     `json:"os"`
	Arch     string     `json:"arch"`
	API      apiDetails `json:"api"`
}

type apiDetails struct {
	Commit    string `json:"commit"`
	Endpoints int    `json:"endpoints"`
}

// buildOf reads the details of the running binary. info is what the Go
// toolchain recorded at build time, or nil when it recorded nothing.
func buildOf(version string, info *debug.BuildInfo, c *catalog.Catalog) buildDetails {
	details := buildDetails{Version: version, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if c != nil {
		details.API = apiDetails{Commit: c.SourceSHA, Endpoints: len(c.Operations)}
	}
	if info == nil {
		return details
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			details.Commit = setting.Value
		case "vcs.time":
			details.CommitTime = setting.Value
		case "vcs.modified":
			details.Modified = setting.Value == "true"
		}
	}
	return details
}

// short is the first characters of a commit, as `git log --oneline` shows it.
func short(commit string, length int) string {
	if len(commit) > length {
		return commit[:length]
	}
	return commit
}

// fields are the details as `edka version` lists them under the version.
func (d buildDetails) fields() [][2]string {
	commit := short(d.Commit, 7)
	if commit != "" {
		if committed := when(d.CommitTime); committed != "" {
			commit += " from " + committed
		}
		if d.Modified {
			commit += ", with local changes"
		}
	}
	api := ""
	if d.API.Endpoints > 0 {
		api = fmt.Sprintf("%d endpoints at %s", d.API.Endpoints, short(d.API.Commit, 9))
	}
	return [][2]string{
		{"Commit", commit},
		{"Go", d.Go + " " + d.OS + "/" + d.Arch},
		{"API", api},
	}
}
