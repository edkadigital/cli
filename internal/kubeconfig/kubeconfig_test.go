package kubeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// downloaded is shaped like the kubeconfig Edka's backend issues for a
// cluster with two API endpoints.
const downloaded = `apiVersion: v1
kind: Config
clusters:
  - name: production
    cluster:
      server: https://1.1.1.1:6443
      certificate-authority-data: ZHVtbXk=
  - name: production-2-2-2-2
    cluster:
      server: https://2.2.2.2:6443
      certificate-authority-data: ZHVtbXk=
contexts:
  - name: production-user
    context:
      cluster: production
      user: edka-user-0f8fad5b
  - name: production-user-2-2-2-2
    context:
      cluster: production-2-2-2-2
      user: edka-user-0f8fad5b
current-context: production-user
users:
  - name: edka-user-0f8fad5b
    user:
      token: secret-token
`

const existing = `apiVersion: v1
kind: Config
# Keep this comment.
clusters:
  - name: minikube
    cluster:
      server: https://127.0.0.1:8443
contexts:
  - name: minikube
    context:
      cluster: minikube
      user: minikube
current-context: minikube
users:
  - name: minikube
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        command: minikube-credentials
preferences: {}
`

type extension struct {
	Name      string
	Extension map[string]string
}

type cluster struct {
	Name    string
	Cluster struct {
		Server     string
		Extensions []extension
	}
}

type context struct {
	Name    string
	Context struct{ Cluster, User string }
}

type user struct {
	Name string
	User map[string]any
}

type file struct {
	Clusters       []cluster
	Contexts       []context
	Users          []user
	CurrentContext string `yaml:"current-context"`
}

func read(t *testing.T, data []byte) file {
	t.Helper()
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatal(err, string(data))
	}
	return f
}

func clusterNames(f file) string {
	var out []string
	for _, c := range f.Clusters {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func contextNames(f file) string {
	var out []string
	for _, c := range f.Contexts {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

var options = Options{Name: "edka-acme-production", ClusterName: "production", ClusterID: "c1", OrganizationID: "o1"}

func TestName(t *testing.T) {
	for _, tc := range []struct{ organization, cluster, want string }{
		{"Acme Inc.", "production", "edka-acme-inc-production"},
		{"", "staging", "edka-staging"},
		{"Ünïcode Org", "EU_West", "edka-n-code-org-eu-west"},
	} {
		if got := Name(tc.organization, tc.cluster); got != tc.want {
			t.Errorf("Name(%q, %q) = %q, want %q", tc.organization, tc.cluster, got, tc.want)
		}
	}
}

func TestMergeIntoEmptyFile(t *testing.T) {
	out, contexts, err := Merge(nil, []byte(downloaded), options)
	if err != nil {
		t.Fatal(err)
	}
	f := read(t, out)
	if got := strings.Join(contexts, ","); got != "edka-acme-production,edka-acme-production-2-2-2-2" {
		t.Fatal(got)
	}
	if got := clusterNames(f); got != "edka-acme-production,edka-acme-production-2-2-2-2" {
		t.Fatal(got)
	}
	second := f.Contexts[1].Context
	if second.Cluster != "edka-acme-production-2-2-2-2" || second.User != "edka-acme-production" {
		t.Fatalf("%+v", second)
	}
	if f.Users[0].Name != "edka-acme-production" || f.Users[0].User["token"] != "secret-token" {
		t.Fatalf("%+v", f.Users)
	}
	marker := f.Clusters[0].Cluster.Extensions[0]
	if marker.Name != "edka.io" || marker.Extension["cluster-id"] != "c1" || marker.Extension["organization-id"] != "o1" {
		t.Fatalf("%+v", marker)
	}
	if f.CurrentContext != "edka-acme-production" {
		t.Fatal("a new file should use the merged context:", f.CurrentContext)
	}
	if !strings.Contains(string(out), "apiVersion: v1") || !strings.Contains(string(out), "kind: Config") {
		t.Fatal(string(out))
	}
}

func TestMergeKeepsTheRestOfTheFile(t *testing.T) {
	out, _, err := Merge([]byte(existing), []byte(downloaded), options)
	if err != nil {
		t.Fatal(err)
	}
	f := read(t, out)
	if f.CurrentContext != "minikube" {
		t.Fatal("changed the current context without --use:", f.CurrentContext)
	}
	if f.Contexts[0].Name != "minikube" || len(f.Contexts) != 3 || len(f.Users) != 2 {
		t.Fatalf("%+v", f)
	}
	text := string(out)
	for _, kept := range []string{"# Keep this comment.", "command: minikube-credentials", "preferences: {}"} {
		if !strings.Contains(text, kept) {
			t.Errorf("lost %q:\n%s", kept, text)
		}
	}

	used, _, err := Merge([]byte(existing), []byte(downloaded), Options{Name: options.Name, ClusterName: "production", ClusterID: "c1", Use: true})
	if err != nil || read(t, used).CurrentContext != "edka-acme-production" {
		t.Fatal("--use should switch the current context", err)
	}
}

func TestMergeAgainReplacesOnlyThisClustersEntries(t *testing.T) {
	first, _, err := Merge([]byte(existing), []byte(downloaded), options)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := Merge(first, []byte(strings.ReplaceAll(downloaded, "production", "staging")), Options{Name: "edka-acme-staging", ClusterName: "staging", ClusterID: "c2"})
	if err != nil {
		t.Fatal(err)
	}
	// The cluster now has one endpoint and a new token.
	single := `apiVersion: v1
kind: Config
clusters:
  - name: production
    cluster:
      server: https://3.3.3.3:6443
contexts:
  - name: production-user
    context:
      cluster: production
      user: edka-user-0f8fad5b
current-context: production-user
users:
  - name: edka-user-0f8fad5b
    user:
      token: rotated-token
`
	again, _, err := Merge(other, []byte(single), options)
	if err != nil {
		t.Fatal(err)
	}
	f := read(t, again)
	if got := contextNames(f); got != "minikube,edka-acme-staging,edka-acme-staging-2-2-2-2,edka-acme-production" {
		t.Fatal(got)
	}
	// The old token now only appears in staging's user entry.
	if !strings.Contains(string(again), "secret-token") || strings.Count(string(again), "rotated-token") != 1 {
		t.Fatal(string(again))
	}
	for _, user := range f.Users {
		if user.Name == "edka-acme-production" && user.User["token"] != "rotated-token" {
			t.Fatalf("%+v", user)
		}
	}
}

func TestMergeRefusesAnEntryEdkaDidNotWrite(t *testing.T) {
	foreign := existing + "\n"
	foreign = strings.Replace(foreign, "  - name: minikube\n    context:", "  - name: edka-acme-production\n    context:", 1)
	var conflict *ConflictError
	if err := Check([]byte(foreign), "edka-acme-production"); !errors.As(err, &conflict) || conflict.Section != "contexts" {
		t.Fatal(err)
	}
	if _, _, err := Merge([]byte(foreign), []byte(downloaded), options); !errors.As(err, &conflict) {
		t.Fatal(err)
	}
	if err := Check([]byte(existing), "edka-acme-production"); err != nil {
		t.Fatal(err)
	}
	if err := Check([]byte("clusters: [\n"), "edka-acme-production"); err == nil {
		t.Fatal("accepted invalid YAML")
	}
	if _, _, err := Merge(nil, []byte(downloaded), Options{Name: "edka-x"}); err == nil {
		t.Fatal("merged without a cluster ID, which would match every foreign entry")
	}
}

func TestDefaultPath(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	t.Setenv("KUBECONFIG", strings.Join([]string{first, second}, string(os.PathListSeparator)))
	if got, _ := DefaultPath(); got != second {
		t.Fatal("without an existing file, kubectl writes to the last one:", got)
	}
	if err := os.WriteFile(second, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, _ := DefaultPath(); got != first {
		t.Fatal("kubectl writes to the first existing file:", got)
	}
	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if got, _ := DefaultPath(); got != filepath.Join(dir, ".kube", "config") {
		t.Fatal(got)
	}
}

func TestUpdateLocksAndKeepsABackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	backup, err := Update(path, func(existing []byte) ([]byte, error) {
		if existing != nil {
			t.Fatal("read a file that doesn't exist yet")
		}
		return []byte("first\n"), nil
	})
	if err != nil || backup != "" {
		t.Fatal(backup, err)
	}
	backup, err = Update(path, func(existing []byte) ([]byte, error) { return []byte("second\n"), nil })
	// Update resolves symlinks, such as macOS's /var → /private/var.
	resolved, _ := filepath.EvalSymlinks(path)
	if err != nil || backup != resolved+".edka-backup" {
		t.Fatal(backup, err)
	}
	if data, _ := os.ReadFile(backup); string(data) != "first\n" {
		t.Fatal(string(data))
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("left the lock file behind")
	}

	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(path, func([]byte) ([]byte, error) { return []byte("third\n"), nil }); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "second\n" {
		t.Fatal("wrote through another writer's lock")
	}
}

func TestUpdateWritesThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra privileges on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real-config")
	link := filepath.Join(dir, "config")
	if err := os.WriteFile(real, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(link, func([]byte) ([]byte, error) { return []byte("new\n"), nil }); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced the symlink with a file")
	}
	if data, _ := os.ReadFile(real); string(data) != "new\n" {
		t.Fatal(string(data))
	}
}
