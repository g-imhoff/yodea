package server

// Session, safe-next, and missing-UI regressions for yodead.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDevLoginSetsSubdomainCookies(t *testing.T) {
	s := newTestServer(t)
	rec := doReq(s, http.MethodPost, testDomain, "/api/session", "",
		strings.NewReader(`{"email":"alice@example.com","password":"x"}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.UserID == "" {
		t.Fatal("empty user_id")
	}
	cookies := rec.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == "yodea_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("no yodea_session cookie in %v", cookies)
	}
	// One login must cover the dashboard plus preview subdomains. The code
	// sets Domain=.<base>; Go serializes it bare, which per RFC 6265 still
	// covers subdomains.
	found := false
	for _, h := range rec.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(h, "yodea_session=") && strings.Contains(h, "Domain="+testDomain) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no subdomain cookie in %q", rec.Result().Header.Values("Set-Cookie"))
	}
	if !session.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}
	// Empty email is a 400, not a silent default user.
	rec = doReq(s, http.MethodPost, testDomain, "/api/session", "",
		strings.NewReader(`{"email":"","password":"x"}`), "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty email login = %d, want 400", rec.Code)
	}
}

func TestRefreshAndLogout(t *testing.T) {
	s := newTestServer(t)
	rec := doReq(s, http.MethodPost, testDomain, "/api/session/refresh", "",
		strings.NewReader(`{}`), "application/json")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh without token = %d, want 401", rec.Code)
	}
	// Dev mode reissues without a live IdP; no cookie is minted at login.
	rec = doReq(s, http.MethodPost, testDomain, "/api/session/refresh", "",
		strings.NewReader(`{"refresh_token":"anything"}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("dev refresh = %d, want 200", rec.Code)
	}
	req := doReq(s, http.MethodPost, testDomain, "/api/logout", devToken(t, "a@b.c"), nil, "")
	if req.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", req.Code)
	}
	cleared := false
	for _, c := range req.Result().Cookies() {
		if c.Name == "yodea_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout must clear the session cookie")
	}
}

func TestSafeNextRejectsPreviewSubdomains(t *testing.T) {
	s := newTestServer(t)
	for _, in := range []string{
		"https://lbl." + testDomain + "/app",
		"https://lbl." + testDomain + "/",
		"https://a.b." + testDomain + "/x",
	} {
		if got := s.safeNext(in); got != "/" {
			t.Fatalf("safeNext(%q) = %q, want %q (preview subdomains rejected)", in, got, "/")
		}
	}
	if got := s.safeNext("https://" + testDomain + "/dash"); got != "https://"+testDomain+"/dash" {
		t.Fatalf("safeNext central = %q, want central URL kept", got)
	}
	// Preview-subdomain ?next= is stripped to bare /login.
	rec := doReq(s, http.MethodGet, testDomain, "/login?next="+url.QueryEscape("https://lbl."+testDomain+"/app"), "", nil, "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("preview next = %d %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginNextEscaped(t *testing.T) {
	s := newTestServer(t)
	// Dashboard anon redirect must QueryEscape the next value so embedded
	// & and ? survive as one param.
	req := httptest.NewRequest(http.MethodGet, "/?a=1&b=2", nil)
	req.Host = testDomain
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?next=") {
		t.Fatalf("dashboard redirect = %q, want /login?next=...", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("next"); got != "/?a=1&b=2" {
		t.Fatalf("next param = %q, want %q (must be QueryEscaped)", got, "/?a=1&b=2")
	}
}

func TestDevRefreshPreservesViewerIdentity(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	req := httptest.NewRequest(http.MethodPost, "/api/session/refresh", strings.NewReader(`{"refresh_token":"anything"}`))
	req.Host = testDomain
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+alice)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dev refresh = %d, want 200", rec.Code)
	}
	var refreshed string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "yodea_session" {
			refreshed = c.Value
		}
	}
	if refreshed == "" {
		t.Fatal("refresh must set a session cookie")
	}
	if refreshed != alice {
		t.Fatalf("refreshed token = %q, want %q (per-viewer identity preserved)", refreshed, alice)
	}
	// Without any access token, dev refresh documents its fallback to the
	// generic dev user.
	req2 := httptest.NewRequest(http.MethodPost, "/api/session/refresh", strings.NewReader(`{"refresh_token":"anything"}`))
	req2.Host = testDomain
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req2)
	var fallback string
	for _, c := range rec2.Result().Cookies() {
		if c.Name == "yodea_session" {
			fallback = c.Value
		}
	}
	if fallback != "dev" {
		t.Fatalf("anonymous dev refresh = %q, want %q (documented fallback)", fallback, "dev")
	}
}

func TestSafeNext(t *testing.T) {
	s := newTestServer(t)
	for in, want := range map[string]string{
		"/":                                      "/",
		"/dashboard?x=1":                         "/dashboard?x=1",
		"https://" + testDomain + "/":            "https://" + testDomain + "/",
		"https://lbl." + testDomain + "/app":     "/",
		"":                                       "/",
		"//evil.test/":                           "/",
		"https://evil.test/":                     "/",
		"http://lbl." + testDomain + "/":         "/",
		"https://evil.test/?x=lbl." + testDomain: "/",
		"https://" + testDomain + ".evil.test/":  "/",
		"https://user@" + testDomain + "/":       "/",
		"https://" + testDomain + ":8443/x":      "/",
		"javascript:alert(1)":                    "/",
	} {
		if got := s.safeNext(in); got != want {
			t.Fatalf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
	// Unsafe ?next= is stripped server-side instead of bouncing to evil.
	rec := doReq(s, http.MethodGet, testDomain, "/login?next=https://evil.test/", "", nil, "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unsafe next = %d %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMissingUIKeepsAPILive(t *testing.T) {
	s := newTestServer(t)
	if HasAssets() {
		t.Skip("bundle vendored; degraded path not active")
	}
	rec := doReq(s, http.MethodGet, testDomain, "/login", "", nil, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("login without bundle = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not built") {
		t.Fatalf("login body %q should explain the missing UI", rec.Body.String())
	}
	// API and previews stay alive without the bundle.
	if rec := doReq(s, http.MethodGet, testDomain, "/healthz", "", nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz without bundle = %d, want 200", rec.Code)
	}
	rec = doReq(s, http.MethodGet, testDomain, "/api/sites", devToken(t, "a@b.c"), nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("api without bundle = %d, want 200", rec.Code)
	}
}
