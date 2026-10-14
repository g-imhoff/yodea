// Package store is yodead's Go data access for preview metadata: deployed
// sites, per-viewer visit history, per-viewer favorites, and per-viewer
// preview presets.
//
// Store choice (documented per brief): the MVP runs on the local
// file-backed Store below, whose row model mirrors the Supabase schema in
// supabase/migrations/ one to one (sites, site_views, favorites, presets).
// Set YODEA_STORE=supabase with SUPABASE_URL plus a server-side
// key to use SupabaseStore (PostgREST) instead; DevNoAuth and all tests use
// the local store so nothing needs live Supabase. Personal-rows-only
// isolation is enforced in Go here and by RLS policies there; preview reads
// (SiteByLabel) are allowed for any logged-in user in both.
package store

import (
	"bytes"
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

const PresetLimit = 100

const PresetDataMax = 16384

var ErrPresetLimit = errors.New("preset limit reached")

type Preset struct {
	UserID    string          `json:"user_id"`
	Label     string          `json:"label"`
	Name      string          `json:"name"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt time.Time       `json:"updated_at"`
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
	ListPresets(token, userID, label string) ([]Preset, error)
	PutPreset(token string, p Preset) (Preset, error)
	DeletePreset(token, userID, label, name string) (bool, error)
	// Probe reports whether the backend is reachable. Local always answers
	// from memory; Supabase performs one small system-keyed read.
	Probe() error
}

type data struct {
	Sites     map[string]*Site      `json:"sites"`     // key: label
	Views     map[string][]View     `json:"views"`     // key: user ID
	Favorites map[string][]Favorite `json:"favorites"` // key: user ID
}

type presetData struct {
	Presets map[string][]Preset `json:"presets"`
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
	dir            string
	path           string
	bakPath        string
	presetsPath    string
	presetsBakPath string
	lockPath       string
	mu             sync.Mutex
	d              data
	p              presetData
	lockFile       *os.File
}

// Open loads or creates the store at dir/db.json plus dir/presets.json. It
// takes the single-writer flock on dir/yodead.lock; a second process opening
// the same dir gets an error. When a primary file is corrupt or missing, Open
// boots from its .bak copy when that copy parses, logging which file was used.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:            dir,
		path:           filepath.Join(dir, "db.json"),
		bakPath:        filepath.Join(dir, "db.json.bak"),
		presetsPath:    filepath.Join(dir, "presets.json"),
		presetsBakPath: filepath.Join(dir, "presets.json.bak"),
		lockPath:       filepath.Join(dir, "yodead.lock"),
	}
	s.ensureMaps()

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
	if s.p.Presets == nil {
		s.p.Presets = map[string][]Preset{}
	}
}

// loadWithFallback populates s.d and s.p from their primary files, falling
// back to each backup when its primary is missing, empty, or corrupt. The
// file used is logged whenever a backup is involved.
func (s *Store) loadWithFallback() error {
	d, err := bootState(s.path, s.bakPath, func(d *data) bool {
		return len(d.Sites) > 0 || len(d.Views) > 0 || len(d.Favorites) > 0
	})
	if err != nil {
		return err
	}
	p, err := bootState(s.presetsPath, s.presetsBakPath, func(p *presetData) bool {
		return len(p.Presets) > 0
	})
	if err != nil {
		return err
	}
	if d != nil {
		s.d = *d
	}
	if p != nil {
		s.p = *p
	}
	s.ensureMaps()
	return nil
}

func bootState[T any](path, bakPath string, hasRows func(*T) bool) (*T, error) {
	buf, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		bak, berr := os.ReadFile(bakPath)
		if berr == nil && len(bak) > 0 {
			bd := new(T)
			if jerr := json.Unmarshal(bak, bd); jerr == nil {
				log.Printf("store: primary %s missing, using backup %s", path, bakPath)
				return bd, nil
			}
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		bak, berr := os.ReadFile(bakPath)
		if berr == nil && len(bak) > 0 {
			bd := new(T)
			if jerr := json.Unmarshal(bak, bd); jerr == nil && hasRows(bd) {
				log.Printf("store: primary %s empty, using backup %s", path, bakPath)
				return bd, nil
			}
		}
		return nil, nil
	}
	dd := new(T)
	if err := json.Unmarshal(buf, dd); err != nil {
		log.Printf("store: primary %s corrupt (%v), trying backup %s", path, err, bakPath)
		bak, berr := os.ReadFile(bakPath)
		if berr != nil {
			return nil, fmt.Errorf("store %s corrupt: %w (no backup)", path, err)
		}
		bd := new(T)
		if err2 := json.Unmarshal(bak, bd); err2 != nil {
			return nil, fmt.Errorf("store %s corrupt (%v) and backup %s corrupt (%v)", path, err, bakPath, err2)
		}
		log.Printf("store: using backup %s", bakPath)
		return bd, nil
	}
	return dd, nil
}

// reloadLocked re-reads the on-disk state into memory. Callers must hold
// s.mu. It keeps in-memory state when no file exists yet and falls back to
// the backup when the primary is corrupt, so a save never blindly
// overwrites good disk state with stale memory.
func (s *Store) reloadLocked() error {
	d, err := reloadState[data](s.path, s.bakPath)
	if err != nil {
		return err
	}
	if d != nil {
		s.d = *d
		s.ensureMaps()
	}
	return nil
}

func (s *Store) reloadPresetsLocked() error {
	p, err := reloadState[presetData](s.presetsPath, s.presetsBakPath)
	if err != nil {
		return err
	}
	if p != nil {
		s.p = *p
		s.ensureMaps()
	}
	return nil
}

func reloadState[T any](path, bakPath string) (*T, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if bak, berr := os.ReadFile(bakPath); berr == nil && len(bak) > 0 {
				bd := new(T)
				if jerr := json.Unmarshal(bak, bd); jerr == nil {
					return bd, nil
				}
			}
			return nil, nil
		}
		return nil, err
	}
	if len(buf) == 0 {
		return nil, nil
	}
	nd := new(T)
	if err := json.Unmarshal(buf, nd); err != nil {
		bak, berr := os.ReadFile(bakPath)
		if berr == nil {
			bd := new(T)
			if jerr := json.Unmarshal(bak, bd); jerr == nil {
				log.Printf("store: primary %s corrupt (%v), using backup %s", path, err, bakPath)
				return bd, nil
			}
		}
		return nil, err
	}
	return nd, nil
}

func fsyncDir(dir string) {
	df, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = df.Sync()
	_ = df.Close()
}

func (s *Store) save() error {
	buf, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	return writeState[data](s.dir, s.path, s.bakPath, buf)
}

func (s *Store) savePresets() error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s.p); err != nil {
		return err
	}
	return writeState[presetData](s.dir, s.presetsPath, s.presetsBakPath, buf.Bytes())
}

// writeState writes one store file atomically: previous good primary is
// rotated to its .bak (only when it parses, so a corrupt primary never
// destroys a good backup), the new payload goes to <path>.tmp with an fsync,
// then is renamed over the primary followed by a directory fsync. Callers
// must hold s.mu.
func writeState[T any](dir, path, bakPath string, buf []byte) error {
	tmp := path + ".tmp"
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
	if prev, err := os.ReadFile(path); err == nil && len(prev) > 0 {
		var probe T
		if json.Unmarshal(prev, &probe) == nil {
			bakTmp := bakPath + ".tmp"
			bf, berr := os.OpenFile(bakTmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if berr == nil {
				_, werr := bf.Write(prev)
				serr := bf.Sync()
				cerr := bf.Close()
				if werr == nil && serr == nil && cerr == nil {
					if rerr := os.Rename(bakTmp, bakPath); rerr != nil {
						_ = os.Remove(bakTmp)
					}
				} else {
					_ = bf.Close()
					_ = os.Remove(bakTmp)
				}
			}
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fsyncDir(dir)
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
// It never includes other users' views. n is clamped to 0..ViewReturn;
// n<=0 returns empty.
func (s *Store) RecentViews(_, userID string, n int) []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		return []View{}
	}
	if n > ViewReturn {
		n = ViewReturn
	}
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

// Probe checks metadata health with an actual read plus JSON parse of
// db.json and db.json.bak into a throwaway, mirroring loadWithFallback's
// fallback without mutating s.d. It is healthy when at least one file
// parses, including a missing primary with a valid backup (boot falls
// back). Fresh or empty stores (both files missing or empty) stay healthy.
// Otherwise (both corrupt, or unreadable) it returns an error. A missing
// metadata dir is an error.
func (s *Store) Probe() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, err := os.Stat(s.dir); err != nil {
		return err
	} else if !st.IsDir() {
		return errors.New("metadata dir is not a directory")
	}
	primaryBuf, primaryErr := os.ReadFile(s.path)
	if primaryErr == nil && len(primaryBuf) > 0 {
		var dd data
		if json.Unmarshal(primaryBuf, &dd) == nil {
			return nil
		}
	}
	backupBuf, backupErr := os.ReadFile(s.bakPath)
	if backupErr == nil && len(backupBuf) > 0 {
		var bd data
		if json.Unmarshal(backupBuf, &bd) == nil {
			return nil
		}
	}
	primaryMissingOrEmpty := errors.Is(primaryErr, os.ErrNotExist) || (primaryErr == nil && len(primaryBuf) == 0)
	backupMissingOrEmpty := errors.Is(backupErr, os.ErrNotExist) || (backupErr == nil && len(backupBuf) == 0)
	if primaryMissingOrEmpty && backupMissingOrEmpty {
		return nil
	}
	return fmt.Errorf("metadata store unreadable: primary %s (%s), backup %s (%s)",
		s.path, probeDetail(primaryErr, primaryBuf), s.bakPath, probeDetail(backupErr, backupBuf))
}

// probeDetail describes why a probe read did not parse, for Probe errors.
// It reports read failures verbatim, empty files as empty, and non-empty
// unparsable files as corrupt with the JSON error. Callers only use it on
// the error path where neither file parsed.
func probeDetail(readErr error, buf []byte) string {
	if readErr != nil {
		return readErr.Error()
	}
	if len(buf) == 0 {
		return "empty"
	}
	var dd data
	if err := json.Unmarshal(buf, &dd); err != nil {
		return "corrupt: " + err.Error()
	}
	return "unreadable"
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

func copyPreset(p Preset) Preset {
	var b bytes.Buffer
	if err := json.Compact(&b, p.Data); err == nil {
		p.Data = b.Bytes()
	} else {
		p.Data = append(json.RawMessage(nil), p.Data...)
	}
	return p
}

func checkPreset(p Preset) error {
	if p.UserID == "" || p.Label == "" || p.Name == "" {
		return errors.New("user, label, and name are required")
	}
	if !json.Valid(p.Data) {
		return errors.New("preset data must be valid JSON")
	}
	return nil
}

func (s *Store) ListPresets(_, userID, label string) ([]Preset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Preset{}
	for _, p := range s.p.Presets[userID] {
		if p.Label == label {
			out = append(out, copyPreset(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) PutPreset(_ string, p Preset) (Preset, error) {
	if err := checkPreset(p); err != nil {
		return Preset{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadPresetsLocked(); err != nil {
		return Preset{}, err
	}
	p = copyPreset(p)
	p.UpdatedAt = time.Now().UTC()
	list := s.p.Presets[p.UserID]
	count := 0
	for i, cur := range list {
		if cur.Label != p.Label {
			continue
		}
		if cur.Name == p.Name {
			list[i] = p
			if err := s.savePresets(); err != nil {
				return Preset{}, err
			}
			return copyPreset(p), nil
		}
		count++
	}
	if count >= PresetLimit {
		return Preset{}, ErrPresetLimit
	}
	s.p.Presets[p.UserID] = append(list, p)
	if err := s.savePresets(); err != nil {
		return Preset{}, err
	}
	return copyPreset(p), nil
}

func (s *Store) DeletePreset(_, userID, label, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadPresetsLocked(); err != nil {
		return false, err
	}
	list := s.p.Presets[userID]
	for i, p := range list {
		if p.Label == label && p.Name == name {
			list = append(list[:i], list[i+1:]...)
			if len(list) == 0 {
				delete(s.p.Presets, userID)
			} else {
				s.p.Presets[userID] = list
			}
			if err := s.savePresets(); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}
