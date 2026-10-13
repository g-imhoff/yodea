package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func deployLabel(t *testing.T, s *Server, token, project string) string {
	t.Helper()
	rec := deploy(t, s, token, project, tarGz(t, map[string]string{"index.html": "<h1>" + project + "</h1>"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy %s = %d: %s", project, rec.Code, rec.Body.String())
	}
	var dep struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dep); err != nil || dep.Label == "" {
		t.Fatalf("deploy response %q: %v", rec.Body.String(), err)
	}
	return dep.Label
}

func TestAPIOnPreviewHostIsNotFound(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	label := deployLabel(t, s, alice, "blog")
	sessionValue, csrf := loginCookieAndCSRF(t, s, "alice@example.com")

	for _, host := range []string{
		label + "." + testDomain,
		strings.ToUpper(label) + "." + testDomain + ":443",
		"other.example.com",
		"yodead:8093",
		"",
	} {
		for _, tc := range []struct {
			method, path, body string
		}{
			{http.MethodGet, "/api/config", ""},
			{http.MethodPost, "/api/session", `{"email":"alice@example.com","password":"x"}`},
			{http.MethodPost, "/api/session/refresh", `{"refresh_token":"anything"}`},
			{http.MethodPost, "/api/logout", ""},
			{http.MethodGet, "/api/sites", ""},
			{http.MethodPost, "/api/sites/blog/deploy", ""},
			{http.MethodDelete, "/api/sites/blog", ""},
			{http.MethodGet, "/api/views", ""},
			{http.MethodGet, "/api/favorites", ""},
			{http.MethodPost, "/api/favorites", `{"label":` + jsonQuote(label) + `}`},
			{http.MethodDelete, "/api/favorites/" + label, ""},
		} {
			for _, auth := range []string{"bearer", "cookie", "none"} {
				req := cookieReq(tc.method, tc.path, "", "", tc.body)
				req.Host = host
				switch auth {
				case "bearer":
					req.Header.Set("Authorization", "Bearer "+alice)
				case "cookie":
					req.AddCookie(&http.Cookie{Name: "yodea_session", Value: sessionValue})
					req.Header.Set(csrfHeaderName, csrf)
				}
				rec := serve(s, req)
				assertCode(t, rec.Code, rec.Body.String(), "not_found", http.StatusNotFound)
				var body map[string]string
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
				if len(body) != 2 || body["error"] != "not found" {
					t.Fatalf("%s %s host %q (%s) body = %q, want exactly {code:not_found,error:not found}", tc.method, tc.path, host, auth, rec.Body.String())
				}
				for _, c := range rec.Result().Cookies() {
					t.Fatalf("%s %s host %q (%s) set cookie %q, want none", tc.method, tc.path, host, auth, c.Name)
				}
			}
		}
	}

	if got := s.metadb.SiteByLabel("", label); got == nil {
		t.Fatal("preview-host DELETE /api/sites/blog removed the site")
	}
	rec := doReq(s, http.MethodGet, testDomain, "/api/favorites", alice, nil, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), label) {
		t.Fatalf("favorites after preview-host POST = %d %s, want 200 without %s", rec.Code, rec.Body.String(), label)
	}
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAPIOnCentralHostUnchanged(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	label := deployLabel(t, s, alice, "blog")

	for _, host := range []string{testDomain, strings.ToUpper(testDomain), testDomain + ":443"} {
		rec := doReq(s, http.MethodGet, host, "/api/config", "", nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config host %q = %d (%s), want 200", host, rec.Code, rec.Body.String())
		}
		var cfg map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatalf("config body %q: %v", rec.Body.String(), err)
		}
		if rec := doReq(s, http.MethodGet, host, "/api/sites", alice, nil, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), label) {
			t.Fatalf("GET /api/sites host %q = %d %s, want 200 listing %s", host, rec.Code, rec.Body.String(), label)
		}
		if rec := doReq(s, http.MethodGet, host, "/api/views", alice, nil, ""); rec.Code != http.StatusOK {
			t.Fatalf("GET /api/views host %q = %d, want 200", host, rec.Code)
		}
		if rec := doReq(s, http.MethodGet, host, "/api/favorites", alice, nil, ""); rec.Code != http.StatusOK {
			t.Fatalf("GET /api/favorites host %q = %d, want 200", host, rec.Code)
		}
	}
	rec := doReq(s, http.MethodPost, testDomain, "/api/session", "",
		strings.NewReader(`{"email":"bob@example.com","password":"x"}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("central login = %d, want 200", rec.Code)
	}
	rec = doReq(s, http.MethodPost, testDomain, "/api/favorites", alice,
		strings.NewReader(`{"label":`+jsonQuote(label)+`}`), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("central favorite = %d, want 200", rec.Code)
	}
	rec = doReq(s, http.MethodDelete, testDomain, "/api/favorites/"+label, alice, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("central unfavorite = %d, want 200", rec.Code)
	}
	rec = doReq(s, http.MethodPost, testDomain, "/api/logout", alice, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("central logout = %d, want 200", rec.Code)
	}
	rec = doReq(s, http.MethodDelete, testDomain, "/api/sites/blog", alice, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("central site delete = %d, want 200", rec.Code)
	}
}

func TestNonAPIRoutesIgnoreCentralHostRule(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	label := deployLabel(t, s, alice, "blog")
	previewHost := label + "." + testDomain

	for _, host := range []string{"yodead:8093", "yodead", previewHost, testDomain} {
		rec := doReq(s, http.MethodGet, host, "/api/caddy-ask?domain="+previewHost, "", nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("caddy-ask via host %q = %d (%s), want 200", host, rec.Code, rec.Body.String())
		}
		rec = doReq(s, http.MethodGet, host, "/api/caddy-ask?domain=missing."+testDomain, "", nil, "")
		assertCode(t, rec.Code, rec.Body.String(), "unknown_site", http.StatusNotFound)
		if rec := doReq(s, http.MethodGet, host, "/healthz", "", nil, ""); rec.Code != http.StatusOK {
			t.Fatalf("healthz via host %q = %d, want 200", host, rec.Code)
		}
	}

	rec := doReq(s, http.MethodGet, previewHost, "/login", "", nil, "")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://"+testDomain+"/login?next=") {
		t.Fatalf("preview /login = %d %q, want 302 to central login", rec.Code, rec.Header().Get("Location"))
	}
	rec = doReq(s, http.MethodGet, previewHost, "/", alice, nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "blog") {
		t.Fatalf("preview root = %d %q, want 200", rec.Code, rec.Body.String())
	}
	rec = doReq(s, http.MethodGet, previewHost, "/api/unregistered-route", alice, nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "blog") {
		t.Fatalf("preview SPA route under /api/ = %d %q, want index.html", rec.Code, rec.Body.String())
	}
	rec = doReq(s, http.MethodGet, testDomain, "/", "", nil, "")
	if rec.Code != http.StatusFound {
		t.Fatalf("central root anon = %d, want 302", rec.Code)
	}
}
