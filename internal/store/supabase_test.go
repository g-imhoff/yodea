package store

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Supabase backend must scope every call with the project key and the
// viewer's JWT so PostgREST RLS sees the caller; personal-rows-only must
// never depend on client goodwill.
func TestSupabaseStoreScopesCalls(t *testing.T) {
	var sawKey, sawAuth, sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey, sawAuth = r.Header.Get("apikey"), r.Header.Get("Authorization")
		sawPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"label":"alice-blog","user_id":"alice","project":"blog","files":1,"bytes":2}]`))
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	list := s.ListSites("viewer-jwt", "alice")
	if len(list) != 1 || list[0].Label != "alice-blog" {
		t.Fatalf("list = %+v", list)
	}
	if sawKey != "server-key" {
		t.Fatalf("apikey = %q, want server key", sawKey)
	}
	if sawAuth != "Bearer viewer-jwt" {
		t.Fatalf("auth = %q, want viewer JWT", sawAuth)
	}
	if !strings.Contains(sawPath, "/rest/v1/sites") || !strings.Contains(sawPath, "user_id") {
		t.Fatalf("path = %q, want sites scoped by user", sawPath)
	}
}

// Zero-row RLS guard: a DELETE that affects no rows (RLS denial or
// concurrent delete) must surface as nil so the handler does not delete
// files on a no-op.
func TestSupabaseDeleteZeroRowsReturnsNil(t *testing.T) {
	var sawDeletePrefer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"label":"alice-blog","user_id":"alice","project":"blog","files":1,"bytes":2}]`))
			return
		}
		if r.Method == http.MethodDelete {
			sawDeletePrefer = r.Header.Get("Prefer")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	if got, err := s.DeleteSite("viewer-jwt", "alice", "blog"); err != nil || got != nil {
		t.Fatalf("DeleteSite on 0-row delete = (%+v,%v), want (nil,nil) (no file cleanup)", got, err)
	}
	if sawDeletePrefer != "return=representation" {
		t.Fatalf("DELETE Prefer = %q, want return=representation", sawDeletePrefer)
	}
}

func TestSupabaseUpsertRejectsTakenLabelWithoutWriting(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"label":"shared","user_id":"alice","project":"a","files":1,"bytes":2}]`))
			return
		}
		if r.Method == http.MethodPost {
			posts++
			_, _ = w.Write([]byte(`[{"label":"shared","user_id":"bob","project":"b"}]`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	err := s.UpsertSite("viewer-jwt", &Site{UserID: "bob", Project: "b", Label: "shared"})
	if err == nil || !strings.Contains(err.Error(), "label is taken") {
		t.Fatalf("UpsertSite on taken label = %v, want label-taken error", err)
	}
	if posts != 0 {
		t.Fatalf("UpsertSite wrote %d POSTs after label-taken pre-check, want 0", posts)
	}
}

func TestSupabaseUpsertRequiresRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	if err := s.UpsertSite("viewer-jwt", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err == nil {
		t.Fatal("UpsertSite with empty upsert response = nil, want error")
	}
}

func TestSupabaseStoreFavoriteRoundTrip(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			bodies = append(bodies, string(buf))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Favorite{{UserID: "bob", Label: "alice-blog"}})
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	if err := s.AddFavorite("viewer-jwt", "bob", "alice-blog"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "alice-blog") {
		t.Fatalf("post body = %q", bodies)
	}
	if got := s.ListFavorites("viewer-jwt", "bob"); len(got) != 1 {
		t.Fatalf("favorites = %+v", got)
	}
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("ConfigFromEnv should report missing without env")
	}
	t.Setenv("SUPABASE_URL", "https://xyz.supabase.co/")
	t.Setenv("SUPABASE_SERVICE_KEY", "svc")
	cfg, ok := ConfigFromEnv()
	if !ok || cfg.URL != "https://xyz.supabase.co" || cfg.Key != "svc" {
		t.Fatalf("env config = %+v %v", cfg, ok)
	}
}
