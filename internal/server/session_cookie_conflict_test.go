package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func twoCookieReq(method, host, path, first, second, body string, kv ...string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Cookie", "yodea_session="+first+"; yodea_session="+second)
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Set(kv[i], kv[i+1])
	}
	return req
}

func expiredSessionCookies(rec *httptest.ResponseRecorder) map[string]bool {
	out := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "yodea_session" && c.MaxAge < 0 && c.Value == "" {
			out[c.Domain+" "+c.Path] = true
		}
	}
	return out
}

func TestConflictingSessionCookiesNeverPickAUser(t *testing.T) {
	f := newPresetFixture(t)
	mallory := devToken(t, "mallory@example.com")
	bobCookie, bobCSRF := loginCookieAndCSRF(t, f.s, "bob@example.com")
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "bob-own", `{"who":"bob"}`))
	decodePutPreset(t, putPresetReq(t, f.s, f.host, mallory, "mallory-own", `{"who":"mallory"}`))
	malloryLabel := deployLabel(t, f.s, mallory, "evil")

	for _, order := range [][2]string{{mallory, bobCookie}, {bobCookie, mallory}} {
		for _, tc := range []struct {
			method, host, path, body string
			kv                       []string
		}{
			{http.MethodGet, f.host, "/_yodea/presets", "", []string{"Sec-Fetch-Site", "same-origin"}},
			{http.MethodPut, f.host, presetPath("my-private"), `{"note":"secret"}`, sameOriginHeaders(f.host)},
			{http.MethodPut, f.host, presetPath("bob-own"), `{"who":"changed"}`, sameOriginHeaders(f.host)},
			{http.MethodDelete, f.host, presetPath("mallory-own"), "", []string{"Origin", "https://" + f.host}},
			{http.MethodDelete, f.host, presetPath("bob-own"), "", []string{"Origin", "https://" + f.host}},
			{http.MethodGet, testDomain, "/api/sites", "", nil},
			{http.MethodGet, testDomain, "/api/views", "", nil},
			{http.MethodGet, testDomain, "/api/favorites", "", nil},
			{http.MethodPost, testDomain, "/api/favorites", `{"label":"` + f.label + `"}`, []string{"Content-Type", "application/json", csrfHeaderName, bobCSRF}},
			{http.MethodPost, testDomain, "/api/sites/evil/deploy", "x", []string{"Content-Type", "application/gzip", csrfHeaderName, bobCSRF}},
		} {
			rec := httptest.NewRecorder()
			f.s.Handler().ServeHTTP(rec, twoCookieReq(tc.method, tc.host, tc.path, order[0], order[1], tc.body, tc.kv...))
			what := tc.method + " " + tc.host + tc.path
			assertCode(t, rec.Code, rec.Body.String(), "login_required", http.StatusUnauthorized)
			if strings.HasPrefix(tc.path, "/_yodea/") {
				if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("%s: headers %v, want no-store and nosniff", what, rec.Header())
				}
			}
			expired := expiredSessionCookies(rec)
			if expired[testDomain+" /"] {
				t.Fatalf("%s: expired the real Path=/ session cookie: %v", what, rec.Header().Values("Set-Cookie"))
			}
			first := "/" + strings.SplitN(strings.TrimPrefix(tc.path, "/"), "/", 2)[0]
			for _, want := range []string{testDomain + " " + first, testDomain + " " + first + "/", testDomain + " " + tc.path, "example.test /", "example.test " + first} {
				if !expired[want] {
					t.Fatalf("%s: missing expiry for %q, got %v", what, want, rec.Header().Values("Set-Cookie"))
				}
			}
			if expired["test /"] {
				t.Fatalf("%s: expired cookies outside the base domain: %v", what, expired)
			}
			if central := tc.host == testDomain; expired[" /"] == central || !expired[" "+tc.path] {
				t.Fatalf("%s: host-only expiry wrong (central host %v), got %v", what, central, expired)
			}
		}
	}

	if got := presetNames(listPresetsReq(t, f.s, f.host, f.bob)); strings.Join(got, ",") != "bob-own" {
		t.Fatalf("bob's presets = %q, want only bob-own", got)
	}
	bobList := listPresetsReq(t, f.s, f.host, f.bob)
	if !sameJSON(t, string(bobList[0].Data), `{"who":"bob"}`) {
		t.Fatalf("bob-own changed: %s", bobList[0].Data)
	}
	if got := presetNames(listPresetsReq(t, f.s, f.host, mallory)); strings.Join(got, ",") != "mallory-own" {
		t.Fatalf("mallory's presets = %q, want only mallory-own", got)
	}
	for _, tok := range []string{f.bob, mallory} {
		rec := doReq(f.s, http.MethodGet, testDomain, "/api/favorites", tok, nil, "")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), f.label) {
			t.Fatalf("favorites after refused POST = %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := doReq(f.s, http.MethodGet, testDomain, "/api/sites", mallory, nil, "")
	if strings.Count(rec.Body.String(), `"label"`) != 1 || !strings.Contains(rec.Body.String(), malloryLabel) {
		t.Fatalf("mallory's sites changed: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, twoCookieReq(http.MethodGet, f.host, "/", mallory, bobCookie, ""))
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/login") {
		t.Fatalf("preview page with two session cookies = %d %q, want login redirect", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, twoCookieReq(http.MethodGet, testDomain, "/", mallory, bobCookie, ""))
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/login") {
		t.Fatalf("dashboard with two session cookies = %d %q, want login redirect", rec.Code, rec.Header().Get("Location"))
	}

	req := twoCookieReq(http.MethodGet, f.host, "/_yodea/presets", mallory, bobCookie, "")
	req.Header.Set("Authorization", "Bearer "+f.bob)
	rec = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "bob-own") || strings.Contains(rec.Body.String(), "mallory-own") {
		t.Fatalf("Bearer with two cookies = %d %s, want bob's list", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/_yodea/presets", nil)
	req.Host = f.host
	req.AddCookie(&http.Cookie{Name: "yodea_session", Value: bobCookie})
	rec = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "bob-own") || len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("single cookie = %d %s Set-Cookie %v", rec.Code, rec.Body.String(), rec.Header().Values("Set-Cookie"))
	}
}

func TestTossedSessionCookiesAreExpiredForBrowsers(t *testing.T) {
	f := newPresetFixture(t)
	mallory := devToken(t, "mallory@example.com")
	malloryLabel := deployLabel(t, f.s, mallory, "evil")
	ts := httptest.NewServer(f.s.Handler())
	defer ts.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, ts.Listener.Addr().String())
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	send := func(method, rawURL, body string, kv ...string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i+1 < len(kv); i += 2 {
			req.Header.Set(kv[i], kv[i+1])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := send(http.MethodPost, "http://"+testDomain+"/api/session", `{"email":"bob@example.com","password":"x"}`, "Content-Type", "application/json"); code != http.StatusOK {
		t.Fatalf("login = %d %s", code, body)
	}
	evil, _ := url.Parse("http://" + malloryLabel + "." + testDomain + "/")
	jar.SetCookies(evil, []*http.Cookie{
		{Name: "yodea_session", Value: mallory, Domain: testDomain, Path: "/_yodea"},
		{Name: "yodea_session", Value: mallory, Domain: testDomain, Path: "/api"},
		{Name: "yodea_session", Value: mallory, Domain: "example.test", Path: "/"},
	})
	victim, _ := url.Parse("http://" + f.host + "/_yodea/presets")
	if n := len(jar.Cookies(victim)); n != 3 {
		t.Fatalf("planted setup: %d cookies for the victim preview, want 3", n)
	}

	put := func() (int, string) {
		return send(http.MethodPut, "http://"+f.host+presetPath("my-private"), `{"note":"bob secret"}`,
			"Content-Type", "application/json", "Origin", "http://"+f.host, "Sec-Fetch-Site", "same-origin")
	}
	if code, body := put(); code != http.StatusUnauthorized {
		t.Fatalf("PUT with tossed cookies = %d %s, want 401", code, body)
	}
	if n := len(jar.Cookies(victim)); n != 1 {
		t.Fatalf("after refusal %d session cookies remain for the preview, want only the real one", n)
	}
	if code, body := put(); code != http.StatusOK {
		t.Fatalf("PUT after expiry = %d %s, want 200", code, body)
	}
	if got := presetNames(listPresetsReq(t, f.s, f.host, f.bob)); strings.Join(got, ",") != "my-private" {
		t.Fatalf("bob's presets = %q, want my-private", got)
	}
	if got := listPresetsReq(t, f.s, f.host, mallory); len(got) != 0 {
		t.Fatalf("mallory captured %q", presetNames(got))
	}

	code, body := send(http.MethodGet, "http://"+testDomain+"/api/sites", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("GET /api/sites with tossed /api cookie = %d %s, want 401", code, body)
	}
	code, body = send(http.MethodGet, "http://"+testDomain+"/api/sites", "")
	var sites struct {
		Sites []json.RawMessage `json:"sites"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &sites) != nil || len(sites.Sites) != 0 {
		t.Fatalf("GET /api/sites after expiry = %d %s, want bob's empty list", code, body)
	}
}

func TestConflictingRefreshCookiesAreRefused(t *testing.T) {
	s := newTestServer(t)
	sessionValue, csrf := loginCookieAndCSRF(t, s, "alice@example.com")
	req := cookieReq(http.MethodPost, "/api/session/refresh", sessionValue, csrf, "")
	req.AddCookie(&http.Cookie{Name: "yodea_refresh", Value: "planted"})
	req.AddCookie(&http.Cookie{Name: "yodea_refresh", Value: "real"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	assertCode(t, rec.Code, rec.Body.String(), "login_required", http.StatusUnauthorized)
	expired := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "yodea_session" {
			t.Fatalf("refused refresh set a session cookie: %v", rec.Header().Values("Set-Cookie"))
		}
		if c.Name == "yodea_refresh" && c.MaxAge < 0 {
			if c.Domain == "" {
				t.Fatalf("expired the host-only refresh cookie: %v", rec.Header().Values("Set-Cookie"))
			}
			expired[c.Domain+" "+c.Path] = true
		}
	}
	for _, d := range []string{testDomain, "example.test"} {
		for _, p := range []string{"/", "/api", "/api/", "/api/session", "/api/session/", "/api/session/refresh"} {
			if !expired[d+" "+p] {
				t.Fatalf("missing refresh expiry for %s %s: %v", d, p, rec.Header().Values("Set-Cookie"))
			}
		}
	}

	req = cookieReq(http.MethodPost, "/api/session/refresh", sessionValue, csrf, "")
	req.AddCookie(&http.Cookie{Name: "yodea_refresh", Value: "real"})
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh with one refresh cookie = %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			t.Fatalf("single refresh cookie expired %v", rec.Header().Values("Set-Cookie"))
		}
	}
}

func TestHostOnlySessionCookieIsExpired(t *testing.T) {
	f := newPresetFixture(t)
	mallory := devToken(t, "mallory@example.com")
	ts := httptest.NewServer(f.s.Handler())
	defer ts.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, ts.Listener.Addr().String())
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	send := func(method, rawURL, body string, kv ...string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i+1 < len(kv); i += 2 {
			req.Header.Set(kv[i], kv[i+1])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := send(http.MethodPost, "http://"+testDomain+"/api/session", `{"email":"bob@example.com","password":"x"}`, "Content-Type", "application/json"); code != http.StatusOK {
		t.Fatalf("login = %d %s", code, body)
	}
	preview, _ := url.Parse("http://" + f.host + "/")
	central, _ := url.Parse("http://" + testDomain + "/")
	jar.SetCookies(preview, []*http.Cookie{{Name: "yodea_session", Value: mallory, Path: "/"}})
	jar.SetCookies(central, []*http.Cookie{{Name: "yodea_session", Value: mallory, Path: "/api"}})

	put := func() (int, string) {
		return send(http.MethodPut, "http://"+f.host+presetPath("kept"), `{"note":"bob"}`,
			"Content-Type", "application/json", "Origin", "http://"+f.host, "Sec-Fetch-Site", "same-origin")
	}
	if code, body := put(); code != http.StatusUnauthorized {
		t.Fatalf("PUT with host-only duplicate = %d %s, want 401", code, body)
	}
	if n := len(jar.Cookies(preview)); n != 1 {
		t.Fatalf("after refusal %d session cookies remain for the preview, want 1", n)
	}
	if code, body := put(); code != http.StatusOK {
		t.Fatalf("PUT after expiry = %d %s, want 200", code, body)
	}
	if got := presetNames(listPresetsReq(t, f.s, f.host, f.bob)); strings.Join(got, ",") != "kept" {
		t.Fatalf("bob's presets = %q, want kept", got)
	}
	if code, body := send(http.MethodGet, "http://"+testDomain+"/api/sites", ""); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/sites with host-only duplicate = %d %s, want 401", code, body)
	}
	if code, body := send(http.MethodGet, "http://"+testDomain+"/api/sites", ""); code != http.StatusOK {
		t.Fatalf("GET /api/sites after expiry = %d %s, want 200", code, body)
	}
}
