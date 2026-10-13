// Package server is the yodead HTTP backend: central dashboard plus login,
// session API, site deploys, preview subdomains, views history, and
// favorites.
//
// Host model: the central host (BaseDomain, e.g. previews.example.com) serves the
// dashboard, /login, and /api/*. Each preview lives at
// <label>.<BaseDomain> and serves that label's static dist to any logged-in
// user. One session cookie (Domain=.<BaseDomain>) covers both.
//
// Sandbox model: uploaded code is static data only and is never executed by
// this server. Preview reads deny dot segments and traversal before touching
// disk, send nosniff plus conservative MIME types, sandbox allow-scripts
// allow-same-origin plus X-Frame-Options DENY so previews stay unframeable,
// and fall back to index.html only for extensionless navigations so missing
// assets stay visible 404s. Previews keep their own origin
// (<label>.<BaseDomain>), so preview JavaScript runs same-origin with its
// own host. The central API stays out of reach: session cookies are
// HttpOnly plus SameSite=Lax so preview JavaScript cannot read them,
// cookie-authed /api/* writes need the CSRF synchronizer token, and the
// /api/* session and data endpoints answer only on the central host
// (requireCentralHost).
//
// Run-safe note: production runs this binary as an unprivileged user with a
// data-only writable sites directory (DataDir/sites); metadata is
// DataDir/db.json plus DataDir/presets.json (0600) or Supabase when
// YODEA_STORE=supabase.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/g-imhoff/yodea/internal/auth"
	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

// Config wires the server. SupabaseURL plus AnonKey come from env and the
// anon key is public by design; SupabaseKey (service role) is server-side
// only and never touches cookies or response bodies.
type Config struct {
	Addr          string
	DataDir       string
	BaseDomain    string // e.g. previews.example.com
	SupabaseURL   string
	AnonKey       string
	SupabaseKey   string
	StoreBackend  string // "local" (default) or "supabase"
	DevNoAuth     bool
	SecureCookies bool

	AllowInsecureDevPublic bool
}

// Server serves the API, the dashboard, and the preview subdomains.
type Server struct {
	cfg      Config
	verifier *auth.Verifier
	metadb   store.Storage
	authc    *auth.Client
	mux      *http.ServeMux
	// askLimiter is the per-IP token bucket for /api/caddy-ask.
	askLimiter *caddyAskLimiter
	// srv is the live listener once Run starts, so Shutdown can drain
	// it on SIGINT/SIGTERM. Guarded by mu.
	mu  sync.Mutex
	srv *http.Server
}

func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8093"
	}
	if cfg.DataDir == "" {
		return nil, errors.New("data dir is required")
	}
	if cfg.BaseDomain == "" {
		return nil, errors.New("base domain is required")
	}
	// Production guard: DevNoAuth accepts forged `dev` / `dev:<id>`
	// tokens, so refuse to start unless the bind is loopback AND the
	// domain is a test/local name, or AllowInsecureDevPublic is set.
	// Lives in New so all binaries share it.
	if cfg.DevNoAuth && cfg.AllowInsecureDevPublic {
		log.Printf("WARNING: INSECURE: dev mode on public domain %q (bind %s) via --allow-insecure-dev-public: dev tokens (dev, dev:<id>) are forgeable, anyone who can reach this server can act as any user", cfg.BaseDomain, cfg.Addr)
	} else if cfg.DevNoAuth {
		if !isLoopbackBind(cfg.Addr) {
			return nil, fmt.Errorf("dev mode refused: bind address %q is not loopback (must bind 127.0.0.1, ::1, or localhost)", cfg.Addr)
		}
		if !isTestDomain(cfg.BaseDomain) {
			return nil, fmt.Errorf("dev mode refused: base domain %q is not a test/local name (must end .test, or be localhost, or start 127./::1)", cfg.BaseDomain)
		}
	}
	// Lowercase for case-insensitive DNS comparison; preview routing and
	// safeNext compare against this normalized form.
	cfg.BaseDomain = strings.ToLower(strings.TrimSpace(cfg.BaseDomain))
	if !validBaseDomain(cfg.BaseDomain) {
		return nil, fmt.Errorf("invalid base domain %q: want a DNS suffix (letters/digits/hyphens/dots, max 253 chars) or 127.0.0.1/localhost for dev", cfg.BaseDomain)
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), askLimiter: newCaddyAskLimiter()}
	if cfg.DevNoAuth {
		s.verifier = auth.NewDevVerifier("dev-user")
	} else {
		if cfg.SupabaseURL == "" {
			return nil, errors.New("SUPABASE_URL is required (or --dev for local testing)")
		}
		v, err := auth.NewVerifier(
			strings.TrimSuffix(cfg.SupabaseURL, "/")+"/auth/v1/.well-known/jwks.json",
			strings.TrimSuffix(cfg.SupabaseURL, "/")+"/auth/v1",
		)
		if err != nil {
			return nil, err
		}
		s.verifier = v
		s.authc = auth.NewClient(cfg.SupabaseURL, cfg.AnonKey)
	}
	switch cfg.StoreBackend {
	case "", "local":
		st, err := store.Open(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		s.metadb = st
	case "supabase":
		if cfg.SupabaseURL == "" || cfg.SupabaseKey == "" {
			return nil, errors.New("supabase store needs SUPABASE_URL plus SUPABASE_SERVICE_KEY (service role; the anon key is not enough for server-side writes)")
		}
		s.metadb = store.NewSupabaseStore(cfg.SupabaseURL, cfg.SupabaseKey)
	default:
		return nil, fmt.Errorf("unknown store backend %q", cfg.StoreBackend)
	}
	// Reclaim crashed deploy leftovers (.stage-*, .old-*) under DataDir/sites.
	// Best-effort: a cleanup failure must not prevent startup. Never touches live dest.
	if err := sites.CleanupLeftovers(cfg.DataDir); err != nil {
		log.Printf("sites cleanup: %v", err)
	}
	if !HasAssets() {
		// Degraded UI, live API: the dashboard bundle is copied into
		// internal/server/web/dist by the UI build before `go build`.
		// Never fail fast here; previews and the API stay up.
		log.Printf("warning: no embedded dashboard UI (web/dist/index.html missing); /login and / serve an explanatory message, API and previews stay live")
	}
	s.routes()
	return s, nil
}

// Handler exposes the root handler Run serves, for tests.
func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if cookieCount(r, "yodea_session") > 1 {
		s.expireTossedCookies(w, r, "yodea_session", true, true)
	}
	if cookieCount(r, "yodea_refresh") > 1 {
		s.expireTossedCookies(w, r, "yodea_refresh", false, false)
	}
	if label, ok := s.previewLabel(r.Host); ok && (isPreviewAPIPath(r.URL.Path) || isPreviewAPIPath(path.Clean(r.URL.Path))) {
		s.handlePreviewAPI(w, r, label)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Close stops background refresh loops and releases the metadata store's
// single-writer lock so the same DataDir can be reopened in-process.
func (s *Server) Close() {
	if s.verifier != nil {
		s.verifier.Close()
	}
	if s.metadb != nil {
		if c, ok := s.metadb.(io.Closer); ok {
			_ = c.Close()
		}
	}
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("/healthz", s.handleHealth)
	m.HandleFunc("/api/config", s.handleConfig)
	m.HandleFunc("/api/session", s.handleSession)
	m.HandleFunc("/api/session/refresh", s.handleRefresh)
	m.HandleFunc("/api/logout", s.handleLogout)
	m.HandleFunc("/api/sites", s.handleSites)
	m.HandleFunc("/api/sites/", s.handleSite)
	m.HandleFunc("/api/views", s.handleViews)
	m.HandleFunc("/api/favorites", s.handleFavorites)
	m.HandleFunc("/api/favorites/", s.handleFavorite)
	m.HandleFunc("/api/caddy-ask", s.handleCaddyAsk)
	m.HandleFunc("/login", s.handleLoginPage)
	m.HandleFunc("/", s.handleRoot)
}

// Run serves until interrupted or Shutdown is called. A Shutdown-driven
// stop reports nil (not http.ErrServerClosed) so signal exits stay quiet;
// any other error (bind failure, ...) is returned as is.
func (s *Server) Run() error {
	srv := &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      s,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
	log.Printf("yodead listening on %s for %s (store=%s dev=%v assets=%v)",
		s.cfg.Addr, s.cfg.BaseDomain, s.storeName(), s.cfg.DevNoAuth, HasAssets())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown drains in-flight requests (deploys, db.json writes) until ctx
// expires. main calls it on SIGINT/SIGTERM; Run then returns nil.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Server) storeName() string {
	if s.cfg.StoreBackend == "supabase" {
		return "supabase"
	}
	return "local"
}
