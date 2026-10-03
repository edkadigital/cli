// Package credential stores OAuth secrets separately from project context.
package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edkadigital/cli/internal/config"
	"github.com/zalando/go-keyring"
)

var ErrNotFound = errors.New("not signed in; run `edka login`")

type Session struct {
	APIURL             string    `json:"api_url"`
	Issuer             string    `json:"issuer"`
	ClientID           string    `json:"client_id"`
	TokenEndpoint      string    `json:"token_endpoint"`
	RevocationEndpoint string    `json:"revocation_endpoint,omitempty"`
	AccessToken        string    `json:"access_token"`
	RefreshToken       string    `json:"refresh_token,omitempty"`
	ExpiresAt          time.Time `json:"expires_at"`
	Scope              string    `json:"scope"`
}
type Store struct {
	Dir  string
	Mode string
}

func (s Store) path(profile string) string {
	return filepath.Join(s.Dir, "credentials", profile+".json")
}
func (s Store) Load(profile string) (*Session, error) {
	if !config.ValidName(profile) {
		return nil, fmt.Errorf("invalid profile name")
	}
	if s.Mode == "keyring" || s.Mode != "file" && !fileExists(s.path(profile)) {
		value, err := keyring.Get("edka-cli", s.Dir+":"+profile)
		if err == nil {
			var session Session
			if err := json.Unmarshal([]byte(value), &session); err != nil {
				return nil, err
			}
			return &session, nil
		}
		if s.Mode == "keyring" && !errors.Is(err, keyring.ErrNotFound) {
			return nil, fmt.Errorf("read system keyring: %w", err)
		}
	}
	data, err := os.ReadFile(s.path(profile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("invalid saved credentials: %w", err)
	}
	return &session, nil
}

// SignedIn reports whether the profile has saved credentials. Unlike Load, it
// counts a keyring it can't read as an error rather than as no credentials,
// unless a credential file answers first.
func (s Store) SignedIn(profile string) (bool, error) {
	if !config.ValidName(profile) {
		return false, fmt.Errorf("invalid profile name")
	}
	if _, err := os.Stat(s.path(profile)); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if s.Mode == "file" {
		return false, nil
	}
	_, err := keyring.Get("edka-cli", s.Dir+":"+profile)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("read system keyring: %w", err)
}

// Save returns the chosen storage so the caller can explain file fallback.
func (s Store) Save(profile string, session *Session) (string, error) {
	if !config.ValidName(profile) {
		return "", fmt.Errorf("invalid profile name")
	}
	data, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	if s.Mode != "file" {
		if err := keyring.Set("edka-cli", s.Dir+":"+profile, string(data)); err == nil {
			if err := os.Remove(s.path(profile)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			return "keyring", nil
		} else if s.Mode == "keyring" {
			return "", fmt.Errorf("save to system keyring: %w", err)
		}
	}
	return "file", config.WritePrivate(s.path(profile), data)
}
func (s Store) Delete(profile string) error {
	if !config.ValidName(profile) {
		return fmt.Errorf("invalid profile name")
	}
	if s.Mode != "file" {
		err := keyring.Delete("edka-cli", s.Dir+":"+profile)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) && (s.Mode == "keyring" || !fileExists(s.path(profile))) {
			return fmt.Errorf("remove credentials from system keyring: %w", err)
		}
	}
	err := os.Remove(s.path(profile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
