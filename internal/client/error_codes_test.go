package client

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIErrorParsesCode(t *testing.T) {
	isolateSession(t)
	err := apiError(409, []byte(`{"error":"label is taken","code":"label_taken"}`))
	if CodeOf(err) != "label_taken" {
		t.Fatalf("CodeOf = %q, want label_taken (%v)", CodeOf(err), err)
	}
	if !IsLabelTaken(err) {
		t.Fatalf("IsLabelTaken = false for coded error %v", err)
	}
	if !strings.Contains(err.Error(), "label is taken") {
		t.Fatalf("coded error should keep message substring, got %v", err)
	}
	// Legacy body without code: substring fallback still works.
	legacy := apiError(409, []byte(`{"error":"label is taken"}`))
	if CodeOf(legacy) != "" {
		t.Fatalf("legacy CodeOf = %q, want empty", CodeOf(legacy))
	}
	if !IsLabelTaken(legacy) {
		t.Fatalf("IsLabelTaken fallback failed for %v", legacy)
	}
}

func TestDeleteMatchesCodeFirst(t *testing.T) {
	isolateSession(t)
	// New server: coded body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no such project","code":"no_such_project"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	err := c.Delete("blog")
	if err == nil {
		t.Fatal("expected no-such-project error, got nil")
	}
	if CodeOf(err) != "no_such_project" {
		t.Fatalf("CodeOf = %q, want no_such_project (%v)", CodeOf(err), err)
	}
	if !IsNoSuchProject(err) || !strings.Contains(err.Error(), "no such project") {
		t.Fatalf("delete error should mention no such project, got %v", err)
	}
	// Old server: uncoded body, status fallback still maps to code.
	oldsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no such project"}`))
	}))
	defer oldsrv.Close()
	c2 := New(oldsrv.URL, "tok")
	err2 := c2.Delete("blog")
	if err2 == nil || !IsNoSuchProject(err2) {
		t.Fatalf("fallback IsNoSuchProject failed for %v", err2)
	}
	if CodeOf(err2) != "no_such_project" {
		t.Fatalf("fallback CodeOf = %q, want no_such_project", CodeOf(err2))
	}
}

func TestDeployMatchesCodeFirst(t *testing.T) {
	isolateSession(t)
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Coded conflict.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"label is taken","code":"label_taken"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "tok").Deploy("blog", dist)
	if err == nil || !IsLabelTaken(err) || CodeOf(err) != "label_taken" {
		t.Fatalf("coded IsLabelTaken failed: %v code=%q", err, CodeOf(err))
	}
	// Legacy conflict without code: substring fallback.
	oldsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"label is taken"}`))
	}))
	defer oldsrv.Close()
	_, err2 := New(oldsrv.URL, "tok").Deploy("blog", dist)
	if err2 == nil || !IsLabelTaken(err2) {
		t.Fatalf("fallback IsLabelTaken failed: %v", err2)
	}
	// deploy_failed carries code and no internals.
	failsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"deploy failed","code":"deploy_failed"}`))
	}))
	defer failsrv.Close()
	_, err3 := New(failsrv.URL, "tok").Deploy("blog", dist)
	if err3 == nil || CodeOf(err3) != "deploy_failed" {
		t.Fatalf("deploy_failed code missing: %v", err3)
	}
	if strings.Contains(err3.Error(), "supabase") || strings.Contains(err3.Error(), "PostgREST") {
		t.Fatalf("deploy_failed leaks internals: %v", err3)
	}
}

func TestSessionSitesPreserveCodes(t *testing.T) {
	isolateSession(t)
	// Login preserves invalid_credentials.
	loginsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid email or password","code":"invalid_credentials"}`))
	}))
	defer loginsrv.Close()
	err := New(loginsrv.URL, "").Login("a@b.c", "wrong")
	if err == nil || CodeOf(err) != "invalid_credentials" {
		t.Fatalf("login code = %q, want invalid_credentials (%v)", CodeOf(err), err)
	}
	// Sites list preserves login_required.
	sitesrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"login required","code":"login_required"}`))
	}))
	defer sitesrv.Close()
	_, err2 := New(sitesrv.URL, "tok").List()
	if err2 == nil || CodeOf(err2) != "login_required" {
		t.Fatalf("list code = %q, want login_required (%v)", CodeOf(err2), err2)
	}
}
