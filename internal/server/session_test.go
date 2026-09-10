package server

// Session, safe-next, and missing-UI regressions for yodead.

import (
	"encoding/json"
	"net/http"
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

func TestSafeNext(t *testing.T) {
	s := newTestServer(t)
	for in, want := range map[string]string{
		"/":                                      "/",
		"/dashboard?x=1":                         "/dashboard?x=1",
		"https://" + testDomain + "/":            "https://" + testDomain + "/",
		"https://lbl." + testDomain + "/app":     "https://lbl." + testDomain + "/app",
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
