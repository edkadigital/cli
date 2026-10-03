package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLinkInheritanceStopsAtGitBoundary(t *testing.T) {
	outer := t.TempDir()
	if err := WriteJSON(filepath.Join(outer, LinkName), Link{Version: 1, Cluster: "outside"}); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(repo, "src")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	link, _, err := FindLink(child)
	if err != nil || link != nil {
		t.Fatal("inherited outside repository")
	}
	if err := WriteJSON(filepath.Join(repo, LinkName), Link{Version: 1, Cluster: "inside"}); err != nil {
		t.Fatal(err)
	}
	link, _, err = FindLink(child)
	if err != nil || link.Cluster != "inside" {
		t.Fatal("did not inherit project link")
	}
}
func TestPrivateAtomicConfig(t *testing.T) {
	dir := t.TempDir()
	c, _ := Load(dir)
	c.Profiles["test"] = Profile{APIURL: "https://api.edka.io"}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	reloaded, err := Load(dir)
	if err != nil || reloaded.Profiles["test"].APIURL == "" {
		t.Fatal("config did not round-trip")
	}
}
