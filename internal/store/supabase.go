// Supabase-backed Storage: the same metadata rows over PostgREST.
//
// Wiring: set YODEA_STORE=supabase with SUPABASE_URL and
// SUPABASE_SERVICE_KEY. The service key never leaves the server process:
// it is only ever an apikey header plus, for the explicit system context,
// an Authorization header on these back-channel calls, never a cookie,
// never a response body, never a client path. Row tables and RLS policies
// live in supabase/migrations/0001_yodea_mvp.sql; every data request below
// carries the viewer's JWT as Authorization so RLS sees the caller.
//
// Behavior change: data calls no longer fall back to the server key when
// the caller token is empty. A missing caller JWT is an error for writes
// (UpsertSite, AddFavorite) and a nil/false/empty result without any HTTP
// request for reads that cannot be answered safely. The only server-key
// Authorization path is the explicit system context (SystemToken) plus the
// legacy SiteByLabel("", label) existence check used by Caddy on-demand TLS
// ask, which keeps working without a caller JWT because any existing site
// may get a certificate. ConfigFromEnv matches server.New here: it
// requires SUPABASE_SERVICE_KEY and no longer falls back to
// SUPABASE_ANON_KEY.
//
// Read errors: fetchSiteByLabel distinguishes transport/status/JSON
// failures from genuine not-found so UpsertSite aborts the write when the
// precheck fails instead of treating nil as label-free. List-style reads
// keep their historical nil-on-error shape (nil means error, non-nil empty
// means genuinely empty) so existing callers stay compiling; Upsert uses
// the error-aware path internally.
//
// Orphan favorites: the local Store prunes favorites eagerly on DeleteSite
// while RLS stops one viewer deleting another's favorite rows, so
// SupabaseStore.ListFavorites filters orphans against existing sites with
// one batched existence query. Both satisfy "deleted previews drop out of
// favorites"; on existence-check failure ListFavorites returns the
// unfiltered rows to avoid data loss.
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

// viewTrimLimit bounds one trim listing so concurrent traffic cannot grow
// the response unbounded; trimming converges over successive writes.
const viewTrimLimit = 200

// SystemToken is the explicit system context for back-channel existence
// checks with no caller JWT (Caddy on-demand TLS ask). Pass it as the
// token to SiteByLabel to authorize with the server-side key. It is
// rejected for user-scoped reads and writes, which require a caller JWT.
const SystemToken = "yodea-system"

// SupabaseConfig is the server-side Supabase wiring. Key stays on the
// server; see the package note above.
type SupabaseConfig struct {
	URL string
	Key string
}

// ConfigFromEnv reads SUPABASE_URL plus SUPABASE_SERVICE_KEY. ok is false
// when either is missing. It intentionally does not fall back to
// SUPABASE_ANON_KEY so it stays consistent with server.New, which requires
// a server-side key for the supabase store.
func ConfigFromEnv() (cfg SupabaseConfig, ok bool) {
	cfg.URL = strings.TrimSuffix(os.Getenv("SUPABASE_URL"), "/")
	cfg.Key = os.Getenv("SUPABASE_SERVICE_KEY")
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

func isSystemToken(token string) bool { return token == SystemToken }

// requireCallerToken rejects empty and system tokens for user-scoped
// calls. Empty previously fell back to the service_role key, which let
// PostgREST skip RLS; that fallback is removed.
func requireCallerToken(token string) error {
	if token == "" {
		return fmt.Errorf("supabase: caller JWT is required")
	}
	if isSystemToken(token) {
		return fmt.Errorf("supabase: system context cannot perform user-scoped calls")
	}
	return nil
}

func (s *SupabaseStore) req(method, table, query, token string, body any) (*http.Request, error) {
	if err := requireCallerToken(token); err != nil {
		return nil, err
	}
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
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// systemReq builds a request authorized explicitly with the server-side
// key. Use only for the system existence check (SiteByLabel with an empty
// or SystemToken token, e.g. Caddy ask).
func (s *SupabaseStore) systemReq(method, table, query string, body any) (*http.Request, error) {
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
	req.Header.Set("Authorization", "Bearer "+s.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
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

// fetchSiteByLabel distinguishes read failures from genuine not-found:
// (nil, nil) means no such label, (nil, err) means the precheck failed and
// callers must abort instead of treating nil as label-free. Empty and
// SystemToken tokens use the explicit system context so the Caddy ask
// existence check keeps working without a caller JWT.
func (s *SupabaseStore) fetchSiteByLabel(token, label string) (*Site, error) {
	var req *http.Request
	var err error
	if token == "" || isSystemToken(token) {
		req, err = s.systemReq(http.MethodGet, tableSites, eq("label", label), nil)
	} else {
		req, err = s.req(http.MethodGet, tableSites, eq("label", label), token, nil)
	}
	if err != nil {
		return nil, err
	}
	var out []Site
	if err := s.do(req, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	cp := out[0]
	return &cp, nil
}

func isConflictError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, sub := range []string{"409", "conflict", "duplicate", "already exists", "unique"} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// UpsertSite records a deploy keyed by label. A label owned by another
// user is rejected before writing (mirrors Store's label-taken rule);
// an upsert that returns no rows (RLS denial) is an error. The precheck
// aborts on read errors, and genuine write conflicts (409/duplicate, or a
// zero-row write that re-reads as another owner's label) map to the exact
// `label is taken` string the local backend returns so callers see one
// contract under concurrent deploy races.
func (s *SupabaseStore) UpsertSite(token string, site *Site) error {
	if site.Label == "" || site.UserID == "" || site.Project == "" {
		return fmt.Errorf("label, user, and project are required")
	}
	if err := requireCallerToken(token); err != nil {
		return err
	}
	existing, err := s.fetchSiteByLabel(token, site.Label)
	if err != nil {
		return err
	}
	if existing != nil && existing.UserID != site.UserID {
		return fmt.Errorf("label is taken")
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
	if err := s.do(req, &out); err != nil {
		if isConflictError(err) {
			return fmt.Errorf("label is taken")
		}
		if other, ferr := s.fetchSiteByLabel(token, site.Label); ferr == nil && other != nil && other.UserID != site.UserID {
			return fmt.Errorf("label is taken")
		}
		return err
	}
	if len(out) == 0 {
		if other, ferr := s.fetchSiteByLabel(token, site.Label); ferr == nil && other != nil && other.UserID != site.UserID {
			return fmt.Errorf("label is taken")
		} else if ferr != nil {
			return ferr
		}
		return fmt.Errorf("supabase upsert returned no rows")
	}
	return nil
}

// SiteByLabel returns the site for a preview label, or nil. Any logged-in
// viewer may read any label (RLS: authenticated SELECT on sites). An empty
// or SystemToken token uses the explicit system context (Caddy ask); other
// read errors also return nil, so writers needing to distinguish failure
// from not-found must use fetchSiteByLabel.
func (s *SupabaseStore) SiteByLabel(token, label string) *Site {
	site, err := s.fetchSiteByLabel(token, label)
	if err != nil {
		return nil
	}
	return site
}

// ListSites returns one user's sites newest first. Nil means the read
// failed; non-nil empty means the user genuinely has no sites.
func (s *SupabaseStore) ListSites(token, userID string) []*Site {
	if err := requireCallerToken(token); err != nil {
		return nil
	}
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
// to read-time filtering (see package note). The DELETE requests its
// representation and returns (nil, nil) on zero rows (RLS denial or
// concurrent delete) so the handler skips file cleanup on a no-op. Transport
// or request-build failures return (nil, err) so the handler can fail the
// request instead of reporting success while the delete was lost.
func (s *SupabaseStore) DeleteSite(token, userID, project string) (*Site, error) {
	if err := requireCallerToken(token); err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("project", "eq."+project)
	req, err := s.req(http.MethodGet, tableSites, q(v), token, nil)
	if err != nil {
		return nil, err
	}
	var found []Site
	if err := s.do(req, &found); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	site := found[0]
	del, err := s.req(http.MethodDelete, tableSites, eq("label", site.Label), token, nil)
	if err != nil {
		return nil, err
	}
	del.Header.Set("Prefer", "return=representation")
	var deleted []Site
	if err := s.do(del, &deleted); err != nil {
		return nil, err
	}
	if len(deleted) == 0 {
		return nil, nil
	}
	return &site, nil
}

// RecordView prepends a visit; the table is trimmed to ViewCap per viewer.
// The trim lists ids only ordered by at.desc,id.desc with a bounded limit
// past ViewCap, then deletes the overflow in one statement. Insert and trim
// errors are returned; callers log trim failures but still serve the view.
func (s *SupabaseStore) RecordView(token, userID, label string) error {
	if err := requireCallerToken(token); err != nil {
		return err
	}
	body := map[string]any{"user_id": userID, "label": label, "at": time.Now().UTC()}
	req, err := s.req(http.MethodPost, tableSiteViews, "", token, body)
	if err != nil {
		return err
	}
	req.Header.Set("Prefer", "return=minimal")
	if err := s.do(req, nil); err != nil {
		return err
	}

	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("select", "id")
	v.Set("order", "at.desc,id.desc")
	v.Set("offset", fmt.Sprint(ViewCap))
	v.Set("limit", fmt.Sprint(viewTrimLimit))
	list, err := s.req(http.MethodGet, tableSiteViews, q(v), token, nil)
	if err != nil {
		return err
	}
	var old []struct {
		ID int64 `json:"id"`
	}
	if err := s.do(list, &old); err != nil {
		return err
	}
	if len(old) == 0 {
		return nil
	}
	ids := make([]string, 0, len(old))
	for _, row := range old {
		ids = append(ids, fmt.Sprint(row.ID))
	}
	delQ := url.Values{}
	delQ.Set("user_id", "eq."+userID)
	delQ.Set("id", "in.("+strings.Join(ids, ",")+")")
	del, err := s.req(http.MethodDelete, tableSiteViews, q(delQ), token, nil)
	if err != nil {
		return err
	}
	return s.do(del, nil)
}

// RecentViews returns personal history newest first, at most n entries.
// n is clamped to 0..ViewReturn; n<=0 returns empty without a request.
// Nil means the read failed.
func (s *SupabaseStore) RecentViews(token, userID string, n int) []View {
	if n <= 0 {
		return []View{}
	}
	if n > ViewReturn {
		n = ViewReturn
	}
	if err := requireCallerToken(token); err != nil {
		return nil
	}
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("order", "at.desc,id.desc")
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

// Probe performs one small system-keyed read to prove PostgREST is up.
// Any transport or status error is returned; callers treat it as unready.
func (s *SupabaseStore) Probe() error {
	v := url.Values{}
	v.Set("select", "label")
	v.Set("limit", "1")
	req, err := s.systemReq(http.MethodGet, tableSites, q(v), nil)
	if err != nil {
		return err
	}
	var out []struct {
		Label string `json:"label"`
	}
	return s.do(req, &out)
}

// ListFavorites returns one viewer's favorites newest first, filtered
// against existing sites so deleted previews drop out (parity with the
// local Store's eager prune). The existence check runs with the explicit
// system context so RLS cannot hide sites owned by other users: any
// readable preview may be favorited, and only genuinely deleted labels
// filter out. Nil means the favorites read failed; on existence-check
// transport/status failure the unfiltered rows are returned to avoid
// data loss.
func (s *SupabaseStore) ListFavorites(token, userID string) []Favorite {
	if err := requireCallerToken(token); err != nil {
		return nil
	}
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
	if len(out) == 0 {
		return out
	}
	labels := make([]string, 0, len(out))
	seen := map[string]bool{}
	for _, f := range out {
		if f.Label != "" && !seen[f.Label] {
			seen[f.Label] = true
			labels = append(labels, f.Label)
		}
	}
	if len(labels) == 0 {
		return out
	}
	ev := url.Values{}
	ev.Set("select", "label")
	ev.Set("label", "in.("+strings.Join(labels, ",")+")")
	// System context: the viewer JWT would let RLS hide sites owned by
	// other users and wrongly drop shared favorites as orphans.
	ereq, err := s.systemReq(http.MethodGet, tableSites, q(ev), nil)
	if err != nil {
		return out
	}
	var existing []struct {
		Label string `json:"label"`
	}
	if err := s.do(ereq, &existing); err != nil {
		return out
	}
	keep := map[string]bool{}
	for _, e := range existing {
		keep[e.Label] = true
	}
	filtered := make([]Favorite, 0, len(out))
	for _, f := range out {
		if keep[f.Label] {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// AddFavorite saves a label for a viewer; re-favoriting is idempotent.
func (s *SupabaseStore) AddFavorite(token, userID, label string) error {
	if err := requireCallerToken(token); err != nil {
		return err
	}
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

// RemoveFavorite drops one viewer's favorite; (false, nil) means it wasn't
// there. Transport failures return (false, err).
func (s *SupabaseStore) RemoveFavorite(token, userID, label string) (bool, error) {
	if err := requireCallerToken(token); err != nil {
		return false, err
	}
	v := url.Values{}
	v.Set("user_id", "eq."+userID)
	v.Set("label", "eq."+label)
	req, err := s.req(http.MethodDelete, tableFavorites, q(v), token, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Prefer", "return=representation")
	var out []Favorite
	if err := s.do(req, &out); err != nil {
		return false, err
	}
	if len(out) == 0 {
		return false, nil
	}
	return true, nil
}
