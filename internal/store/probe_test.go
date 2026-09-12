package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Corrupt primary with a valid backup stays healthy: boot falls back to the
// backup, so Probe must too. Probe is read-only and must not touch s.d.
func TestProbeCorruptPrimaryValidBackupHealthy(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "a", Label: "lbl-a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "b", Label: "lbl-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db.json.bak")); err != nil {
		t.Fatalf("want valid backup before corrupting primary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(); err != nil {
		t.Fatalf("Probe with corrupt primary + valid backup = %v, want nil", err)
	}
	// Read-only probe: in-memory state still holds lbl-b from before corruption.
	if got := s.SiteByLabel("", "lbl-b"); got == nil {
		t.Fatal("Probe mutated store state: lbl-b missing after probe")
	}
}

// Both files corrupt must read unready.
func TestProbeBothCorruptError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "a", Label: "lbl-a"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.json.bak"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(); err == nil {
		t.Fatal("Probe with corrupt primary+backup = nil, want error")
	}
}

// A missing metadata dir must read unready.
func TestProbeMissingDirError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(); err == nil {
		t.Fatal("Probe with missing dir = nil, want error")
	}
}
