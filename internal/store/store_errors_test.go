package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreReadOnlyDataDirUpsertFails covers the DataDir read-only
// branch: Open succeeds on the existing dir, but the atomic save
// (db.json.tmp write) must fail instead of silently dropping the deploy.
func TestStoreReadOnlyDataDirUpsertFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; read-only dir is writable for euid 0")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	err = s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"})
	if err == nil {
		t.Fatal("UpsertSite in read-only DataDir = nil, want permission error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("UpsertSite read-only = %v, want permission denied", err)
	}
}

// TestStoreDiskFullUpsertFails simulates ENOSPC with /dev/full. The store
// path cannot live directly under /dev/full (it is a char device, not a
// directory: Open("/dev/full") fails with "not a directory"), so the
// hermetic simulation routes the atomic tmp write through /dev/full via a
// symlink inside a TempDir DataDir. The write then deterministically
// fails with ENOSPC and no db.json is left behind.
func TestStoreDiskFullUpsertFails(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skipf("no /dev/full to simulate ENOSPC: %v", err)
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "db.json.tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink("/dev/full", tmp); err != nil {
		t.Fatal(err)
	}
	err = s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"})
	if err == nil {
		t.Fatal("UpsertSite with tmp->/dev/full = nil, want ENOSPC error")
	}
	if !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("UpsertSite disk-full = %v, want 'no space left on device'", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "db.json")); !os.IsNotExist(statErr) {
		t.Fatalf("db.json stat = %v, want not-exist after failed save", statErr)
	}
}
