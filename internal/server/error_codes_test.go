package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type errBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func decodeErr(t *testing.T, recBody string) errBody {
	t.Helper()
	var b errBody
	if err := json.Unmarshal([]byte(recBody), &b); err != nil {
		t.Fatalf("error body is not JSON {error,code}: %v (%q)", err, recBody)
	}
	if b.Error == "" {
		t.Fatalf("error body missing error field: %q", recBody)
	}
	if b.Code == "" {
		t.Fatalf("error body missing code field: %q", recBody)
	}
	return b
}

func assertCode(t *testing.T, recCode int, recBody, wantCode string, wantStatus int) errBody {
	t.Helper()
	if recCode != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", recCode, wantStatus, recBody)
	}
	b := decodeErr(t, recBody)
	if b.Code != wantCode {
		t.Fatalf("code = %q, want %q (body %s)", b.Code, wantCode, recBody)
	}
	return b
}

// Frozen four: session.
func TestErrorCodeSession(t *testing.T) {
	s := newTestServer(t)
	// Unauthenticated sites list.
	rec := doReq(s, http.MethodGet, testDomain, "/api/sites", "", nil, "")
	assertCode(t, rec.Code, rec.Body.String(), "login_required", http.StatusUnauthorized)
	// Bad login body in dev mode: empty email.
	rec = doReq(s, http.MethodPost, testDomain, "/api/session", "",
		strings.NewReader(`{}`), "application/json")
	assertCode(t, rec.Code, rec.Body.String(), "bad_request", http.StatusBadRequest)
	// Wrong method.
	rec = doReq(s, http.MethodGet, testDomain, "/api/session", "", nil, "")
	assertCode(t, rec.Code, rec.Body.String(), "method_not_allowed", http.StatusMethodNotAllowed)
}

// Frozen four: sites + deploy validation.
func TestErrorCodeDeployValidation(t *testing.T) {
	s := newTestServer(t)
	tok := devToken(t, "alice@example.com")
	// Bad project name.
	rec := deploy(t, s, tok, "-bad-", tarGz(t, map[string]string{"index.html": "x"}))
	assertCode(t, rec.Code, rec.Body.String(), "bad_project", http.StatusBadRequest)
	// Rejected upload: missing index.
	rec = deploy(t, s, tok, "blog", tarGz(t, map[string]string{"app.js": "x"}))
	b := assertCode(t, rec.Code, rec.Body.String(), "rejected_upload", http.StatusBadRequest)
	if !strings.Contains(b.Error, "rejected upload") {
		t.Fatalf("rejected_upload message = %q, want it to mention rejected upload", b.Error)
	}
	// Unauthenticated deploy.
	rec = deploy(t, s, "", "blog", tarGz(t, map[string]string{"index.html": "x"}))
	assertCode(t, rec.Code, rec.Body.String(), "login_required", http.StatusUnauthorized)
}

// Frozen four: deploy conflict carries label_taken with substring fallback.
func TestErrorCodeLabelTaken(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "v1"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup deploy = %d: %s", rec.Code, rec.Body.String())
	}
	orig := s.metadb
	s.metadb = &failUpsertStore{Storage: orig, err: errors.New("label is taken")}
	defer func() { s.metadb = orig }()
	rec2 := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "v2"}))
	b := assertCode(t, rec2.Code, rec2.Body.String(), "label_taken", http.StatusConflict)
	if !strings.Contains(b.Error, "label is taken") {
		t.Fatalf("label_taken message = %q, want substring label is taken for fallback", b.Error)
	}
}

// Storage/PostgREST internals must not leak: deploy_failed is generic,
// detail stays server-side (logged, not forwarded).
func TestErrorCodeDeployFailedSanitized(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	orig := s.metadb
	leak := "supabase PostgREST password=secret xyz"
	s.metadb = &failUpsertStore{Storage: orig, err: errors.New(leak)}
	defer func() { s.metadb = orig }()
	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "x"}))
	b := assertCode(t, rec.Code, rec.Body.String(), "deploy_failed", http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "supabase") || strings.Contains(rec.Body.String(), "PostgREST") {
		t.Fatalf("deploy_failed body leaks storage internals: %q", rec.Body.String())
	}
	if b.Error != "deploy failed" {
		t.Fatalf("deploy_failed message = %q, want generic %q", b.Error, "deploy failed")
	}
}

// Frozen four: delete.
func TestErrorCodeDelete(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	rec := doReq(s, http.MethodDelete, testDomain, "/api/sites/missing", alice, nil, "")
	assertCode(t, rec.Code, rec.Body.String(), "no_such_project", http.StatusNotFound)
	rec = doReq(s, http.MethodDelete, testDomain, "/api/sites/-bad-", alice, nil, "")
	assertCode(t, rec.Code, rec.Body.String(), "bad_project", http.StatusBadRequest)
}

// Favorites.
func TestErrorCodeFavorites(t *testing.T) {
	s := newTestServer(t)
	bob := devToken(t, "bob@example.com")
	rec := doReq(s, http.MethodPost, testDomain, "/api/favorites", bob,
		strings.NewReader(`{"label":"no-such-site"}`), "application/json")
	assertCode(t, rec.Code, rec.Body.String(), "unknown_site", http.StatusNotFound)
	rec = doReq(s, http.MethodDelete, testDomain, "/api/favorites/no-such-site", bob, nil, "")
	assertCode(t, rec.Code, rec.Body.String(), "no_such_favorite", http.StatusNotFound)
}

// Status table stays frozen per condition.
func TestErrorCodeStatusTable(t *testing.T) {
	cases := map[string]int{
		"login_required":      http.StatusUnauthorized,
		"invalid_credentials": http.StatusUnauthorized,
		"auth_expired":        http.StatusUnauthorized,
		"csrf_required":       http.StatusForbidden,
		"bad_request":         http.StatusBadRequest,
		"bad_project":         http.StatusBadRequest,
		"rejected_upload":     http.StatusBadRequest,
		"label_taken":         http.StatusConflict,
		"unknown_site":        http.StatusNotFound,
		"no_such_project":     http.StatusNotFound,
		"no_such_favorite":    http.StatusNotFound,
		"not_found":           http.StatusNotFound,
		"upload_too_large":    http.StatusRequestEntityTooLarge,
		"deploy_failed":       http.StatusInternalServerError,
		"unknown":             http.StatusInternalServerError,
		"method_not_allowed":  http.StatusMethodNotAllowed,
		"auth_not_configured": http.StatusServiceUnavailable,
		"verify_failed":       http.StatusBadGateway,
	}
	for code, want := range cases {
		if got := statusForCode(code); got != want {
			t.Errorf("statusForCode(%q) = %d, want %d", code, got, want)
		}
	}
}
