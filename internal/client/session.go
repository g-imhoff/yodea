package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Session is the locally persisted login: server URL plus bearer token.
// The file lives at OS config dir (0600): e.g.
// ~/.config/yodea/session.json. YODEA_SESSION_FILE overrides the path
// (tests, throwaway dev logins).
type Session struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	UserID string `json:"user_id"`
}

// SessionPath reports where the session file lives.
func SessionPath() (string, error) {
	if v := os.Getenv("YODEA_SESSION_FILE"); v != "" {
		return v, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir unavailable: %w", err)
	}
	return filepath.Join(dir, "yodea", "session.json"), nil
}

// SaveSession writes the session with 0600 permissions.
func SaveSession(s Session) error {
	path, err := SessionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	// WriteFile leaves existing modes alone; enforce owner-only access.
	// On unix this is Chmod 0600, on Windows an owner-only DACL.
	return restrictSessionFile(path)
}

// LoadSession reads the persisted session, or an actionable login error.
func LoadSession() (Session, error) {
	path, err := SessionPath()
	if err != nil {
		return Session{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Session{}, fmt.Errorf("not logged in (run 'yodea login' first)")
		}
		return Session{}, fmt.Errorf("read session file: %w", err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil || s.Token == "" || s.Server == "" {
		return Session{}, fmt.Errorf("saved session is corrupt (run 'yodea login' again)")
	}
	return s, nil
}
