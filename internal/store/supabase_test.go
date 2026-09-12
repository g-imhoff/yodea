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

// Empty caller JWTs must never fall back to service_role: writes error and
// reads resolve without any HTTP request.
func TestSupabaseRejectsEmptyTokenWithoutRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err == nil ||
		!strings.Contains(err.Error(), "caller JWT") {
		t.Fatalf("UpsertSite empty token = %v, want caller-JWT error", err)
	}
	if err := s.UpsertSite(SystemToken, &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err == nil {
		t.Fatalf("UpsertSite system token = nil, want error")
	}
	if got := s.ListSites("", "alice"); got != nil {
		t.Fatalf("ListSites empty token = %+v, want nil", got)
	}
	if err := s.AddFavorite("", "bob", "alice-blog"); err == nil {
		t.Fatal("AddFavorite empty token = nil, want error")
	}
	if ok, _ := s.RemoveFavorite("", "bob", "alice-blog"); ok {
		t.Fatal("RemoveFavorite empty token = true, want false")
	}
	if got, _ := s.DeleteSite("", "alice", "blog"); got != nil {
		t.Fatalf("DeleteSite empty token = %+v, want nil", got)
	}
	if got := s.RecentViews("", "alice", 5); got != nil {
		t.Fatalf("RecentViews empty token = %+v, want nil", got)
	}
	_ = s.RecordView("", "alice", "alice-blog")
	if requests != 0 {
		t.Fatalf("empty-token calls made %d HTTP requests, want 0 (no service_role fallback)", requests)
	}
}

// The Caddy ask existence check has no caller JWT: SiteByLabel with an
// empty or SystemToken token must keep working via the explicit system
// context, while caller JWTs keep scoping Authorization to the viewer.
func TestSupabaseSystemSiteByLabelUsesServerKey(t *testing.T) {
	var sawAuth, sawKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth, sawKey = r.Header.Get("Authorization"), r.Header.Get("apikey")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"label":"alice-blog","user_id":"alice","project":"blog","files":1,"bytes":2}]`))
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	if got := s.SiteByLabel("", "alice-blog"); got == nil || got.Label != "alice-blog" {
		t.Fatalf("SiteByLabel empty = %+v, want alice-blog (system existence check)", got)
	}
	if sawAuth != "Bearer server-key" {
		t.Fatalf("system auth = %q, want explicit server key", sawAuth)
	}
	if got := s.SiteByLabel(SystemToken, "alice-blog"); got == nil {
		t.Fatal("SiteByLabel SystemToken = nil, want row")
	}
	if sawAuth != "Bearer server-key" {
		t.Fatalf("SystemToken auth = %q, want explicit server key", sawAuth)
	}
	if got := s.SiteByLabel("viewer-jwt", "alice-blog"); got == nil {
		t.Fatal("SiteByLabel viewer = nil, want row")
	}
	if sawAuth != "Bearer viewer-jwt" || sawKey != "server-key" {
		t.Fatalf("viewer auth/key = %q/%q, want viewer JWT + server apikey", sawAuth, sawKey)
	}
}

// ConfigFromEnv must match server.New: a server-side key is required, the
// anon key alone is not enough for the store.
func TestSupabaseConfigRequiresServiceKey(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://xyz.supabase.co/")
	t.Setenv("SUPABASE_SERVICE_KEY", "")
	t.Setenv("SUPABASE_ANON_KEY", "anon")
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("ConfigFromEnv with only anon key = ok, want false (needs service key)")
	}
}

// A failing precheck must abort the upsert without writing.
func TestSupabaseUpsertAbortsOnPrecheckError(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		if r.Method == http.MethodPost {
			posts++
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	if err := s.UpsertSite("viewer-jwt", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err == nil {
		t.Fatal("UpsertSite on precheck error = nil, want error")
	}
	if posts != 0 {
		t.Fatalf("UpsertSite wrote %d POSTs after failed precheck, want 0", posts)
	}
}

// Concurrent deploys racing on one free label must surface the local
// backend's exact `label is taken` string.
func TestSupabaseUpsertMapsConflictToLabelTaken(t *testing.T) {
	t.Run("http409", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"duplicate key value violates unique constraint"}`))
				return
			}
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}))
		defer srv.Close()

		s := NewSupabaseStore(srv.URL, "server-key")
		err := s.UpsertSite("viewer-jwt", &Site{UserID: "bob", Project: "b", Label: "shared"})
		if err == nil || !strings.Contains(err.Error(), "label is taken") {
			t.Fatalf("UpsertSite on 409 = %v, want label is taken", err)
		}
	})

	t.Run("zeroRowsRace", func(t *testing.T) {
		gets := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				gets++
				if gets == 1 {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				_, _ = w.Write([]byte(`[{"label":"shared","user_id":"alice","project":"a","files":1,"bytes":2}]`))
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
		err := s.UpsertSite("viewer-jwt", &Site{UserID: "bob", Project: "b", Label: "shared"})
		if err == nil || !strings.Contains(err.Error(), "label is taken") {
			t.Fatalf("UpsertSite on zero-row race = %v, want label is taken", err)
		}
	})
}

// The view trim must page past ViewCap with a stable order, select ids
// only with a bounded limit, and delete the overflow in one statement.
func TestSupabaseRecordViewTrimBatched(t *testing.T) {
	var trimOrder, trimSelect, trimOffset, trimLimit string
	var delID, delUser string
	deletes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/site_views"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/site_views"):
			qq := r.URL.Query()
			trimOrder, trimSelect, trimOffset, trimLimit = qq.Get("order"), qq.Get("select"), qq.Get("offset"), qq.Get("limit")
			_, _ = w.Write([]byte(`[{"id":101},{"id":102}]`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/site_views"):
			deletes++
			qq := r.URL.Query()
			delID, delUser = qq.Get("id"), qq.Get("user_id")
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	s.RecordView("viewer-jwt", "alice", "lbl")
	if trimOrder != "at.desc,id.desc" {
		t.Fatalf("trim order = %q, want at.desc,id.desc tiebreaker", trimOrder)
	}
	if trimSelect != "id" {
		t.Fatalf("trim select = %q, want id only", trimSelect)
	}
	if trimOffset != "50" {
		t.Fatalf("trim offset = %q, want ViewCap (50)", trimOffset)
	}
	if trimLimit == "" {
		t.Fatal("trim limit missing, want a bounded limit")
	}
	if deletes != 1 {
		t.Fatalf("trim deletes = %d, want 1 batched statement", deletes)
	}
	if !strings.Contains(delID, "101") || !strings.Contains(delID, "102") {
		t.Fatalf("delete id filter = %q, want both overflow ids in one statement", delID)
	}
	if delUser == "" {
		t.Fatalf("delete missing user scope: %q", delUser)
	}
	if err := s.RecordView("viewer-jwt", "alice", "lbl"); err != nil {
		t.Fatalf("RecordView on healthy fake = %v, want nil", err)
	}

	// Trim listing failures must surface instead of deleting row-by-row
	// while ignoring errors.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer bad.Close()
	sb := NewSupabaseStore(bad.URL, "server-key")
	if err := sb.RecordView("viewer-jwt", "alice", "lbl"); err == nil {
		t.Fatal("RecordView on trim failure = nil, want error")
	}
}

// RecentViews clamps n to 0..ViewReturn and avoids requests for n<=0.
func TestSupabaseRecentViewsClamp(t *testing.T) {
	var sawLimit, sawOrder string
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		qq := r.URL.Query()
		sawLimit, sawOrder = qq.Get("limit"), qq.Get("order")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	if got := s.RecentViews("viewer-jwt", "alice", -5); len(got) != 0 {
		t.Fatalf("RecentViews(-5) = %+v, want empty", got)
	}
	if requests != 0 {
		t.Fatalf("RecentViews(-5) made %d requests, want 0", requests)
	}
	if got := s.RecentViews("viewer-jwt", "alice", 0); len(got) != 0 {
		t.Fatalf("RecentViews(0) = %+v, want empty", got)
	}
	if requests != 0 {
		t.Fatalf("RecentViews(0) made %d requests, want 0", requests)
	}
	_ = s.RecentViews("viewer-jwt", "alice", 1000)
	if sawLimit != "20" {
		t.Fatalf("RecentViews(1000) limit = %q, want ViewReturn (20)", sawLimit)
	}
	_ = s.RecentViews("viewer-jwt", "alice", 5)
	if sawLimit != "5" {
		t.Fatalf("RecentViews(5) limit = %q, want 5", sawLimit)
	}
	if sawOrder != "at.desc,id.desc" {
		t.Fatalf("RecentViews order = %q, want stable at.desc,id.desc", sawOrder)
	}
}

// Deleted previews must drop out of Supabase favorites via read-time
// filtering against existing sites.
func TestSupabaseListFavoritesFiltersOrphans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/favorites") {
			_, _ = w.Write([]byte(`[{"user_id":"bob","label":"kept"},{"user_id":"bob","label":"gone"}]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sites") {
			qq := r.URL.Query()
			if qq.Get("select") != "label" {
				t.Errorf("existence check select = %q, want label", qq.Get("select"))
			}
			if !strings.Contains(qq.Get("label"), "kept") || !strings.Contains(qq.Get("label"), "gone") {
				t.Errorf("existence check labels = %q, want both favorites in one query", qq.Get("label"))
			}
			_, _ = w.Write([]byte(`[{"label":"kept"}]`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	got := s.ListFavorites("viewer-jwt", "bob")
	if len(got) != 1 || got[0].Label != "kept" {
		t.Fatalf("filtered favorites = %+v, want only kept", got)
	}
}

// Shared favorites must survive the orphan filter even when RLS hides the
// site from the viewer JWT: the label-existence check runs with the system
// key, so only genuinely deleted labels drop out.
func TestSupabaseListFavoritesKeepsSharedSiteHiddenFromViewer(t *testing.T) {
	var existAuth, favAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/favorites") {
			favAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`[{"user_id":"bob","label":"shared"},{"user_id":"bob","label":"gone"}]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sites") {
			existAuth = r.Header.Get("Authorization")
			qq := r.URL.Query()
			if qq.Get("select") != "label" {
				t.Errorf("existence check select = %q, want label", qq.Get("select"))
			}
			if existAuth == "Bearer server-key" {
				// System sees the shared site; gone is deleted for everyone.
				_, _ = w.Write([]byte(`[{"label":"shared"}]`))
				return
			}
			// Viewer-scoped RLS hides the other owner's site.
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	s := NewSupabaseStore(srv.URL, "server-key")
	got := s.ListFavorites("viewer-jwt", "bob")
	if favAuth != "Bearer viewer-jwt" {
		t.Fatalf("favorites auth = %q, want viewer JWT", favAuth)
	}
	if existAuth != "Bearer server-key" {
		t.Fatalf("existence auth = %q, want system key (viewer RLS would hide shared sites)", existAuth)
	}
	if len(got) != 1 || got[0].Label != "shared" {
		t.Fatalf("favorites = %+v, want only shared (gone is a genuine orphan)", got)
	}
}

// Parity: ListFavorites after DeleteSite drops the deleted preview on both
// backends (local prunes eagerly, Supabase filters orphans on read).
func TestListFavoritesDeleteParity(t *testing.T) {
	local, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := local.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err != nil {
		t.Fatal(err)
	}
	if err := local.AddFavorite("", "bob", "alice-blog"); err != nil {
		t.Fatal(err)
	}
	if got, err := local.DeleteSite("", "alice", "blog"); err != nil || got == nil {
		t.Fatalf("local delete = (%+v,%v), want row", got, err)
	}
	if got := local.ListFavorites("", "bob"); len(got) != 0 {
		t.Fatalf("local favorites after delete = %+v, want empty", got)
	}

	sites := map[string]Site{"alice-blog": {UserID: "alice", Project: "blog", Label: "alice-blog"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sites") && r.URL.Query().Get("select") == "label":
			var existing []map[string]string
			for _, want := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(r.URL.Query().Get("label"), "in.("), ")"), ",") {
				if s, ok := sites[want]; ok {
					existing = append(existing, map[string]string{"label": s.Label})
				}
			}
			if existing == nil {
				existing = []map[string]string{}
			}
			_ = json.NewEncoder(w).Encode(existing)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sites"):
			// DeleteSite lookup by user_id+project.
			for _, s := range sites {
				if "eq."+s.UserID == r.URL.Query().Get("user_id") && "eq."+s.Project == r.URL.Query().Get("project") {
					_ = json.NewEncoder(w).Encode([]Site{s})
					return
				}
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/sites"):
			label := strings.TrimPrefix(r.URL.Query().Get("label"), "eq.")
			if s, ok := sites[label]; ok {
				delete(sites, label)
				_ = json.NewEncoder(w).Encode([]Site{s})
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/favorites"):
			// Supabase leaves the orphan row; ListFavorites must filter it.
			_ = json.NewEncoder(w).Encode([]Favorite{{UserID: "bob", Label: "alice-blog"}})
		default:
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	remote := NewSupabaseStore(srv.URL, "server-key")
	if got, err := remote.DeleteSite("viewer-jwt", "alice", "blog"); err != nil || got == nil {
		t.Fatalf("supabase delete = (%+v,%v), want row", got, err)
	}
	if got := remote.ListFavorites("viewer-jwt", "bob"); len(got) != 0 {
		t.Fatalf("supabase favorites after delete = %+v, want empty (orphan filtered)", got)
	}
}
