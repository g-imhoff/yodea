package server

// HTTP contract tests for yodead: auth gating, deploy validation, preview
// sandboxing, and per-viewer privacy of views plus favorites.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/auth"
	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

const testDomain = "previews.example.test"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{DataDir: t.TempDir(), BaseDomain: testDomain, DevNoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func devToken(t *testing.T, email string) string {
	t.Helper()
	tok, _, err := auth.DevTokenFor(email)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func doReq(s *Server, method, host, path, token string, body io.Reader, ctype string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.Host = host
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func tarGz(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func deploy(t *testing.T, s *Server, token, project string, arc *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(s, http.MethodPost, testDomain, "/api/sites/"+project+"/deploy", token, arc, "application/gzip")
}

func TestHealthzOpen(t *testing.T) {
	s := newTestServer(t)
	rec := doReq(s, http.MethodGet, testDomain, "/healthz", "", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rec.Code)
	}
}

func TestUnauthenticatedRejected(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/sites"},
		{http.MethodGet, "/api/views"},
		{http.MethodGet, "/api/favorites"},
		{http.MethodPost, "/api/favorites"},
	} {
		var body io.Reader
		if tc.method == http.MethodPost {
			body = strings.NewReader(`{"label":"x"}`)
		}
		rec := doReq(s, tc.method, testDomain, tc.path, "", body, "application/json")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
	// Dashboard root requires login: redirect to central login.
	rec := doReq(s, http.MethodGet, testDomain, "/", "", nil, "")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("dashboard anon = %d %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestBadLabelRejected(t *testing.T) {
	s := newTestServer(t)
	tok := devToken(t, "alice@example.com")
	for _, project := range []string{"-bad-", strings.Repeat("p", 80)} {
		rec := deploy(t, s, tok, project, tarGz(t, map[string]string{"index.html": "x"}))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("project %q = %d, want 400", project, rec.Code)
		}
	}
}

func TestArchiveWithoutIndexRejected(t *testing.T) {
	s := newTestServer(t)
	rec := deploy(t, s, devToken(t, "alice@example.com"), "blog", tarGz(t, map[string]string{"app.js": "x"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("indexless deploy = %d, want 400", rec.Code)
	}
}

func TestTraversalArchiveRejected(t *testing.T) {
	s := newTestServer(t)
	rec := deploy(t, s, devToken(t, "alice@example.com"), "blog", tarGz(t, map[string]string{
		"index.html":   "ok",
		"../evil.html": "x",
	}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal deploy = %d, want 400", rec.Code)
	}
}

func TestDeployPreviewAndViewsFlow(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	bob := devToken(t, "bob@example.com")

	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "<h1>blog</h1>"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d: %s", rec.Code, rec.Body.String())
	}
	var dep struct {
		Label string `json:"label"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dep); err != nil {
		t.Fatal(err)
	}
	if dep.Label == "" || !strings.Contains(dep.URL, dep.Label+"."+testDomain) {
		t.Fatalf("bad deploy response: %+v", dep)
	}

	// Redeploy overwrites the same label.
	rec2 := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "<h1>v2</h1>"}))
	var dep2 struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &dep2)
	if dep2.Label != dep.Label {
		t.Fatalf("redeploy label = %q, want %q", dep2.Label, dep.Label)
	}

	// Sites are personal: bob sees none.
	rec = doReq(s, http.MethodGet, testDomain, "/api/sites", bob, nil, "")
	var sites struct {
		Sites []any `json:"sites"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sites)
	if len(sites.Sites) != 0 {
		t.Fatalf("bob sees %d sites, want 0", len(sites.Sites))
	}

	// Any logged-in user can open the preview; anon is bounced to login.
	rec = doReq(s, http.MethodGet, dep.Label+"."+testDomain, "/", bob, nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "v2") {
		t.Fatalf("bob preview = %d %q, want 200 with v2", rec.Code, rec.Body.String())
	}
	rec = doReq(s, http.MethodGet, dep.Label+"."+testDomain, "/", "", nil, "")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/login?next=") {
		t.Fatalf("anon preview = %d %q, want 302 with next", rec.Code, rec.Header().Get("Location"))
	}

	// Visit recorded in the viewer's own history only.
	rec = doReq(s, http.MethodGet, testDomain, "/api/views", bob, nil, "")
	var views struct {
		Views []struct {
			Label string `json:"label"`
		} `json:"views"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &views)
	if len(views.Views) != 1 || views.Views[0].Label != dep.Label {
		t.Fatalf("bob views = %+v, want [%s]", views.Views, dep.Label)
	}
	rec = doReq(s, http.MethodGet, testDomain, "/api/views", alice, nil, "")
	var aviews struct {
		Views []any `json:"views"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &aviews)
	if len(aviews.Views) != 0 {
		t.Fatalf("alice sees %d views, want 0", len(aviews.Views))
	}
}

func TestFavoritesFlow(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	bob := devToken(t, "bob@example.com")

	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "x"}))
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)

	// Bob favorites alice's preview: allowed for any preview he can open.
	rec = doReq(s, http.MethodPost, testDomain, "/api/favorites", bob,
		strings.NewReader(`{"label":`+jsonQuote(dep.Label)+`}`), "application/json")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("favorite = %d: %s", rec.Code, rec.Body.String())
	}
	// Favoriting a missing preview is 404.
	rec = doReq(s, http.MethodPost, testDomain, "/api/favorites", bob,
		strings.NewReader(`{"label":"no-such-site"}`), "application/json")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing favorite = %d, want 404", rec.Code)
	}

	rec = doReq(s, http.MethodGet, testDomain, "/api/favorites", bob, nil, "")
	var favs struct {
		Favorites []struct {
			Label   string `json:"label"`
			Link    string `json:"link"`
			Owner   string `json:"owner"`
			Project string `json:"project"`
		} `json:"favorites"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &favs)
	if len(favs.Favorites) != 1 {
		t.Fatalf("bob favorites = %+v, want 1", favs.Favorites)
	}
	f := favs.Favorites[0]
	if f.Label != dep.Label || !strings.Contains(f.Link, dep.Label) || f.Project != "blog" || f.Owner == "" {
		t.Fatalf("bad favorite row: %+v", f)
	}
	// Alice's list stays empty: favorites are per viewer.
	rec = doReq(s, http.MethodGet, testDomain, "/api/favorites", alice, nil, "")
	var afavs struct {
		Favorites []any `json:"favorites"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &afavs)
	if len(afavs.Favorites) != 0 {
		t.Fatalf("alice sees %d favorites, want 0", len(afavs.Favorites))
	}

	// Unfavorite.
	rec = doReq(s, http.MethodDelete, testDomain, "/api/favorites/"+dep.Label, bob, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unfavorite = %d, want 200", rec.Code)
	}
	rec = doReq(s, http.MethodGet, testDomain, "/api/favorites", bob, nil, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &favs)
	if len(favs.Favorites) != 0 {
		t.Fatalf("bob favorites after delete = %+v, want empty", favs.Favorites)
	}
}

func TestDeletedPreviewDropsOutOfFavorites(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	bob := devToken(t, "bob@example.com")

	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "x"}))
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	rec = doReq(s, http.MethodPost, testDomain, "/api/favorites", bob,
		strings.NewReader(`{"label":`+jsonQuote(dep.Label)+`}`), "application/json")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("favorite = %d", rec.Code)
	}
	rec = doReq(s, http.MethodDelete, testDomain, "/api/sites/blog", alice, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doReq(s, http.MethodGet, testDomain, "/api/favorites", bob, nil, "")
	var favs struct {
		Favorites []any `json:"favorites"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &favs)
	if len(favs.Favorites) != 0 {
		t.Fatalf("deleted preview still favorited: %+v", favs.Favorites)
	}
	// Preview itself is gone.
	rec = doReq(s, http.MethodGet, dep.Label+"."+testDomain, "/", bob, nil, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted preview = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestCaddyAskAllowsOnlyLivePreviews(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")

	ask := func(domain string) int {
		rec := doReq(s, http.MethodGet, testDomain, "/api/caddy-ask?domain="+domain, "", nil, "")
		return rec.Code
	}

	// Unknown label, apex, foreign host, and bad labels are denied.
	if got := ask("blog." + testDomain); got != http.StatusNotFound {
		t.Fatalf("ask unknown = %d, want 404", got)
	}
	if got := ask(testDomain); got != http.StatusNotFound {
		t.Fatalf("ask apex = %d, want 404", got)
	}
	if got := ask("blog.evil.test"); got != http.StatusNotFound {
		t.Fatalf("ask foreign = %d, want 404", got)
	}
	if got := ask("-bad-." + testDomain); got != http.StatusNotFound {
		t.Fatalf("ask bad label = %d, want 404", got)
	}
	if got := ask(""); got != http.StatusNotFound {
		t.Fatalf("ask empty = %d, want 404", got)
	}

	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "x"}))
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	if got := ask(dep.Label + "." + testDomain); got != http.StatusOK {
		t.Fatalf("ask live preview = %d, want 200", got)
	}
	// Case-insensitive like DNS.
	if got := ask(strings.ToUpper(dep.Label) + "." + testDomain); got != http.StatusOK {
		t.Fatalf("ask uppercase = %d, want 200", got)
	}

	// Deleted previews stop getting certificates.
	if rec := doReq(s, http.MethodDelete, testDomain, "/api/sites/blog", alice, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	if got := ask(dep.Label + "." + testDomain); got != http.StatusNotFound {
		t.Fatalf("ask deleted = %d, want 404", got)
	}
}

func TestMultipartDeploy(t *testing.T) {
	s := newTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("archive", "dist.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	arc := tarGz(t, map[string]string{"index.html": "mp"})
	if _, err := io.Copy(fw, arc); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	rec := doReq(s, http.MethodPost, testDomain, "/api/sites/mp/deploy",
		devToken(t, "alice@example.com"), &buf, mw.FormDataContentType())
	if rec.Code != http.StatusOK {
		t.Fatalf("multipart deploy = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMultipartOversizedIgnoredFieldRejected(t *testing.T) {
	s := newTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// Oversized ignored field first: the cap must reject it with 413
	// without unbounded reads.
	fw, err := mw.CreateFormField("note")
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(sites.MaxUploadBytes) + 1
	chunk := make([]byte, 1<<20)
	var written int64
	for written < limit {
		n := int64(len(chunk))
		if written+n > limit {
			n = limit - written
		}
		if _, err := fw.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
	afw, err := mw.CreateFormFile("archive", "dist.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	arc := tarGz(t, map[string]string{"index.html": "mp"})
	if _, err := io.Copy(afw, arc); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	rec := doReq(s, http.MethodPost, testDomain, "/api/sites/mpbig/deploy",
		devToken(t, "alice@example.com"), &buf, mw.FormDataContentType())
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized ignored field = %d: %s, want 413", rec.Code, rec.Body.String())
	}
}

func TestPreviewSandboxHeaders(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "<h1>hi</h1>"}))
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	rec = doReq(s, http.MethodGet, dep.Label+"."+testDomain, "/", alice, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts" {
		t.Fatalf("CSP = %q, want %q", got, "sandbox allow-scripts")
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
}

func TestPreviewAssetHitsDoNotRecordViews(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{
		"index.html": "<h1>hi</h1>",
		"app.js":     "console.log(1)",
	}))
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	host := dep.Label + "." + testDomain
	viewCount := func() int {
		r := doReq(s, http.MethodGet, testDomain, "/api/views", alice, nil, "")
		var v struct {
			Views []any `json:"views"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &v)
		return len(v.Views)
	}
	// Document navigation records.
	if r := doReq(s, http.MethodGet, host, "/", alice, nil, ""); r.Code != http.StatusOK {
		t.Fatalf("root preview = %d, want 200", r.Code)
	}
	if n := viewCount(); n != 1 {
		t.Fatalf("views after root = %d, want 1", n)
	}
	// Asset hits must not record.
	if r := doReq(s, http.MethodGet, host, "/app.js", alice, nil, ""); r.Code != http.StatusOK {
		t.Fatalf("asset preview = %d, want 200", r.Code)
	}
	if n := viewCount(); n != 1 {
		t.Fatalf("views after asset = %d, want 1 (assets must not record)", n)
	}
	// Extensionless document fallback records.
	if r := doReq(s, http.MethodGet, host, "/missing-route", alice, nil, ""); r.Code != http.StatusOK {
		t.Fatalf("extensionless fallback = %d, want 200", r.Code)
	}
	if n := viewCount(); n != 2 {
		t.Fatalf("views after extensionless = %d, want 2", n)
	}
	// Missing asset stays 404 and records nothing.
	if r := doReq(s, http.MethodGet, host, "/missing.js", alice, nil, ""); r.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", r.Code)
	}
	if n := viewCount(); n != 2 {
		t.Fatalf("views after missing asset = %d, want 2", n)
	}
}

func TestPreviewTraversalAndDotSegmentDenied(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "ok"}))
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	host := dep.Label + "." + testDomain
	for _, p := range []string{"/.env", "/.git/config", "/assets/.hidden", "/%2e%2e/secret", "/%2E%2E/secret", "/..%2fsecret"} {
		r := doReq(s, http.MethodGet, host, p, alice, nil, "")
		if r.Code == http.StatusOK {
			t.Fatalf("GET %q = 200 with %q, want denial (404 or redirect)", p, r.Body.String())
		}
		if r.Code != http.StatusNotFound && r.Code != http.StatusMovedPermanently && r.Code != http.StatusFound {
			t.Fatalf("GET %q = %d, want 404 or redirect", p, r.Code)
		}
	}
}

func TestDeployRejectsDotfile(t *testing.T) {
	s := newTestServer(t)
	rec := deploy(t, s, devToken(t, "alice@example.com"), "blog", tarGz(t, map[string]string{
		"index.html": "ok",
		".env":       "secret",
	}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("dotfile deploy = %d, want 400", rec.Code)
	}
}

func TestValidProjectRejectsBlankDotOnlyAndFallback(t *testing.T) {
	for _, raw := range []string{"", "   ", ".", "..", "...", "---", "!!!", "___", " - ", "??"} {
		t.Run("reject/"+raw, func(t *testing.T) {
			if validProject(raw) {
				t.Fatalf("validProject(%q) = true, want false", raw)
			}
		})
	}
	for _, raw := range []string{"site", "blog", "my-app", "a1", "my.project"} {
		t.Run("accept/"+raw, func(t *testing.T) {
			if !validProject(raw) {
				t.Fatalf("validProject(%q) = false, want true", raw)
			}
		})
	}
	if validProject("Site") {
		t.Fatal("validProject(Site) = true, want false (sanitizes to reserved site)")
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestLabelSuffixBeyond20(t *testing.T) {
	s := newTestServer(t)
	project := "blog"
	baseUser := "collision-user"
	basePart := auth.UserPart(baseUser)
	base := sites.LabelFor(basePart, project)
	// Occupy base plus -2..-21 with distinct colliding users. Trailing
	// punctuation trims away, so every ID shares one UserPart.
	for i := 0; i < 21; i++ {
		var label string
		if i == 0 {
			label = base
		} else {
			label = fmt.Sprintf("%s-%d", base, i+1)
		}
		uid := baseUser + strings.Repeat("!", i)
		if auth.UserPart(uid) != basePart {
			t.Fatalf("setup user %q maps to %q, want %q", uid, auth.UserPart(uid), basePart)
		}
		if err := s.metadb.UpsertSite("", &store.Site{
			UserID:  uid,
			Project: fmt.Sprintf("occupy-%d", i),
			Label:   label,
		}); err != nil {
			t.Fatalf("occupy %q: %v", label, err)
		}
	}
	newcomer := baseUser + strings.Repeat("#", 22)
	if auth.UserPart(newcomer) != basePart {
		t.Fatalf("newcomer maps to %q, want %q", auth.UserPart(newcomer), basePart)
	}
	got := s.labelFor("", newcomer, project)
	want := fmt.Sprintf("%s-%d", base, 22)
	if got != want {
		t.Fatalf("suffix beyond 20 = %q, want %q", got, want)
	}
	if err := sites.ValidateLabel(got); err != nil {
		t.Fatalf("suffixed label invalid: %v", err)
	}
}

func TestLabelSuffixLongBaseFits63(t *testing.T) {
	s := newTestServer(t)
	baseUser := "edge-user"
	basePart := auth.UserPart(baseUser)
	longProject := strings.Repeat("p", 60)
	base := sites.LabelFor(basePart, longProject)
	if len(base) != 63 {
		t.Fatalf("setup base len = %d (%q), want 63", len(base), base)
	}
	occupant := baseUser + "!"
	if err := s.metadb.UpsertSite("", &store.Site{
		UserID:  occupant,
		Project: "other",
		Label:   base,
	}); err != nil {
		t.Fatalf("occupy base: %v", err)
	}
	newcomer := baseUser + "!!"
	got := s.labelFor("", newcomer, longProject)
	if got == base {
		t.Fatalf("long-base collision did not suffix: %q", got)
	}
	if len(got) > 63 {
		t.Fatalf("suffixed long base too long (%d): %q", len(got), got)
	}
	if err := sites.ValidateLabel(got); err != nil {
		t.Fatalf("suffixed long base invalid: %v", err)
	}
	if !strings.HasSuffix(got, "-2") {
		t.Fatalf("suffixed long base = %q, want suffix -2", got)
	}
}

func TestDevNoAuthGuard(t *testing.T) {
	// Production domain must refuse to boot with DevNoAuth.
	s, err := New(Config{DataDir: t.TempDir(), BaseDomain: "previews.example.com", DevNoAuth: true})
	if err == nil {
		s.Close()
		t.Fatal("New(DevNoAuth, previews.example.com) succeeded, want refusal")
	} else if !strings.Contains(err.Error(), "base domain") {
		t.Fatalf("prod-domain error = %q, want it to name the base domain condition", err.Error())
	}
	// Test/local domain stays allowed on the default loopback bind.
	s, err = New(Config{DataDir: t.TempDir(), BaseDomain: "previews.example.test", DevNoAuth: true})
	if err != nil {
		t.Fatalf("New(DevNoAuth, previews.example.test) = %v, want success", err)
	}
	s.Close()
	// Public bind must refuse even with a test domain.
	s, err = New(Config{Addr: "0.0.0.0:8093", DataDir: t.TempDir(), BaseDomain: testDomain, DevNoAuth: true})
	if err == nil {
		s.Close()
		t.Fatal("New(DevNoAuth, 0.0.0.0) succeeded, want refusal")
	} else if !strings.Contains(err.Error(), "bind") {
		t.Fatalf("public-bind error = %q, want it to name the bind condition", err.Error())
	}
}
