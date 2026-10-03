package cli

import (
	"encoding/json"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/edkadigital/cli/internal/catalog"
)

func TestBuildDetailsComeFromTheCheckoutAndTheCatalog(t *testing.T) {
	c := &catalog.Catalog{SourceSHA: "21a0a3d0815c3d01709bef9e9dcf3182efa5f02f", Operations: make([]catalog.Operation, 434)}
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "b6d3ce75e23d4b35783b5f5fa03e46a747cbc6c1"},
		{Key: "vcs.time", Value: "2026-10-02T13:54:39Z"},
		{Key: "vcs.modified", Value: "true"},
	}}
	details := buildOf("v1.2.3", info, c)
	if details.Version != "v1.2.3" || details.Commit != "b6d3ce75e23d4b35783b5f5fa03e46a747cbc6c1" || details.CommitTime != "2026-10-02T13:54:39Z" || !details.Modified {
		t.Fatalf("%+v", details)
	}
	if details.Go != runtime.Version() || details.OS != runtime.GOOS || details.Arch != runtime.GOARCH {
		t.Fatalf("%+v", details)
	}
	if details.API != (apiDetails{Commit: "21a0a3d0815c3d01709bef9e9dcf3182efa5f02f", Endpoints: 434}) {
		t.Fatalf("%+v", details.API)
	}
	fields := details.fields()
	// The commit time prints in local time, so only its date part is fixed.
	if commit := fields[0][1]; !strings.HasPrefix(commit, "b6d3ce7 from 2026-10-0") || !strings.HasSuffix(commit, ", with local changes") {
		t.Fatal(commit)
	}
	if fields[2] != [2]string{"API", "434 endpoints at 21a0a3d08"} {
		t.Fatal(fields[2])
	}

	// A clean checkout has no note, and a build outside a checkout no commit.
	info.Settings[3].Value = "false"
	if commit := buildOf("v1.2.3", info, c).fields()[0][1]; strings.Contains(commit, "local changes") {
		t.Fatal(commit)
	}
	for _, info := range []*debug.BuildInfo{nil, {}} {
		details := buildOf("dev", info, nil)
		if details.Commit != "" || details.Modified || details.fields()[0][1] != "" || details.fields()[2][1] != "" {
			t.Fatalf("%+v", details)
		}
	}
}

func TestVersionCommandNamesTheAPIRevision(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := execute(t, "http://127.0.0.1:1", "version")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// The first line stays what it was, for whatever reads it.
	if lines[0] != "edka test" || !strings.Contains(squeeze(out), "Go "+runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH) || !strings.Contains(squeeze(out), "endpoints at "+c.SourceSHA[:9]) {
		t.Fatal(out)
	}
	out, _, err = execute(t, "http://127.0.0.1:1", "version", "--json")
	var details buildDetails
	if err != nil || json.Unmarshal([]byte(out), &details) != nil {
		t.Fatal(out, err)
	}
	if details.Version != "test" || details.Go != runtime.Version() || details.API.Commit != c.SourceSHA || details.API.Endpoints != len(c.Operations) {
		t.Fatalf("%+v", details)
	}
}
