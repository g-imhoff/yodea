package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadSessionMissingFileSaysNotLoggedIn(t *testing.T) {
	p := isolateSession(t) // points at a path that does not exist yet
	if _, err := LoadSession(); err == nil {
		t.Fatal("expected not-logged-in error for missing file, got nil")
	} else if !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("missing file error = %q, want 'not logged in'", err)
	}
	_ = p
}

func TestLoadSessionPermissionDeniedIsNotNotLoggedIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based permission test does not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not block reads")
	}
	p := isolateSession(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"server":"http://x","token":"tok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o600)
	if _, err := os.ReadFile(p); err == nil {
		t.Skip("platform ignores file permission bits; cannot exercise EACCES")
	}
	_, err := LoadSession()
	if err == nil {
		t.Fatal("expected permission error, got nil")
	}
	if strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("permission error misreported as not-logged-in: %q", err)
	}
}

func TestLoadSessionDirectoryAtPathIsMeaningful(t *testing.T) {
	p := isolateSession(t)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSession()
	if err == nil {
		t.Fatal("expected error when a directory sits at the session path, got nil")
	}
	if strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("directory-at-path misreported as not-logged-in: %q", err)
	}
}

func TestSaveSessionOwnerOnlyBestEffort(t *testing.T) {
	p := isolateSession(t)
	want := Session{Server: "http://127.0.0.1:8093", Token: "tok-lock", UserID: "u1"}
	if err := SaveSession(want); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Best effort on Windows: the ACL lockdown must not lock out the
		// owner; the session must still round-trip.
		got, err := LoadSession()
		if err != nil {
			t.Fatalf("session unreadable after Windows ACL lockdown: %v", err)
		}
		if got != want {
			t.Fatalf("round-trip = %+v, want %+v", got, want)
		}
		return
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %o, want 600", fi.Mode().Perm())
	}
}
