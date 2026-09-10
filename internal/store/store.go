// Package store is yodead's Go data access for preview metadata: deployed
// sites, per-viewer visit history, and per-viewer favorites.
//
// Store choice (documented per brief): the MVP runs on the local
// file-backed Store below, whose row model mirrors the Supabase schema in
// supabase/migrations/0001_yodea_mvp.sql one to one (sites, site_views,
// favorites). Set YODEA_STORE=supabase with SUPABASE_URL plus a server-side
// key to use SupabaseStore (PostgREST) instead; DevNoAuth and all tests use
// the local store so nothing needs live Supabase. Personal-rows-only
// isolation is enforced in Go here and by RLS policies there; preview reads
// (SiteByLabel) are allowed for any logged-in user in both.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ViewCap is how many visits are stored per viewer; views endpoints return
// at most ViewReturn of them, newest first.
const (
	ViewCap    = 50
	ViewReturn = 20
)

// Site is one deployed preview.
type Site struct {
	UserID    string    `json:"user_id"`
	Project   string    `json:"project"`
	Label     string    `json:"label"` // globally unique DNS label part
	UpdatedAt time.Time `json:"updated_at"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
}

// View is one recorded preview visit.
type View struct {
	Label string    `json:"label"`
	At    time.Time `json:"at"`
}

// Favorite is one viewer's saved preview.
type Favorite struct {
	UserID    string    `json:"user_id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

// Storage is the metadata backend contract both Store and SupabaseStore
// satisfy. token is the caller's Supabase JWT (empty in DevNoAuth); the
// local Store ignores it, SupabaseStore forwards it so PostgREST RLS sees
// the viewer.
type Storage interface {
	UpsertSite(token string, site *Site) error
	SiteByLabel(token, label string) *Site
	ListSites(token, userID string) []*Site
	DeleteSite(token, userID, project string) *Site
	RecordView(token, userID, label string)
	RecentViews(token, userID string, n int) []View
	ListFavorites(token, userID string) []Favorite
	AddFavorite(token, userID, label string) error
	RemoveFavorite(token, userID, label string) bool
}

type data struct {
	Sites     map[string]*Site      `json:"sites"`     // key: label
	Views     map[string][]View     `json:"views"`     // key: user ID
	Favorites map[string][]Favorite `json:"favorites"` // key: user ID
}

// Store is a small JSON metadata store. It fits the 10-user MVP and keeps
// tests hermetic. Writes are atomic and mutex-guarded.
type Store struct {
	path string
	mu   sync.Mutex
	d    data
}

// Open loads or creates the store at dir/db.json.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "db.json")}
	s.d.Sites = map[string]*Site{}
	s.d.Views = map[string][]View{}
	s.d.Favorites = map[string][]Favorite{}
	buf, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(buf, &s.d); err != nil {
		return nil, err
	}
	if s.d.Sites == nil {
		s.d.Sites = map[string]*Site{}
	}
	if s.d.Views == nil {
		s.d.Views = map[string][]View{}
	}
	if s.d.Favorites == nil {
		s.d.Favorites = map[string][]Favorite{}
	}
	return s, nil
}

func (s *Store) save() error {
	buf, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// UpsertSite records a deploy. Labels are globally unique so a preview Host
// maps to exactly one site.
func (s *Store) UpsertSite(_ string, site *Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if site.Label == "" || site.UserID == "" || site.Project == "" {
		return errors.New("label, user, and project are required")
	}
	if other, ok := s.d.Sites[site.Label]; ok && other.UserID != site.UserID {
		return errors.New("label is taken")
	}
	site.UpdatedAt = time.Now().UTC()
	s.d.Sites[site.Label] = site
	return s.save()
}

// SiteByLabel returns a copy of the site for a preview hostname label,
// or nil. Any logged-in viewer may read any label: this is the preview
// path. Copies are returned so callers cannot mutate internal state.
func (s *Store) SiteByLabel(_, label string) *Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	if site, ok := s.d.Sites[label]; ok {
		cp := *site
		return &cp
	}
	return nil
}

// ListSites returns copies of one user's sites newest first. It never
// includes other users' rows. Each struct is copied and the slice is fresh
// so callers cannot alias or mutate internal state.
func (s *Store) ListSites(_, userID string) []*Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Site
	for _, site := range s.d.Sites {
		if site.UserID == userID {
			cp := *site
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// DeleteSite removes one user's project metadata and drops every favorite
// pointing at its label, so deleted previews disappear from all viewers'
// favorite lists. Callers remove files from disk.
func (s *Store) DeleteSite(_, userID, project string) *Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	for label, site := range s.d.Sites {
		if site.UserID == userID && site.Project == project {
			delete(s.d.Sites, label)
			for uid, favs := range s.d.Favorites {
				kept := favs[:0]
				for _, f := range favs {
					if f.Label != label {
						kept = append(kept, f)
					}
				}
				s.d.Favorites[uid] = kept
			}
			_ = s.save()
			return site
		}
	}
	return nil
}

// RecordView prepends a visit, capped at ViewCap per viewer.
func (s *Store) RecordView(_, userID, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := append([]View{{Label: label, At: time.Now().UTC()}}, s.d.Views[userID]...)
	if len(views) > ViewCap {
		views = views[:ViewCap]
	}
	s.d.Views[userID] = views
	_ = s.save()
}

// RecentViews returns personal history newest first, at most n entries.
// It never includes other users' views.
func (s *Store) RecentViews(_, userID string, n int) []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := s.d.Views[userID]
	if len(views) > n {
		views = views[:n]
	}
	out := make([]View, len(views))
	copy(out, views)
	return out
}

// ListFavorites returns one viewer's favorites newest first.
func (s *Store) ListFavorites(_, userID string) []Favorite {
	s.mu.Lock()
	defer s.mu.Unlock()
	favs := s.d.Favorites[userID]
	out := make([]Favorite, len(favs))
	copy(out, favs)
	return out
}

// AddFavorite saves a label for a viewer; re-favoriting is idempotent.
func (s *Store) AddFavorite(_, userID, label string) error {
	if userID == "" || label == "" {
		return errors.New("user and label are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.d.Favorites[userID] {
		if f.Label == label {
			return nil
		}
	}
	s.d.Favorites[userID] = append([]Favorite{{UserID: userID, Label: label, CreatedAt: time.Now().UTC()}}, s.d.Favorites[userID]...)
	return s.save()
}

// RemoveFavorite drops one viewer's favorite; false means it wasn't there.
func (s *Store) RemoveFavorite(_, userID, label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	favs := s.d.Favorites[userID]
	for i, f := range favs {
		if f.Label == label {
			s.d.Favorites[userID] = append(favs[:i], favs[i+1:]...)
			_ = s.save()
			return true
		}
	}
	return false
}
