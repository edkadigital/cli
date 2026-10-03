// Package config manages non-secret profiles and directory context.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultAPI = "https://api.edka.io"
const LinkName = ".edka.json"

type Profile struct {
	APIURL           string `json:"api_url"`
	ConsoleURL       string `json:"console_url,omitempty"`
	Organization     string `json:"organization,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	Cluster          string `json:"cluster,omitempty"`
}
type Config struct {
	Version  int                `json:"version"`
	Active   string             `json:"active"`
	Profiles map[string]Profile `json:"profiles"`
}
type Link struct {
	Version      int    `json:"version"`
	Profile      string `json:"profile"`
	APIURL       string `json:"api_url"`
	Organization string `json:"organization"`
	Cluster      string `json:"cluster"`
	Deployment   string `json:"deployment,omitempty"`
}

func Dir() (string, error) {
	if value := os.Getenv("EDKA_CONFIG_DIR"); value != "" {
		return filepath.Abs(value)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "edka"), nil
}
func Load(dir string) (*Config, error) {
	c := &Config{Version: 1, Active: "default", Profiles: map[string]Profile{"default": {APIURL: DefaultAPI}}}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err = json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if c.Version != 1 || c.Profiles == nil {
		return nil, fmt.Errorf("unsupported config version; expected version 1")
	}
	return c, nil
}
func Save(dir string, c *Config) error { return WriteJSON(filepath.Join(dir, "config.json"), c) }
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(path, append(data, '\n'))
}
func WritePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".edka-*")
	if err != nil {
		return err
	}
	name := f.Name()
	// After the rename there is no temporary file left to remove.
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func FindLink(start string) (*Link, string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, "", err
	}
	for {
		path := filepath.Join(dir, LinkName)
		data, err := os.ReadFile(path)
		if err == nil {
			var l Link
			if err := json.Unmarshal(data, &l); err != nil {
				return nil, path, fmt.Errorf("invalid %s: %w", path, err)
			}
			if l.Version != 1 {
				return nil, path, fmt.Errorf("unsupported project link version in %s", path)
			}
			return &l, path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, path, err
		}
		// Do not inherit a link from above a repository boundary.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, "", nil
}
func ValidName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_", c) {
			return false
		}
	}
	return true
}
