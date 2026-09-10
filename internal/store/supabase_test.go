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
