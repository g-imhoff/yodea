// Supabase-backed Storage: the same metadata rows over PostgREST.
//
// Wiring: set YODEA_STORE=supabase with SUPABASE_URL and a server-side key
// (service role preferred; the anon key works but leans fully on RLS).
// The service key never leaves the server process: it is only ever an
// Authorization header on these back-channel calls, never a cookie, never
// a response body, never a client path. Row tables and RLS policies live in
// supabase/migrations/0001_yodea_mvp.sql; every request below carries the
// viewer's JWT as well so RLS sees the caller even when the service key is
// configured.
//
// Orphan favorites: RLS lets a site owner delete only their own favorite
// rows, so readers (server handlers) filter favorites against existing
// sites and skip labels whose preview is gone. The local Store prunes
// eagerly instead; both satisfy "deleted previews drop out of favorites".
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Supabase tables. Keep in sync with supabase/migrations/0001_yodea_mvp.sql.
const (
	tableSites     = "sites"
	tableSiteViews = "site_views"
	tableFavorites = "favorites"
)

// SupabaseConfig is the server-side Supabase wiring. Key stays on the
// server; see the package note above.
type SupabaseConfig struct {
	URL string
	Key string
}

// ConfigFromEnv reads SUPABASE_URL plus SUPABASE_SERVICE_KEY (falling back
// to SUPABASE_ANON_KEY). ok is false when the URL or any key is missing.
func ConfigFromEnv() (cfg SupabaseConfig, ok bool) {
	cfg.URL = strings.TrimSuffix(os.Getenv("SUPABASE_URL"), "/")
	cfg.Key = os.Getenv("SUPABASE_SERVICE_KEY")
	if cfg.Key == "" {
		cfg.Key = os.Getenv("SUPABASE_ANON_KEY")
	}
	if cfg.URL == "" || cfg.Key == "" {
		return SupabaseConfig{}, false
	}
	return cfg, true
}

// SupabaseStore implements Storage over PostgREST.
type SupabaseStore struct {
	baseURL string
	key     string
	http    *http.Client
}

// NewSupabaseStore builds a PostgREST-backed store for baseURL such as
// https://xyzcompany.supabase.co with a server-side key.
func NewSupabaseStore(baseURL, key string) *SupabaseStore {
	return &SupabaseStore{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		key:     key,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *SupabaseStore) req(method, table, query, token string, body any) (*http.Request, error) {
	u := s.baseURL + "/rest/v1/" + table
	if query != "" {
		u += "?" + query
	}
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", s.key)
	req.Header.Set("Authorization", "Bearer "+firstNonEmpty(token, s.key))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *SupabaseStore) do(req *http.Request, out any) error {
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("supabase %s %s: HTTP %d: %s",
			req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	if out == nil || len(bytes.TrimSpace(buf)) == 0 {
		return nil
	}
	return json.Unmarshal(buf, out)
}

func q(v url.Values) string { return v.Encode() }

func eq(col, val string) string {
	v := url.Values{}
	v.Set(col, "eq."+val)
	return v.Encode()
}

// UpsertSite records a deploy keyed by label.
func (s *SupabaseStore) UpsertSite(token string, site *Site) error {
	if site.Label == "" || site.UserID == "" || site.Project == "" {
		return fmt.Errorf("label, user, and project are required")
	}
	site.UpdatedAt = time.Now().UTC()
	body := map[string]any{
		"label":      site.Label,
		"user_id":    site.UserID,
		"project":    site.Project,
		"files":      site.Files,
		"bytes":      site.Bytes,
		"updated_at": site.UpdatedAt,
	}
	req, err := s.req(http.MethodPost, tableSites,
		"on_conflict=label", token, body)
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=representation")
	var out []Site
	return s.do(req, &out)
}

// SiteByLabel returns the site for a preview label, or nil. Any logged-in
// viewer may read any label (RLS: authenticated SELECT on sites).
func (s *SupabaseStore) SiteByLabel(token, label string) *Site {
	req, err := s.req(http.MethodGet, tableSites, eq("label", label), token, nil)
	if err != nil {
		return nil
	}
	var out []Site
	if err := s.do(req, &out); err != nil || len(out) == 0 {
		return nil
	}
	return &out[0]
}

// ListSites returns one user's sites newest first.
func (s *SupabaseStore) ListSites(token, userID string) []*Site {
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("order", "updated_at.desc")
	req, err := s.req(http.MethodGet, tableSites, q(v), token, nil)
	if err != nil {
		return nil
	}
	var out []*Site
	if err := s.do(req, &out); err != nil {
		return nil
	}
	return out
}

// DeleteSite removes one user's project metadata and returns the removed
// row for disk cleanup. Orphaned favorite rows from other viewers are left
// to read-time filtering (see package note).
func (s *SupabaseStore) DeleteSite(token, userID, project string) *Site {
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("project", "eq."+project)
	req, err := s.req(http.MethodGet, tableSites, q(v), token, nil)
	if err != nil {
		return nil
	}
	var found []Site
	if err := s.do(req, &found); err != nil || len(found) == 0 {
		return nil
	}
	site := found[0]
	del, err := s.req(http.MethodDelete, tableSites, eq("label", site.Label), token, nil)
	if err != nil {
		return nil
	}
	if err := s.do(del, nil); err != nil {
		return nil
	}
	return &site
}

// RecordView prepends a visit; the table is trimmed to ViewCap per viewer.
func (s *SupabaseStore) RecordView(token, userID, label string) {
	body := map[string]any{"user_id": userID, "label": label, "at": time.Now().UTC()}
	req, err := s.req(http.MethodPost, tableSiteViews, "", token, body)
	if err != nil {
		return
	}
	req.Header.Set("Prefer", "return=minimal")
	_ = s.do(req, nil)

	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("order", "at.desc")
	v.Set("offset", fmt.Sprint(ViewCap))
	list, err := s.req(http.MethodGet, tableSiteViews, q(v), token, nil)
	if err != nil {
		return
	}
	var old []struct {
		ID int64 `json:"id"`
	}
	if err := s.do(list, &old); err != nil {
		return
	}
	for _, row := range old {
		del, err := s.req(http.MethodDelete, tableSiteViews,
			eq("id", fmt.Sprint(row.ID)), token, nil)
		if err != nil {
			continue
		}
		_ = s.do(del, nil)
	}
}

// RecentViews returns personal history newest first, at most n entries.
func (s *SupabaseStore) RecentViews(token, userID string, n int) []View {
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("order", "at.desc")
	v.Set("limit", fmt.Sprint(n))
	req, err := s.req(http.MethodGet, tableSiteViews, q(v), token, nil)
	if err != nil {
		return nil
	}
	var out []View
	if err := s.do(req, &out); err != nil {
		return nil
	}
	return out
}

// ListFavorites returns one viewer's favorites newest first.
func (s *SupabaseStore) ListFavorites(token, userID string) []Favorite {
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("order", "created_at.desc")
	req, err := s.req(http.MethodGet, tableFavorites, q(v), token, nil)
	if err != nil {
		return nil
	}
	var out []Favorite
	if err := s.do(req, &out); err != nil {
		return nil
	}
	return out
}

// AddFavorite saves a label for a viewer; re-favoriting is idempotent.
func (s *SupabaseStore) AddFavorite(token, userID, label string) error {
	if userID == "" || label == "" {
		return fmt.Errorf("user and label are required")
	}
	body := map[string]any{"user_id": userID, "label": label}
	req, err := s.req(http.MethodPost, tableFavorites, "on_conflict=user_id,label", token, body)
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "resolution=ignore-duplicates,return=minimal")
	return s.do(req, nil)
}

// RemoveFavorite drops one viewer's favorite; false means it wasn't there.
func (s *SupabaseStore) RemoveFavorite(token, userID, label string) bool {
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("label", "eq."+label)
	req, err := s.req(http.MethodDelete, tableFavorites, q(v), token, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Prefer", "return=representation")
	var out []Favorite
	if err := s.do(req, &out); err != nil || len(out) == 0 {
		return false
	}
	return true
}
