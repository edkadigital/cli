// Package kubeconfig merges an Edka user kubeconfig into the file kubectl
// reads, keeping everything else in that file as it was.
package kubeconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/edkadigital/cli/internal/config"
	"go.yaml.in/yaml/v3"
)

// extensionName marks the entries Edka wrote, so a later merge replaces them
// and leaves the user's own entries alone.
const extensionName = "edka.io"

// kinds lists each kubeconfig section with the key of its entries' body.
var kinds = []struct{ section, body string }{
	{"clusters", "cluster"},
	{"contexts", "context"},
	{"users", "user"},
}

var unsafeName = regexp.MustCompile(`[^a-z0-9]+`)

// Name builds the entry name for a cluster: edka-<organization>-<cluster>.
// The organization keeps clusters of the same name in two organizations apart.
func Name(organization, cluster string) string {
	parts := []string{"edka"}
	for _, part := range []string{organization, cluster} {
		if slug := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(part), "-"), "-"); slug != "" {
			parts = append(parts, slug)
		}
	}
	return strings.Join(parts, "-")
}

// DefaultPath is the file kubectl writes new entries to: with KUBECONFIG set,
// its first file that exists, or else its last; otherwise ~/.kube/config.
func DefaultPath() (string, error) {
	var files []string
	for _, file := range filepath.SplitList(os.Getenv("KUBECONFIG")) {
		if file != "" {
			files = append(files, file)
		}
	}
	for _, file := range files {
		if _, err := os.Stat(file); err == nil {
			return file, nil
		}
	}
	if len(files) > 0 {
		return files[len(files)-1], nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kube", "config"), nil
}

// ConflictError reports an entry with Edka's name that Edka didn't write.
type ConflictError struct{ Section, Name string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s entry %q already exists and wasn't written by edka; rename or remove it first", strings.TrimSuffix(e.Section, "s"), e.Name)
}

// Options names the merged entries and records which cluster they belong to.
type Options struct {
	// Name of the primary cluster, context and user entries; see Name.
	Name string
	// ClusterName is what the downloaded entries are named after.
	ClusterName    string
	ClusterID      string
	OrganizationID string
	// Use makes the merged context kubectl's current context.
	Use bool
}

// Check confirms that existing is a kubeconfig Merge can write Options.Name
// into, before a one-time kubeconfig is spent on it.
func Check(existing []byte, name string) error {
	root, err := parse(existing)
	if err != nil {
		return err
	}
	for _, kind := range kinds {
		for _, entry := range sequence(root, kind.section).Content {
			if entryName(entry) == name && owner(entry, kind.body) == "" {
				return &ConflictError{kind.section, name}
			}
		}
	}
	return nil
}

// Merge adds the downloaded kubeconfig's entries to existing under Options.Name,
// replacing the entries an earlier merge wrote for the same cluster. It returns
// the new file and the merged context names, the primary one first.
func Merge(existing, downloaded []byte, opts Options) ([]byte, []string, error) {
	// An empty ID would match every entry without Edka's marker.
	if opts.Name == "" || opts.ClusterID == "" {
		return nil, nil, errors.New("merging a kubeconfig needs a name and a cluster ID")
	}
	source, err := parse(downloaded)
	if err != nil {
		return nil, nil, fmt.Errorf("the downloaded kubeconfig: %w", err)
	}
	target, err := parse(existing)
	if err != nil {
		return nil, nil, err
	}

	users := sequence(source, "users").Content
	renames := map[string]map[string]string{"clusters": {}, "contexts": {}, "users": {}}
	for _, entry := range sequence(source, "clusters").Content {
		renames["clusters"][entryName(entry)] = rename(entryName(entry), opts.ClusterName, opts.Name)
	}
	for _, entry := range sequence(source, "contexts").Content {
		renames["contexts"][entryName(entry)] = rename(entryName(entry), opts.ClusterName+"-user", opts.Name)
	}
	for _, entry := range users {
		renames["users"][entryName(entry)] = opts.Name
		if len(users) > 1 {
			renames["users"][entryName(entry)] = rename(entryName(entry), "", opts.Name)
		}
	}

	for _, kind := range kinds {
		incoming := sequence(source, kind.section).Content
		names := map[string]bool{}
		for _, entry := range incoming {
			name := renames[kind.section][entryName(entry)]
			setEntryName(entry, name)
			body := mapping(entry, kind.body)
			if kind.section == "contexts" {
				for key, section := range map[string]string{"cluster": "clusters", "user": "users"} {
					if ref := value(body, key); ref != nil {
						if renamed, ok := renames[section][ref.Value]; ok {
							ref.Value = renamed
						}
					}
				}
			}
			mark(body, opts)
			names[name] = true
		}

		current := sequence(target, kind.section)
		kept := current.Content[:0]
		for _, entry := range current.Content {
			name, cluster := entryName(entry), owner(entry, kind.body)
			switch {
			case names[name] && cluster == "":
				return nil, nil, &ConflictError{kind.section, name}
			case names[name], cluster == opts.ClusterID:
				// Replace this cluster's earlier entries, including endpoints it no longer has.
			default:
				kept = append(kept, entry)
			}
		}
		current.Content = append(kept, incoming...)
	}

	var contexts []string
	primary := renames["contexts"][scalar(source, "current-context")]
	if primary != "" {
		contexts = append(contexts, primary)
	}
	for _, entry := range sequence(source, "contexts").Content {
		if name := entryName(entry); name != primary {
			contexts = append(contexts, name)
		}
	}
	if len(contexts) == 0 {
		return nil, nil, errors.New("the downloaded kubeconfig has no context")
	}
	if opts.Use || scalar(target, "current-context") == "" {
		setScalar(target, "current-context", contexts[0])
	}

	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(target); err != nil {
		return nil, nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, err
	}
	return out.Bytes(), contexts, nil
}

// Update rewrites path through change while holding kubectl's lock file, and
// keeps the previous version next to it. It returns that backup's path, or ""
// for a new file.
func Update(path string, change func(existing []byte) ([]byte, error)) (string, error) {
	// Write through a symlinked kubeconfig instead of replacing the link.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%s is locked; if nothing else is writing it, remove %s", path, lockPath)
	}
	if err != nil {
		return "", err
	}
	_ = lock.Close()
	defer func() { _ = os.Remove(lockPath) }()

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	updated, err := change(existing)
	if err != nil {
		return "", err
	}
	backup := ""
	if existing != nil {
		backup = path + ".edka-backup"
		if err := config.WritePrivate(backup, existing); err != nil {
			return "", err
		}
	}
	return backup, config.WritePrivate(path, updated)
}

// rename maps a downloaded entry name to the merged one: prefix itself becomes
// name, and prefix-suffix becomes name-suffix.
func rename(entry, prefix, name string) string {
	switch {
	case prefix != "" && entry == prefix:
		return name
	case prefix != "" && strings.HasPrefix(entry, prefix+"-"):
		return name + entry[len(prefix):]
	default:
		return name + "-" + entry
	}
}

// parse returns the top-level mapping of a kubeconfig, or of a new one when data is empty.
func parse(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("not a valid kubeconfig: %w", err)
		}
	}
	if doc.Kind == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode}
		setScalar(root, "apiVersion", "v1")
		setScalar(root, "kind", "Config")
		return root, nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a valid kubeconfig: expected a single YAML mapping")
	}
	return doc.Content[0], nil
}

func value(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func set(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

func scalar(m *yaml.Node, key string) string {
	if v := value(m, key); v != nil && v.Kind == yaml.ScalarNode && v.Tag != "!!null" {
		return v.Value
	}
	return ""
}

func setScalar(m *yaml.Node, key, v string) {
	set(m, key, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
}

// sequence returns m[key] as a sequence, replacing a missing or null value.
func sequence(m *yaml.Node, key string) *yaml.Node {
	if v := value(m, key); v != nil && v.Kind == yaml.SequenceNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.SequenceNode}
	set(m, key, v)
	return v
}

// mapping returns m[key] as a mapping, creating it when missing.
func mapping(m *yaml.Node, key string) *yaml.Node {
	if v := value(m, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	set(m, key, v)
	return v
}

func entryName(entry *yaml.Node) string { return scalar(entry, "name") }

func setEntryName(entry *yaml.Node, name string) { setScalar(entry, "name", name) }

// owner returns the cluster ID Edka recorded on an entry, or "" for another tool's entry.
func owner(entry *yaml.Node, body string) string {
	extensions := value(value(entry, body), "extensions")
	if extensions == nil || extensions.Kind != yaml.SequenceNode {
		return ""
	}
	for _, extension := range extensions.Content {
		if scalar(extension, "name") == extensionName {
			return scalar(value(extension, "extension"), "cluster-id")
		}
	}
	return ""
}

func mark(body *yaml.Node, opts Options) {
	extension := &yaml.Node{Kind: yaml.MappingNode}
	setScalar(extension, "cluster-id", opts.ClusterID)
	if opts.OrganizationID != "" {
		setScalar(extension, "organization-id", opts.OrganizationID)
	}
	entry := &yaml.Node{Kind: yaml.MappingNode}
	setScalar(entry, "name", extensionName)
	set(entry, "extension", extension)

	extensions := sequence(body, "extensions")
	kept := extensions.Content[:0]
	for _, existing := range extensions.Content {
		if scalar(existing, "name") != extensionName {
			kept = append(kept, existing)
		}
	}
	extensions.Content = append(kept, entry)
}
