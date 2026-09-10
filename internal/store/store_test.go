package store

import (
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSitesArePersonal(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "bob", Project: "shop", Label: "bob-shop"}); err != nil {
		t.Fatal(err)
	}
	got := s.ListSites("", "alice")
	if len(got) != 1 || got[0].Label != "alice-blog" {
		t.Fatalf("alice sees %v, want only alice-blog", got)
	}
	// Preview reads stay open to any logged-in viewer.
	if s.SiteByLabel("", "bob-shop") == nil {
		t.Fatal("SiteByLabel should serve any label to logged-in viewers")
	}
}

func TestLabelCollisionAcrossUsers(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "a", Label: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "bob", Project: "b", Label: "shared"}); err == nil {
		t.Fatal("want label-taken error, got nil")
	}
}

func TestViewsCapAndOrder(t *testing.T) {
	s := openTest(t)
	for i := 0; i < ViewCap+10; i++ {
		s.RecordView("", "alice", "lbl")
	}
	if got := s.RecentViews("", "alice", ViewReturn); len(got) != ViewReturn {
		t.Fatalf("views = %d, want %d", len(got), ViewReturn)
	}
	if got := s.RecentViews("", "bob", ViewReturn); len(got) != 0 {
		t.Fatalf("bob sees %d views, want 0", len(got))
	}
}

func TestSiteByLabelAndListSitesReturnCopies(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err != nil {
		t.Fatal(err)
	}
	got := s.SiteByLabel("", "alice-blog")
	if got == nil {
		t.Fatal("SiteByLabel returned nil")
	}
	got.Project = "MUTATED"
	if again := s.SiteByLabel("", "alice-blog"); again.Project != "blog" {
		t.Fatalf("SiteByLabel aliases internal state: project = %q", again.Project)
	}
	list := s.ListSites("", "alice")
	if len(list) != 1 {
		t.Fatalf("list = %v, want 1", list)
	}
	list[0].Project = "MUTATED"
	list[0].Label = "mutated"
	if again := s.SiteByLabel("", "alice-blog"); again == nil || again.Project != "blog" {
		t.Fatalf("ListSites aliases internal state: %+v", again)
	}
	if again := s.ListSites("", "alice"); again[0].Project != "blog" {
		t.Fatalf("ListSites second read mutated: %+v", again[0])
	}
}

func TestMultiLabelViewOrderingNewestFirst(t *testing.T) {
	s := openTest(t)
	s.RecordView("", "alice", "lbl-a")
	s.RecordView("", "alice", "lbl-b")
	s.RecordView("", "alice", "lbl-c")
	got := s.RecentViews("", "alice", ViewReturn)
	if len(got) != 3 {
		t.Fatalf("views = %v, want 3", got)
	}
	if got[0].Label != "lbl-c" || got[1].Label != "lbl-b" || got[2].Label != "lbl-a" {
		t.Fatalf("views order = %v, want [lbl-c lbl-b lbl-a] newest first", got)
	}
}

func TestFavoritesPrivateAndPrunedOnDelete(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFavorite("", "bob", "alice-blog"); err != nil {
		t.Fatal(err)
	}
	if got := s.ListFavorites("", "alice"); len(got) != 0 {
		t.Fatalf("alice sees %d favorites, want 0", len(got))
	}
	if got := s.ListFavorites("", "bob"); len(got) != 1 {
		t.Fatalf("bob sees %d favorites, want 1", len(got))
	}
	if !s.RemoveFavorite("", "bob", "nope") {
		// expected false; keep linters quiet about ignored result below
	}
	if s.RemoveFavorite("", "bob", "nope") {
		t.Fatal("removing missing favorite should return false")
	}
	// Re-add, then delete the preview: the favorite must drop out.
	if err := s.AddFavorite("", "bob", "alice-blog"); err != nil {
		t.Fatal(err)
	}
	if s.DeleteSite("", "alice", "blog") == nil {
		t.Fatal("delete should return the removed site")
	}
	if got := s.ListFavorites("", "bob"); len(got) != 0 {
		t.Fatalf("deleted preview still favorited: %v", got)
	}
}
