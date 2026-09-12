package server

// CSRF synchronizer plus refresh-cookie scope regressions.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func loginCookieAndCSRF(t *testing.T, s *Server, email string) (sessionValue, csrf string) {
	t.Helper()
	rec := doReq(s, http.MethodPost, testDomain, "/api/session", "",
		strings.NewReader(`{"email":`+jsonQuote(email)+`,"password":"x"}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		UserID    string `json:"user_id"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CSRFToken == "" {
		t.Fatal("login must return csrf_token")
	}
	if want := csrfTokenFor(sessionCookieValue(t, rec)); want != body.CSRFToken {
		t.Fatalf("csrf_token = %q, want recomputed %q", body.CSRFToken, want)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "yodea_session" {
			sessionValue = c.Value
		}
	}
	if sessionValue == "" {
		t.Fatal("no session cookie")
	}
	return sessionValue, body.CSRFToken
}

func sessionCookieValue(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "yodea_session" {
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func cookieReq(method, path, sessionValue, csrf, body string) *http.Request {
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	req.Host = testDomain
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionValue != "" {
		req.AddCookie(&http.Cookie{Name: "yodea_session", Value: sessionValue})
	}
	if csrf != "" {
		req.Header.Set(csrfHeaderName, csrf)
	}
	return req
}

func TestCookieAuthedWriteRequiresCSRF(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "x"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d: %s", rec.Code, rec.Body.String())
	}
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)

	sessionValue, csrf := loginCookieAndCSRF(t, s, "alice@example.com")

	// Cookie-authed POST without header -> 403.
	req := cookieReq(http.MethodPost, "/api/favorites", sessionValue, "", `{"label":`+jsonQuote(dep.Label)+`}`)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie POST without CSRF = %d, want 403", rec.Code)
	}

	// Cookie-authed POST with wrong header -> 403.
	req = cookieReq(http.MethodPost, "/api/favorites", sessionValue, "wrong", `{"label":`+jsonQuote(dep.Label)+`}`)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie POST with wrong CSRF = %d, want 403", rec.Code)
	}

	// Cookie-authed POST with correct header -> 200.
	req = cookieReq(http.MethodPost, "/api/favorites", sessionValue, csrf, `{"label":`+jsonQuote(dep.Label)+`}`)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie POST with CSRF = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// GET with cookie and no header stays open (reads need no token).
	req = cookieReq(http.MethodGet, "/api/favorites", sessionValue, "", "")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie GET without CSRF = %d, want 200", rec.Code)
	}

	// Bearer-authed POST without header -> 200 (not ambient credentials).
	rec = doReq(s, http.MethodPost, testDomain, "/api/favorites", alice,
		strings.NewReader(`{"label":`+jsonQuote(dep.Label)+`}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer POST without CSRF = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestRefreshCookieHasNoDomain(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.setSessionCookies(rec, "access123", 3600, "refresh123")
	var sessionFound, refreshFound bool
	for _, h := range rec.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(h, "yodea_session=") {
			sessionFound = true
			if !strings.Contains(h, "Domain="+testDomain) {
				t.Fatalf("session cookie should stay Domain-scoped: %q", h)
			}
		}
		if strings.HasPrefix(h, "yodea_refresh=") {
			refreshFound = true
			if strings.Contains(h, "Domain=") {
				t.Fatalf("refresh cookie must be host-only (no Domain): %q", h)
			}
			if !strings.Contains(h, "Path=/api/session") {
				t.Fatalf("refresh cookie must keep Path=/api/session: %q", h)
			}
			if !strings.Contains(h, "HttpOnly") {
				t.Fatalf("refresh cookie must stay HttpOnly: %q", h)
			}
			if !strings.Contains(h, "SameSite=Lax") {
				t.Fatalf("refresh cookie must stay SameSite=Lax: %q", h)
			}
		}
	}
	if !sessionFound || !refreshFound {
		t.Fatalf("missing cookies in %q", rec.Result().Header.Values("Set-Cookie"))
	}
}
