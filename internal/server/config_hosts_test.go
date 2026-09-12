package server

// Regression tests for config/host/safeNext hardening.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSupabaseStoreAcceptsAnonFallback(t *testing.T) {
	// Aligns New with ConfigFromEnv: anon key works as the server-side
	// key instead of refusing to boot.
	s, err := New(Config{
		DataDir:      t.TempDir(),
		BaseDomain:   testDomain,
		DevNoAuth:    true,
		StoreBackend: "supabase",
		SupabaseURL:  "https://xyz.supabase.co",
		AnonKey:      "anon-key",
	})
	if err != nil {
		t.Fatalf("supabase with anon fallback = %v, want boot", err)
	}
	s.Close()
	// No key at all still fails.
	if _, err := New(Config{
		DataDir:      t.TempDir(),
		BaseDomain:   testDomain,
		DevNoAuth:    true,
		StoreBackend: "supabase",
		SupabaseURL:  "https://xyz.supabase.co",
	}); err == nil {
		t.Fatal("supabase without any key = nil, want error")
	}
	// Service key still boots.
	s2, err := New(Config{
		DataDir:      t.TempDir(),
		BaseDomain:   testDomain,
		DevNoAuth:    true,
		StoreBackend: "supabase",
		SupabaseURL:  "https://xyz.supabase.co",
		SupabaseKey:  "service-key",
	})
	if err != nil {
		t.Fatalf("supabase with service key = %v, want boot", err)
	}
	s2.Close()
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":          "example.com",
		"Example.COM":          "example.com",
		"  example.com  ":      "example.com",
		"example.com:443":      "example.com",
		"127.0.0.1:8093":       "127.0.0.1",
		"[::1]:8093":           "::1",
		"[::1]":                "::1",
		"lbl.previews.example.test": "lbl.previews.example.test",
		"LBL.PREVIEWS.EXAMPLE.TEST": "lbl.previews.example.test",
	} {
		got, ok := normalizeHost(in)
		if !ok || got != want {
			t.Fatalf("normalizeHost(%q) = %q,%v want %q,true", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", " ", ":443", "host:", "host:abc", "host:12x",
		"::1", "fe80::1", "a:b:c",
		"[::1", "[::1]extra", "[]:80",
		"user@example.com", "example.com/path",
	} {
		if got, ok := normalizeHost(in); ok {
			t.Fatalf("normalizeHost(%q) = %q,true want rejection", in, got)
		}
	}
}

func TestValidBaseDomain(t *testing.T) {
	for _, d := range []string{
		"previews.example.test", "example.com", "a-b.example.com",
		"127.0.0.1", "localhost",
	} {
		if !validBaseDomain(d) {
			t.Fatalf("validBaseDomain(%q) = false, want true", d)
		}
	}
	for _, d := range []string{
		"", "example.com:8443", "user@example.com",
		"-bad.example.com", "bad-.example.com",
		".example.com", "example.com.", "a..b.com",
		"example_com", "exa mple.com", "192.168.1.1",
		"http://example.com", strings.Repeat("a", 254),
	} {
		if validBaseDomain(d) {
			t.Fatalf("validBaseDomain(%q) = true, want false", d)
		}
	}
	// Startup rejects bad domains but still allows loopback for dev.
	if _, err := New(Config{DataDir: t.TempDir(), BaseDomain: "example.com:8443", DevNoAuth: true}); err == nil {
		t.Fatal("New with port in BaseDomain = nil, want error")
	}
	if _, err := New(Config{DataDir: t.TempDir(), BaseDomain: "127.0.0.1", DevNoAuth: true}); err != nil {
		t.Fatalf("New with 127.0.0.1 = %v, want boot", err)
	}
}

func TestPreviewRoutingCaseInsensitive(t *testing.T) {
	s := newTestServer(t)
	tok := devToken(t, "alice@example.com")
	rec := deploy(t, s, tok, "blog", tarGz(t, map[string]string{"index.html": "<h1>hi</h1>"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d", rec.Code)
	}
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	// Uppercase apex still routes to the dashboard (302 to login anon).
	rec = doReq(s, http.MethodGet, strings.ToUpper(testDomain), "/", "", nil, "")
	if rec.Code != http.StatusFound {
		t.Fatalf("uppercase apex = %d, want 302", rec.Code)
	}
	// Uppercase preview host plus explicit port still serves the preview.
	bob := devToken(t, "bob@example.com")
	rec = doReq(s, http.MethodGet, strings.ToUpper(dep.Label)+"."+strings.ToUpper(testDomain)+":443", "/", bob, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uppercase preview with port = %d, want 200", rec.Code)
	}
	// Caddy ask stays case-insensitive.
	rec = doReq(s, http.MethodGet, testDomain, "/api/caddy-ask?domain="+strings.ToUpper(dep.Label)+"."+strings.ToUpper(testDomain), "", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("uppercase caddy ask = %d, want 200", rec.Code)
	}
}

func TestSafeNextHardening(t *testing.T) {
	s := newTestServer(t)
	for _, in := range []string{
		`/\\evil`,
		`/\evil`,
		"/%5cevil",
		"/%5Cevil",
		"/%2fevil",
		"/%2Fevil",
		"/%00evil",
		"/%00",
		"https://" + testDomain + "/%5cevil",
		"https://user@" + testDomain + "/",
		"https://" + testDomain + ":8443/x",
		"https://user:pass@" + testDomain + "/",
		"/bad\x01path",
	} {
		if got := s.safeNext(in); got != "/" {
			t.Fatalf("safeNext(%q) = %q, want %q", in, got, "/")
		}
	}
	// Legit targets still pass.
	for in, want := range map[string]string{
		"/dashboard?x=1":                  "/dashboard?x=1",
		"https://" + testDomain + "/dash": "https://" + testDomain + "/dash",
	} {
		if got := s.safeNext(in); got != want {
			t.Fatalf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
