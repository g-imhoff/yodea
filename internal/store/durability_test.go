package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSingleWriterLockRefusesSecondOpener(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	if _, err := os.Stat(filepath.Join(dir, "yodead.lock")); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("second Open on same dir succeeded, want single-writer refusal")
	} else if !strings.Contains(err.Error(), "single-writer") && !strings.Contains(err.Error(), "locked") {
		t.Fatalf("second Open error = %q, want single-writer/locked", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after Close = %v, want nil", err)
	}
	_ = s2.Close()
}

func TestSaveReloadsExternalChanges(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "a", Label: "lbl-a"}); err != nil {
		t.Fatal(err)
	}
	// External writer bypassing the API (simulates a stale-map risk):
	// add lbl-b directly to db.json on disk.
	path := filepath.Join(dir, "db.json")
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var dd data
	if err := json.Unmarshal(buf, &dd); err != nil {
		t.Fatal(err)
	}
	if dd.Sites == nil {
		dd.Sites = map[string]*Site{}
	}
	dd.Sites["lbl-b"] = &Site{UserID: "alice", Project: "b", Label: "lbl-b"}
	nbuf, _ := json.MarshalIndent(dd, "", "  ")
	if err := os.WriteFile(path, nbuf, 0o600); err != nil {
		t.Fatal(err)
	}
	// Next mutating call must reload lbl-b before saving lbl-c.
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "c", Label: "lbl-c"}); err != nil {
		t.Fatal(err)
	}
	if s.SiteByLabel("", "lbl-b") == nil {
		t.Fatal("reload on save lost external lbl-b change")
	}
	if s.SiteByLabel("", "lbl-c") == nil {
		t.Fatal("own lbl-c change missing")
	}
}

func TestBackupRotationAndFallback(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "a", Label: "lbl-a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "b", Label: "lbl-b"}); err != nil {
		t.Fatal(err)
	}
	bak := filepath.Join(dir, "db.json.bak")
	if _, err := os.Stat(bak); err != nil {
		t.Fatalf("backup missing after two saves: %v", err)
	}
	// Backup must hold the previous good copy (lbl-a).
	bbuf, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	var bd data
	if err := json.Unmarshal(bbuf, &bd); err != nil {
		t.Fatalf("backup corrupt: %v", err)
	}
	if _, ok := bd.Sites["lbl-a"]; !ok {
		t.Fatalf("backup sites = %v, want lbl-a present", bd.Sites)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Corrupt the primary; reopen must boot from backup.
	if err := os.WriteFile(filepath.Join(dir, "db.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open with corrupt primary = %v, want backup fallback", err)
	}
	defer s2.Close()
	if got := s2.SiteByLabel("", "lbl-a"); got == nil {
		t.Fatal("backup fallback lost lbl-a")
	}
}

func TestOpenFailsWhenBothPrimaryAndBackupCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "a", Label: "lbl-a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "b", Label: "lbl-b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "db.json"), []byte("{bad"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "db.json.bak"), []byte("{bad"), 0o600)
	if _, err := Open(dir); err == nil {
		t.Fatal("Open with corrupt primary+backup succeeded, want error")
	}
}

func TestWriteErrorsPropagate(t *testing.T) {
	breakPaths := func(s *Store) {
		badDir := filepath.Join(t.TempDir(), "no-such-dir")
		s.path = filepath.Join(badDir, "db.json")
		s.bakPath = filepath.Join(badDir, "db.json.bak")
		s.dir = badDir
	}
	s := openTest(t)
	// Point at a missing directory so tmp writes fail.
	breakPaths(s)
	if err := s.RecordView("", "alice", "lbl"); err == nil {
		t.Fatal("RecordView with unwritable dir = nil, want error")
	}
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "p", Label: "lbl-p"}); err == nil {
		t.Fatal("UpsertSite with unwritable dir = nil, want error")
	}
	// Seed a deletable site in a good store, then break the paths and delete.
	s2 := openTest(t)
	if err := s2.UpsertSite("", &Site{UserID: "alice", Project: "p", Label: "lbl-p"}); err != nil {
		t.Fatal(err)
	}
	if err := s2.AddFavorite("", "alice", "lbl-p"); err != nil {
		t.Fatal(err)
	}
	breakPaths(s2)
	if _, err := s2.DeleteSite("", "alice", "p"); err == nil {
		t.Fatal("DeleteSite with unwritable dir = nil, want error")
	}
	// Fresh seed for RemoveFavorite so Delete's in-memory mutation above
	// cannot hide the favorite.
	s2b := openTest(t)
	if err := s2b.UpsertSite("", &Site{UserID: "alice", Project: "p", Label: "lbl-p"}); err != nil {
		t.Fatal(err)
	}
	if err := s2b.AddFavorite("", "alice", "lbl-p"); err != nil {
		t.Fatal(err)
	}
	breakPaths(s2b)
	if _, err := s2b.RemoveFavorite("", "alice", "lbl-p"); err == nil {
		t.Fatal("RemoveFavorite with unwritable dir = nil, want error")
	}
	// Not-found contracts stay (nil,nil)/(false,nil) on a healthy store.
	s3 := openTest(t)
	if got, err := s3.DeleteSite("", "alice", "missing"); err != nil || got != nil {
		t.Fatalf("DeleteSite missing = (%+v,%v), want (nil,nil)", got, err)
	}
	if ok, err := s3.RemoveFavorite("", "alice", "missing"); err != nil || ok {
		t.Fatalf("RemoveFavorite missing = (%v,%v), want (false,nil)", ok, err)
	}
}

func TestUpsertCopiesInputAndDeleteCopiesOutput(t *testing.T) {
	s := openTest(t)
	in := &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}
	if err := s.UpsertSite("", in); err != nil {
		t.Fatal(err)
	}
	in.Project = "MUTATED"
	in.Label = "mutated"
	if got := s.SiteByLabel("", "alice-blog"); got == nil || got.Project != "blog" {
		t.Fatalf("UpsertSite stored caller pointer: %+v", got)
	}
	// Internal map must not alias the caller's pointer.
	s.mu.Lock()
	internal := s.d.Sites["alice-blog"]
	s.mu.Unlock()
	if internal == in {
		t.Fatal("UpsertSite stored caller pointer (same address)")
	}
	deleted, err := s.DeleteSite("", "alice", "blog")
	if err != nil || deleted == nil {
		t.Fatalf("DeleteSite = (%+v,%v), want site,nil", deleted, err)
	}
	if deleted == internal {
		t.Fatal("DeleteSite returned internal pointer (same address)")
	}
	deleted.Project = "MUTATED"
	if got := s.SiteByLabel("", "alice-blog"); got != nil {
		t.Fatalf("DeleteSite output aliases store: %+v", got)
	}
	if list := s.ListSites("", "alice"); len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}
}

func TestParallelUpsertRecordViewRace(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	const workers = 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			label := "race-" + itoa(n)
			_ = s.UpsertSite("", &Site{UserID: "alice", Project: "p-" + itoa(n), Label: label})
			for j := 0; j < 10; j++ {
				_ = s.RecordView("", "alice", label)
			}
		}(i)
	}
	wg.Wait()
	if got := s.ListSites("", "alice"); len(got) != workers {
		t.Fatalf("sites after parallel upsert = %d, want %d", len(got), workers)
	}
	views := s.RecentViews("", "alice", ViewCap)
	if len(views) == 0 || len(views) > ViewCap {
		t.Fatalf("views after parallel record = %d, want 1..%d", len(views), ViewCap)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
