package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionUnreadableReturnsPermissionError covers the chmod-000 branch:
// a session file the process cannot read must surface the underlying
// permission error, not the misleading "not logged in" hint (which is
// reserved for a missing file).
func TestSessionUnreadableReturnsPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; chmod 000 is not denied for euid 0")
	}
	p := isolateSession(t)
	if err := SaveSession(Session{Server: "http://127.0.0.1:8093", Token: "tok123", UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if _, err := LoadSession(); err == nil {
		t.Fatal("LoadSession with unreadable file = nil, want permission error")
	} else if strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("LoadSession unreadable = %v, want permission error without 'not logged in' hint", err)
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("LoadSession unreadable = %v, want permission denied", err)
	}
}

// TestSessionSaveReadOnlyDirFails covers saves into a read-only config
// dir: MkdirAll succeeds on the existing dir but the write must fail.
func TestSessionSaveReadOnlyDirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; read-only dir is writable for euid 0")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	t.Setenv("YODEA_SESSION_FILE", filepath.Join(dir, "session.json"))
	if err := SaveSession(Session{Server: "http://x", Token: "tok", UserID: "u"}); err == nil {
		t.Fatal("SaveSession in read-only dir = nil, want permission error")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("SaveSession read-only = %v, want permission denied", err)
	}
}

// TestSessionSaveDiskFullFails uses /dev/full, which deterministically
// returns ENOSPC on write, to cover the disk-full save branch.
func TestSessionSaveDiskFullFails(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skipf("no /dev/full to simulate ENOSPC: %v", err)
	}
	t.Setenv("YODEA_SESSION_FILE", "/dev/full")
	err := SaveSession(Session{Server: "http://x", Token: "tok", UserID: "u"})
	if err == nil {
		t.Fatal("SaveSession to /dev/full = nil, want ENOSPC error")
	}
	if !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("SaveSession /dev/full = %v, want 'no space left on device'", err)
	}
}
