package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Credentials persist the login on the author's machine.
type Credentials struct {
	Server       string `json:"server"`
	Email        string `json:"email"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix seconds
}

func dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "yodea")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func credPath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "credentials.json"), nil
}

// Load returns nil, nil when never logged in.
func Load() (*Credentials, error) {
	p, err := credPath()
	if err != nil {
		return nil, err
	}
	buf, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(buf, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes credentials with owner-only permissions.
func Save(c *Credentials) error {
	p, err := credPath()
	if err != nil {
		return err
	}
	buf, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, buf, 0o600)
}

// Clear removes stored credentials.
func Clear() error {
	p, err := credPath()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
