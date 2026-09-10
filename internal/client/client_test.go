package client

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// RED: these tests exercise the w_cli contract before it exists.

func TestResolveServerDevDefault(t *testing.T) {
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "1")
	if got := ResolveServer(""); got != "http://127.0.0.1:8093" {
		t.Fatalf("dev default = %q, want localhost:8093", got)
	}
}

func TestLoginBadAuthFailsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid email or password"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	if err := c.Login("nobody@example.com", "wrong"); err == nil {
		t.Fatal("expected login error for bad credentials, got nil")
	}
}

func TestInitRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Scaffold(dir, "demo", false); err == nil {
		t.Fatal("expected Scaffold to refuse a non-empty dir without force, got nil")
	}
}

func TestPushFastFailsNonReactTS(t *testing.T) {
	dir := t.TempDir() // no package.json, no vite config
	if err := ValidateReactTS(dir); err == nil {
		t.Fatal("expected ValidateReactTS to reject a non-React-TS dir, got nil")
	}
}

func TestDeploySendsRawTarball(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://demo.example/","project":"demo","label":"demo","files":1,"bytes":10}`))
	}))
	defer srv.Close()

	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(srv.URL, "dev")
	res, err := c.Deploy("demo", dist)
	if err != nil {
		t.Fatal(err)
	}
	if res.URL == "" {
		t.Fatal("expected preview URL in deploy result")
	}
	if gotCT != "application/gzip" {
		t.Fatalf("Content-Type = %q, want raw tarball application/gzip", gotCT)
	}
}
