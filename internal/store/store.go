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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
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
	DeleteSite(token, userID, project string) (*Site, error)
	RecordView(token, userID, label string) error
	RecentViews(token, userID string, n int) []View
	ListFavorites(token, userID string) []Favorite
	AddFavorite(token, userID, label string) error
	RemoveFavorite(token, userID, label string) (bool, error)
}

type data struct {
	Sites     map[string]*Site      `json:"sites"`     // key: label
	Views     map[string][]View     `json:"views"`     // key: user ID
	Favorites map[string][]Favorite `json:"favorites"` // key: user ID
}

// Store is a small JSON metadata store. It fits the 10-user MVP and keeps
// tests hermetic. Writes are atomic and mutex-guarded.
//
// Single-writer hard limit: the mutex guards only in-process memory. Two
// yodead processes sharing one DataDir would load stale maps and
// last-rename-wins on save, so exactly one writer may hold the directory at
// a time. Open enforces this with an exclusive non-blocking flock on
// <dir>/yodead.lock (holding the FD for the Store lifetime) and refuses to
// start a second writer. Do not run two yodeads on one DataDir; restart or
// Close the first before opening a second. Within the single writer,
// mutating methods reload the file before applying the change (so a save
// never silently discards a good on-disk state) and save fsyncs the temp
// file plus the directory for durability.
type Store struct {
	dir      string
	path     string
	bakPath  string
	lockPath string
	mu       sync.Mutex
	d        data
	lockFile *os.File
}

// Open loads or creates the store at dir/db.json. It takes the single-writer
// flock on dir/yodead.lock; a second process opening the same dir gets an
// error. When the primary db.json is corrupt or missing, Open boots from
// db.json.bak when that copy parses, logging which file was used.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:      dir,
		path:     filepath.Join(dir, "db.json"),
		bakPath:  filepath.Join(dir, "db.json.bak"),
		lockPath: filepath.Join(dir, "yodead.lock"),
	}
	s.d.Sites = map[string]*Site{}
	s.d.Views = map[string][]View{}
	s.d.Favorites = map[string][]Favorite{}

	lf, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lf.Close()
		return nil, fmt.Errorf("store %q is locked by another yodead process (single-writer limit): %w", dir, err)
	}
	s.lockFile = lf
	if _, err := lf.Seek(0, 0); err == nil {
		_ = lf.Truncate(0)
		_, _ = fmt.Fprintf(lf, "%d\n", os.Getpid())
		_ = lf.Sync()
	}

	if err := s.loadWithFallback(); err != nil {
		_ = lf.Close()
		s.lockFile = nil
		return nil, err
	}
	return s, nil
}

// Close releases the single-writer lock. Stores that are never closed hold
// the lock until the process exits.
func (s *Store) Close() error {
	if s.lockFile == nil {
		return nil
	}
	_ = syscall.Flock(int(s.lockFile.Fd()), syscall.LOCK_UN)
	err := s.lockFile.Close()
	s.lockFile = nil
	return err
}

func (s *Store) ensureMaps() {
	if s.d.Sites == nil {
		s.d.Sites = map[string]*Site{}
	}
	if s.d.Views == nil {
		s.d.Views = map[string][]View{}
	}
	if s.d.Favorites == nil {
		s.d.Favorites = map[string][]Favorite{}
	}
}

// loadWithFallback populates s.d from the primary file, falling back to the
// backup when the primary is missing, empty, or corrupt. The file used is
// logged whenever the backup is involved.
func (s *Store) loadWithFallback() error {
	buf, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		bak, berr := os.ReadFile(s.bakPath)
		if berr == nil && len(bak) > 0 {
			var bd data
			if jerr := json.Unmarshal(bak, &bd); jerr == nil {
				log.Printf("store: primary %s missing, using backup %s", s.path, s.bakPath)
				s.d = bd
				s.ensureMaps()
				return nil
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	if len(buf) == 0 {
		bak, berr := os.ReadFile(s.bakPath)
		if berr == nil && len(bak) > 0 {
			var bd data
			if jerr := json.Unmarshal(bak, &bd); jerr == nil {
				hasRows := len(bd.Sites) > 0 || len(bd.Views) > 0 || len(bd.Favorites) > 0
				if hasRows {
					log.Printf("store: primary %s empty, using backup %s", s.path, s.bakPath)
					s.d = bd
					s.ensureMaps()
					return nil
				}
			}
		}
		return nil
	}
	var dd data
	if err := json.Unmarshal(buf, &dd); err != nil {
		log.Printf("store: primary %s corrupt (%v), trying backup %s", s.path, err, s.bakPath)
		bak, berr := os.ReadFile(s.bakPath)
		if berr != nil {
			return fmt.Errorf("store %s corrupt: %w (no backup)", s.path, err)
		}
		var bd data
		if err2 := json.Unmarshal(bak, &bd); err2 != nil {
			return fmt.Errorf("store %s corrupt (%v) and backup %s corrupt (%v)", s.path, err, s.bakPath, err2)
		}
		log.Printf("store: using backup %s", s.bakPath)
		s.d = bd
		s.ensureMaps()
		return nil
	}
	s.d = dd
	s.ensureMaps()
	return nil
}

// reloadLocked re-reads the on-disk state into memory. Callers must hold
// s.mu. It keeps in-memory state when no file exists yet and falls back to
// the backup when the primary is corrupt, so a save never blindly
// overwrites good disk state with stale memory.
func (s *Store) reloadLocked() error {
	buf, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if bak, berr := os.ReadFile(s.bakPath); berr == nil && len(bak) > 0 {
				var bd data
				if jerr := json.Unmarshal(bak, &bd); jerr == nil {
					s.d = bd
					s.ensureMaps()
				}
			}
			return nil
		}
		return err
	}
	if len(buf) == 0 {
		return nil
	}
	var nd data
	if err := json.Unmarshal(buf, &nd); err != nil {
		bak, berr := os.ReadFile(s.bakPath)
		if berr == nil {
			var bd data
			if jerr := json.Unmarshal(bak, &bd); jerr == nil {
				log.Printf("store: primary %s corrupt (%v), using backup %s", s.path, err, s.bakPath)
				s.d = bd
				s.ensureMaps()
				return nil
			}
		}
		return err
	}
	s.d = nd
	s.ensureMaps()
	return nil
}

func fsyncDir(dir string) {
	df, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = df.Sync()
	_ = df.Close()
}

// save writes the store atomically: previous good primary is rotated to
// db.json.bak (only when it parses, so a corrupt primary never destroys a
// good backup), the new payload goes to db.json.tmp with an fsync, then is
// renamed over db.json followed by a directory fsync. Callers must hold s.mu.
func (s *Store) save() error {
	buf, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Rotate the previous good copy aside before overwriting.
	if prev, err := os.ReadFile(s.path); err == nil && len(prev) > 0 {
		var probe data
		if json.Unmarshal(prev, &probe) == nil {
			bakTmp := s.bakPath + ".tmp"
			bf, berr := os.OpenFile(bakTmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if berr == nil {
				_, werr := bf.Write(prev)
				serr := bf.Sync()
				cerr := bf.Close()
				if werr == nil && serr == nil && cerr == nil {
					if rerr := os.Rename(bakTmp, s.bakPath); rerr != nil {
						_ = os.Remove(bakTmp)
					}
				} else {
					_ = bf.Close()
					_ = os.Remove(bakTmp)
				}
			}
		}
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fsyncDir(s.dir)
	return nil
}

// UpsertSite records a deploy. Labels are globally unique so a preview Host
// maps to exactly one site. The input pointer is copied so later caller
// mutations cannot alter stored state without a save.
func (s *Store) UpsertSite(_ string, site *Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	if site.Label == "" || site.UserID == "" || site.Project == "" {
		return errors.New("label, user, and project are required")
	}
	if other, ok := s.d.Sites[site.Label]; ok && other.UserID != site.UserID {
		return errors.New("label is taken")
	}
	now := time.Now().UTC()
	cp := *site
	cp.UpdatedAt = now
	site.UpdatedAt = now
	s.d.Sites[cp.Label] = &cp
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
// favorite lists. Callers remove files from disk. The returned site is a
// copy. A nil site with nil error means no such project; a non-nil error
// means the in-memory delete happened but the write failed.
func (s *Store) DeleteSite(_, userID, project string) (*Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	for label, site := range s.d.Sites {
		if site.UserID == userID && site.Project == project {
			cp := *site
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
			if err := s.save(); err != nil {
				return nil, err
			}
			return &cp, nil
		}
	}
	return nil, nil
}

// RecordView prepends a visit, capped at ViewCap per viewer. A non-nil
// error means the visit was recorded in memory but the write failed.
func (s *Store) RecordView(_, userID, label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	views := append([]View{{Label: label, At: time.Now().UTC()}}, s.d.Views[userID]...)
	if len(views) > ViewCap {
		views = views[:ViewCap]
	}
	s.d.Views[userID] = views
	return s.save()
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
	if err := s.reloadLocked(); err != nil {
		return err
	}
	for _, f := range s.d.Favorites[userID] {
		if f.Label == label {
			return nil
		}
	}
	s.d.Favorites[userID] = append([]Favorite{{UserID: userID, Label: label, CreatedAt: time.Now().UTC()}}, s.d.Favorites[userID]...)
	return s.save()
}

// RemoveFavorite drops one viewer's favorite; false with nil error means it
// wasn't there. A non-nil error means the write failed.
func (s *Store) RemoveFavorite(_, userID, label string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return false, err
	}
	favs := s.d.Favorites[userID]
	for i, f := range favs {
		if f.Label == label {
			s.d.Favorites[userID] = append(favs[:i], favs[i+1:]...)
			if err := s.save(); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}
