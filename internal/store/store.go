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

type data struct {
	Sites map[string]*Site   `json:"sites"` // key: label
	Views map[string][]View  `json:"views"` // key: user ID
}

// Store is a small JSON metadata store. It fits the 10-user MVP and avoids
// a database dependency. Writes are atomic and mutex-guarded.
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

// UpsertSite records a deploy. Labels are globally unique so Host maps to
// exactly one site.
func (s *Store) UpsertSite(site *Site) error {
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

// SiteByLabel returns the site for a preview hostname label, or nil.
func (s *Store) SiteByLabel(label string) *Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Sites[label]
}

// ListSites returns one user's sites newest first.
func (s *Store) ListSites(userID string) []*Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Site
	for _, site := range s.d.Sites {
		if site.UserID == userID {
			out = append(out, site)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// DeleteSite removes metadata. Callers remove files from disk.
func (s *Store) DeleteSite(userID, project string) *Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	for label, site := range s.d.Sites {
		if site.UserID == userID && site.Project == project {
			delete(s.d.Sites, label)
			_ = s.save()
			return site
		}
	}
	return nil
}

// RecordView appends a visit, capped at 50 per user.
func (s *Store) RecordView(userID, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := append([]View{{Label: label, At: time.Now().UTC()}}, s.d.Views[userID]...)
	if len(views) > 50 {
		views = views[:50]
	}
	s.d.Views[userID] = views
	_ = s.save()
}

// RecentViews returns personal history newest first. It never includes
// other users' views.
func (s *Store) RecentViews(userID string, n int) []View {
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
