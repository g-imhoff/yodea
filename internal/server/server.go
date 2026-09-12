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
// (no allow-same-origin) plus X-Frame-Options DENY so sibling previews under
// the shared parent domain stay opaque and unframeable, and fall back to
// index.html only for extensionless navigations so missing assets stay
// visible 404s. Session cookies are HttpOnly plus SameSite=Lax so preview
// JavaScript cannot read them.
//
// Run-safe note: production runs this binary as an unprivileged user with a
// data-only writable sites directory (DataDir/sites); metadata is
// DataDir/db.json (0600) or Supabase when YODEA_STORE=supabase.
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
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
}

// Server serves the API, the dashboard, and the preview subdomains.
type Server struct {
	cfg      Config
	verifier *auth.Verifier
	metadb   store.Storage
	authc    *auth.Client
	mux      *http.ServeMux
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
	// domain is a test/local name. Lives in New so all binaries share it.
	if cfg.DevNoAuth {
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
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
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
		key := cfg.SupabaseKey
		if key == "" {
			// Match ConfigFromEnv: the anon key works as the server-side
			// key (leaning fully on RLS) so local/dev boots agree.
			key = cfg.AnonKey
		}
		if cfg.SupabaseURL == "" || key == "" {
			return nil, errors.New("supabase store needs SUPABASE_URL plus a server-side key")
		}
		s.metadb = store.NewSupabaseStore(cfg.SupabaseURL, key)
	default:
		return nil, fmt.Errorf("unknown store backend %q", cfg.StoreBackend)
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

// Handler exposes the mux for tests.
func (s *Server) Handler() http.Handler { return s.mux }

// Close stops background refresh loops.
func (s *Server) Close() {
	s.verifier.Close()
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
		Handler:      s.mux,
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// tokenFor authenticates API callers via Bearer header, falling back to the
// session cookie for browser dashboard calls.
func (s *Server) tokenFor(r *http.Request) (token, userID string, err error) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	} else if c, cerr := r.Cookie("yodea_session"); cerr == nil {
		token = c.Value
	}
	if token == "" {
		return "", "", errors.New("login required")
	}
	userID, err = s.verifier.Verify(token)
	if err != nil {
		return "", "", err
	}
	return token, userID, nil
}

// handleHealth is readiness, not just liveness: 200 {"status":"ok"}
// only when the data dir is usable and the metadata store answers. Dead
// disk or dead store reads 503 so traffic moves elsewhere.
// csrfHeaderName carries the synchronizer token for cookie-authed writes.
const csrfHeaderName = "X-Yodea-CSRF"

// csrfTokenFor derives the synchronizer token statelessly from the access
// token: hex(sha256(access_token))[:32]. No server secret is stored; the
// dashboard keeps the value in JS memory (set at login, cleared on logout)
// and echoes it back on state-changing /api/* calls. Preview JavaScript
// cannot read the HttpOnly session cookie, so it cannot recompute this.
func csrfTokenFor(access string) string {
	sum := sha256.Sum256([]byte(access))
	return hex.EncodeToString(sum[:])[:32]
}

// checkCSRF enforces the synchronizer token for cookie-authed
// state-changing /api/* requests. Bearer-only callers skip the check
// (Authorization header present means non-ambient credentials). GET/HEAD/
// OPTIONS never mutate, and the token-issuing endpoints (/api/session,
// /api/session/refresh) are exempt so a client can obtain a token. When the
// session cookie is present without a Bearer header, the X-Yodea-CSRF
// header must equal csrfTokenFor(token); otherwise 403.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	if r.URL.Path == "/api/session" || r.URL.Path == "/api/session/refresh" {
		return true
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return true
	}
	c, err := r.Cookie("yodea_session")
	if err != nil {
		// No ambient cookie: Bearer-only or unauthenticated, nothing to forge.
		return true
	}
	if token == "" {
		token = c.Value
	}
	if token == "" {
		return true
	}
	if r.Header.Get(csrfHeaderName) != csrfTokenFor(token) {
		writeErr(w, http.StatusForbidden, "csrf required")
		return false
	}
	return true
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if err := s.checkReady(); err != nil {
		log.Printf("healthz unavailable: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// checkReady probes the data dir (stat, is-dir, writable) plus one store
// read. RecentViews is the read that stays non-nil when healthy on both
// backends (local make()s its result; PostgREST decodes [] to an empty
// non-nil slice), so a nil return means a Supabase transport error: the
// list handlers deliberately tolerate that nil as empty, but readiness
// must not. Empty (len 0) stays healthy here. The empty token leans on
// the server-side key for Supabase, same as handleCaddyAsk.
func (s *Server) checkReady() error {
	info, err := os.Stat(s.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if !info.IsDir() {
		return errors.New("data dir is not a directory")
	}
	// Deploys extract under DataDir/sites and the local store rewrites
	// db.json here, so a read-only DataDir must read unready.
	probe, err := os.CreateTemp(s.cfg.DataDir, ".healthz-*")
	if err != nil {
		return fmt.Errorf("data dir not writable: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	if s.metadb == nil {
		return errors.New("metadata store not configured")
	}
	if s.metadb.RecentViews("", "__healthz__", 1) == nil {
		return errors.New("metadata store unreachable")
	}
	return nil
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, auth.PublicConfig{
		SupabaseURL: s.cfg.SupabaseURL,
		AnonKey:     s.cfg.AnonKey,
	})
}

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) setSessionCookies(w http.ResponseWriter, access string, accessTTL int, refresh string) {
	domain := "." + s.cfg.BaseDomain
	http.SetCookie(w, &http.Cookie{
		Name:     "yodea_session",
		Value:    access,
		Path:     "/",
		Domain:   domain, // one login covers dashboard plus preview subdomains
		MaxAge:   accessTTL,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	if refresh != "" {
		http.SetCookie(w, &http.Cookie{
			Name:  "yodea_refresh",
			Value: refresh,
			// Host-only (no Domain): the long-lived credential must never
			// reach preview subdomains, so a preview hosting path
			// /api/session cannot receive it. Path stays scoped to the
			// refresh endpoint.
			Path:     "/api/session",
			MaxAge:   30 * 24 * 3600,
			HttpOnly: true,
			Secure:   s.cfg.SecureCookies,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body loginBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if s.cfg.DevNoAuth {
		token, userID, err := auth.DevTokenFor(body.Email)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.setSessionCookies(w, token, 3600, "")
		writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "expires_in": 3600, "csrf_token": csrfTokenFor(token)})
		return
	}
	if s.authc == nil {
		writeErr(w, http.StatusServiceUnavailable, "auth not configured")
		return
	}
	sess, err := s.authc.Login(body.Email, body.Password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	userID, err := s.verifier.Verify(sess.AccessToken)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not verify new session")
		return
	}
	s.setSessionCookies(w, sess.AccessToken, sess.ExpiresIn, sess.RefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "expires_in": sess.ExpiresIn, "csrf_token": csrfTokenFor(sess.AccessToken)})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	refresh := ""
	if c, err := r.Cookie("yodea_refresh"); err == nil {
		refresh = c.Value
	} else {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err == nil {
			refresh = body.RefreshToken
		}
	}
	if refresh == "" {
		writeErr(w, http.StatusUnauthorized, "no refresh token")
		return
	}
	if s.cfg.DevNoAuth {
		// Preserve per-viewer identity when the caller still presents
		// its access token (Bearer or session cookie). Without any
		// access token there is no identity to keep: documented
		// fallback is the generic "dev" user (see
		// TestDevRefreshPreservesViewerIdentity).
		if _, userID, err := s.tokenFor(r); err == nil {
			tok := "dev"
			if userID != "dev-user" {
				tok = "dev:" + userID
			}
			s.setSessionCookies(w, tok, 3600, "")
			writeJSON(w, http.StatusOK, map[string]any{"expires_in": 3600, "csrf_token": csrfTokenFor(tok)})
			return
		}
		s.setSessionCookies(w, "dev", 3600, "")
		writeJSON(w, http.StatusOK, map[string]any{"expires_in": 3600, "csrf_token": csrfTokenFor("dev")})
		return
	}
	if s.authc == nil {
		writeErr(w, http.StatusServiceUnavailable, "auth not configured")
		return
	}
	sess, err := s.authc.Refresh(refresh)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "session expired, log in again")
		return
	}
	s.setSessionCookies(w, sess.AccessToken, sess.ExpiresIn, sess.RefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"expires_in": sess.ExpiresIn, "csrf_token": csrfTokenFor(sess.AccessToken)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Cookie-authed writes need the synchronizer token so a same-site
	// preview cannot forge a logout (or any other state change).
	if !s.checkCSRF(w, r, "") {
		return
	}
	// Best-effort server-side revocation before clearing cookies. Skipped
	// in DevNoAuth (no IdP); errors are ignored because cookie clearing is
	// what actually signs the browser out.
	if !s.cfg.DevNoAuth && s.authc != nil {
		var token string
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		} else if c, cerr := r.Cookie("yodea_session"); cerr == nil {
			token = c.Value
		}
		if token != "" {
			s.authc.Logout(token)
		}
	}
	domain := "." + s.cfg.BaseDomain
	for _, c := range []*http.Cookie{
		{Name: "yodea_session", Path: "/"},
		{Name: "yodea_refresh", Path: "/"},
		{Name: "yodea_refresh", Path: "/api/session"},
	} {
		c.Domain = domain
		c.Value = ""
		c.MaxAge = -1
		http.SetCookie(w, c)
	}
	// Clear the host-only refresh cookie (no Domain) alongside the legacy
	// Domain-scoped variants above.
	http.SetCookie(w, &http.Cookie{
		Name:   "yodea_refresh",
		Path:   "/api/session",
		Value:  "",
		MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	list := s.metadb.ListSites(token, userID)
	if list == nil {
		list = []*store.Site{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": list})
}

// handleSite routes POST .../deploy and DELETE .../{project}.
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sites/")
	parts := strings.SplitN(rest, "/", 2)
	project := parts[0]
	if project == "" || strings.Contains(project, "/") {
		writeErr(w, http.StatusBadRequest, "project required")
		return
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "deploy":
		s.handleDeploy(w, r, project)
	case r.Method == http.MethodDelete && len(parts) == 1:
		s.handleDelete(w, r, project)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// validProject delegates to sites.CheckProjectName (single source of
// truth); the label builder sanitizes the rest into DNS-safe form.
func validProject(raw string) bool {
	return sites.CheckProjectName(raw) == nil
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, project string) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	if !s.checkCSRF(w, r, token) {
		return
	}
	if !validProject(project) {
		writeErr(w, http.StatusBadRequest, "bad project name")
		return
	}
	project = sites.Sanitize(project)
	arc, err := readArchive(w, r)
	if err != nil {
		return // readArchive already answered
	}
	label := s.labelFor(token, userID, project)
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, http.StatusBadRequest, "bad label: "+err.Error())
		return
	}
	dest := filepath.Join(s.cfg.DataDir, "sites", label)
	res, err := sites.ExtractDist(bytes.NewReader(arc), dest, 0, 0)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "rejected upload: "+err.Error())
		return
	}
	if err := s.metadb.UpsertSite(token, &store.Site{
		UserID:  userID,
		Project: project,
		Label:   label,
		Files:   res.Files,
		Bytes:   res.Bytes,
	}); err != nil {
		os.RemoveAll(dest)
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	url := "https://" + label + "." + s.cfg.BaseDomain + "/"
	log.Printf("deploy user=%s project=%s label=%s files=%d bytes=%d", userID, project, label, res.Files, res.Bytes)
	writeJSON(w, http.StatusOK, map[string]any{
		"url":     url,
		"project": project,
		"label":   label,
		"files":   res.Files,
		"bytes":   res.Bytes,
	})
}

// readArchive accepts a raw gzipped tar body or a multipart upload with the
// archive in the "archive" (or "file", or first file) part. Oversized
// payloads are rejected before extraction.
func readArchive(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	limit := int64(sites.MaxUploadBytes) + 1
	if ctype := r.Header.Get("Content-Type"); strings.HasPrefix(ctype, "multipart/") {
		// Total cap applies before multipart parsing so a huge ignored
		// field cannot exhaust time/memory while the per-part cap never
		// applies.
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		mr, err := r.MultipartReader()
		if err != nil {
			if isBodyTooLarge(err) {
				writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
			} else {
				writeErr(w, http.StatusBadRequest, "bad multipart body")
			}
			return nil, err
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if isBodyTooLarge(err) {
					writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
				} else {
					writeErr(w, http.StatusBadRequest, "bad multipart body")
				}
				return nil, err
			}
			name, file := part.FormName(), part.FileName()
			if file == "" && name != "archive" && name != "file" {
				n, err := io.Copy(io.Discard, io.LimitReader(part, limit))
				if err != nil {
					if isBodyTooLarge(err) {
						writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
					} else {
						writeErr(w, http.StatusBadRequest, "could not read upload")
					}
					return nil, err
				}
				if n >= limit {
					writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
					return nil, errors.New("upload too large")
				}
				continue
			}
			buf, err := io.ReadAll(io.LimitReader(part, limit))
			if err != nil {
				if isBodyTooLarge(err) {
					writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
				} else {
					writeErr(w, http.StatusBadRequest, "could not read upload")
				}
				return nil, err
			}
			if int64(len(buf)) >= limit {
				writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
				return nil, errors.New("upload too large")
			}
			return buf, nil
		}
		writeErr(w, http.StatusBadRequest, "no archive file in upload")
		return nil, errors.New("no archive part")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	buf, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
		return nil, err
	}
	if int64(len(buf)) >= limit {
		writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
		return nil, errors.New("upload too large")
	}
	return buf, nil
}

func isBodyTooLarge(err error) bool {
	return err != nil && strings.Contains(err.Error(), "request body too large")
}

// labelFor reuses the author's existing label for the same project so
// redeploys overwrite. New projects claim LabelFor(user, project),
// suffixed on collision with another author's label. The suffix range runs
// to 999 with 63-char truncation of the suffixed candidate; genuine
// exhaustion still falls through to base so UpsertSite returns 409.
func (s *Server) labelFor(token, userID, project string) string {
	for _, site := range s.metadb.ListSites(token, userID) {
		if site.Project == project {
			return site.Label
		}
	}
	base := sites.LabelFor(auth.UserPart(userID), project)
	if other := s.metadb.SiteByLabel(token, base); other == nil {
		return base
	}
	for n := 2; n <= 999; n++ {
		suffix := fmt.Sprintf("-%d", n)
		candidate := base + suffix
		if len(candidate) > 63 {
			trunc := strings.TrimRight(base[:63-len(suffix)], "-")
			if trunc == "" {
				trunc = base[:63-len(suffix)]
			}
			candidate = trunc + suffix
		}
		if other := s.metadb.SiteByLabel(token, candidate); other == nil {
			return candidate
		}
	}
	return base // UpsertSite will return 409 on genuine exhaustion.
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, project string) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	if !s.checkCSRF(w, r, token) {
		return
	}
	if !validProject(project) {
		writeErr(w, http.StatusBadRequest, "bad project name")
		return
	}
	site, err := s.metadb.DeleteSite(token, userID, sites.Sanitize(project))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not delete site")
		return
	}
	if site == nil {
		writeErr(w, http.StatusNotFound, "no such project")
		return
	}
	os.RemoveAll(filepath.Join(s.cfg.DataDir, "sites", site.Label))
	log.Printf("delete user=%s project=%s label=%s", userID, site.Project, site.Label)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleViews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	views := s.metadb.RecentViews(token, userID, store.ViewReturn)
	if views == nil {
		views = []store.View{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": views})
}

type favoriteRow struct {
	Label   string `json:"label"`
	Link    string `json:"link"`
	Owner   string `json:"owner"`
	Project string `json:"project"`
}

func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	if r.Method != http.MethodGet && !s.checkCSRF(w, r, token) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := []favoriteRow{}
		for _, f := range s.metadb.ListFavorites(token, userID) {
			// Skip orphans: deleted previews drop out of favorites.
			site := s.metadb.SiteByLabel(token, f.Label)
			if site == nil {
				continue
			}
			out = append(out, favoriteRow{
				Label:   f.Label,
				Link:    "https://" + f.Label + "." + s.cfg.BaseDomain + "/",
				Owner:   site.UserID,
				Project: site.Project,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"favorites": out})
	case http.MethodPost:
		var body struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request")
			return
		}
		if err := sites.ValidateLabel(body.Label); err != nil {
			writeErr(w, http.StatusBadRequest, "bad label: "+err.Error())
			return
		}
		// A viewer may favorite any preview they can open.
		site := s.metadb.SiteByLabel(token, body.Label)
		if site == nil {
			writeErr(w, http.StatusNotFound, "unknown site")
			return
		}
		if err := s.metadb.AddFavorite(token, userID, body.Label); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, favoriteRow{
			Label:   body.Label,
			Link:    "https://" + body.Label + "." + s.cfg.BaseDomain + "/",
			Owner:   site.UserID,
			Project: site.Project,
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	if !s.checkCSRF(w, r, token) {
		return
	}
	label := strings.TrimPrefix(r.URL.Path, "/api/favorites/")
	if strings.Contains(label, "/") || sites.ValidateLabel(label) != nil {
		writeErr(w, http.StatusBadRequest, "bad label")
		return
	}
	ok, err := s.metadb.RemoveFavorite(token, userID, label)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not remove favorite")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such favorite")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleCaddyAsk answers Caddy's on-demand TLS `ask` checks: 200 when
// domain is a live preview subdomain of this server
// (<label>.<BaseDomain> with an existing site), 404 otherwise. It takes
// no auth: Caddy calls it back-channel over the container network, and it
// reveals at most whether a preview exists (the preview URL is equally
// guessable, and previews themselves are team-visible by design).
func (s *Server) handleCaddyAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	domain, ok := normalizeHost(r.URL.Query().Get("domain"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not a preview host")
		return
	}
	label, ok := strings.CutSuffix(domain, "."+strings.ToLower(s.cfg.BaseDomain))
	if !ok || label == "" || strings.Contains(label, ".") {
		writeErr(w, http.StatusNotFound, "not a preview host")
		return
	}
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, http.StatusNotFound, "bad label")
		return
	}
	// Existence check only. The token is empty so the local store ignores
	// it; the Supabase store falls back to its server-side key, which is
	// correct here because any existing site may get a certificate.
	if s.metadb.SiteByLabel("", label) == nil {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// safeNext mirrors the dashboard's allowlist: a relative central path with
// a single leading slash, or an https URL on the central host only.
// Preview-subdomain targets are rejected (fall back to "/") so a crafted
// ?next= can never bounce a fresh login into attacker-controlled preview
// content; the login page then strips the value and redirects to bare
// /login. Anything else falls back to "/" for the same reason.
func (s *Server) safeNext(raw string) string {
	if raw == "" {
		return "/"
	}
	// Browsers normalize backslashes to slashes, so reject them plus
	// control chars and encoded separators/nulls in any candidate before
	// allowing it, in both the relative and absolute branches below.
	if strings.Contains(raw, "\\") {
		return "/"
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	lowered := strings.ToLower(raw)
	if strings.Contains(lowered, "%5c") || strings.Contains(lowered, "%2f") || strings.Contains(lowered, "%00") {
		return "/"
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	if strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" {
			return "/"
		}
		// Reject userinfo/ports explicitly before host equality: parsing
		// keeps them out of Hostname, so checking after EqualFold could
		// never fire.
		if u.User != nil || u.Port() != "" || strings.Contains(u.Host, "@") || strings.Contains(u.Host, ":") {
			return "/"
		}
		if strings.EqualFold(u.Hostname(), s.cfg.BaseDomain) {
			return raw
		}
	}
	return "/"
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	host, ok := normalizeHost(r.Host)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	if host != s.cfg.BaseDomain {
		// Deep-link return: preview hosts bounce to the central login with
		// the original preview URL preserved for post-login return.
		next := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, "https://"+s.cfg.BaseDomain+"/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	// Drop unsafe ?next= values server-side; the client re-checks anyway.
	if next := r.URL.Query().Get("next"); next != "" && s.safeNext(next) == "/" && next != "/" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.serveUI(w, r)
}

// handleRoot routes the central dashboard versus preview subdomains.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	host, ok := normalizeHost(r.Host)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	if host == s.cfg.BaseDomain {
		s.handleDashboard(w, r)
		return
	}
	label, ok := strings.CutSuffix(host, "."+s.cfg.BaseDomain)
	if !ok || label == "" || strings.Contains(label, ".") {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	s.handlePreview(w, r, label)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	host, ok := normalizeHost(r.Host)
	if !ok || host != s.cfg.BaseDomain {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if _, _, err := s.tokenFor(r); err != nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	s.serveUI(w, r)
}

// handlePreview serves one site's static files to any logged-in user and
// records document-navigation visits in that viewer's personal history.
// Only document navigations (root, directory index, extensionless routes,
// or Accept text/html documents) record views; asset hits (.js/.css/images
// etc.) never do, so scrolling a preview's assets does not spam history.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, label string) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		host, ok := normalizeHost(r.Host)
		if !ok {
			writeErr(w, http.StatusNotFound, "unknown site")
			return
		}
		next := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, "https://"+s.cfg.BaseDomain+"/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	site := s.metadb.SiteByLabel(token, label)
	if site == nil {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	// Deny encoded traversal/dot tricks before Clean normalizes them away:
	// %2e decodes to "." so /%2e%2e/secret would otherwise collapse to a
	// legitimate-looking path and serve index.html with a recorded view.
	if previewPathHasEncodedDot(r) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	root := filepath.Join(s.cfg.DataDir, "sites", label)
	upath := path.Clean("/" + r.URL.Path)
	rel := strings.TrimPrefix(upath, "/")
	if rel == "" {
		serveFile(w, r, filepath.Join(root, "index.html"))
		if err := s.metadb.RecordView(token, userID, label); err != nil {
			log.Printf("record view user=%s label=%s: %v", userID, label, err)
		}
		return
	}
	// Deny dot segments anywhere in the path before touching disk.
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, root+string(filepath.Separator)) && full != root {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if info, err := os.Stat(full); err == nil {
		if info.IsDir() {
			serveFile(w, r, filepath.Join(full, "index.html"))
			if err := s.metadb.RecordView(token, userID, label); err != nil {
				log.Printf("record view user=%s label=%s: %v", userID, label, err)
			}
			return
		}
		serveFile(w, r, full)
		// Asset hits serve bytes but record nothing: only documents count.
		if isDocumentNav(rel, r) {
			if err := s.metadb.RecordView(token, userID, label); err != nil {
				log.Printf("record view user=%s label=%s: %v", userID, label, err)
			}
		}
		return
	}
	// SPA fallback only for extensionless routes and HTML navigations.
	// Missing .js/.css return 404 so breakage stays visible.
	if path.Ext(rel) == "" || (strings.Contains(r.Header.Get("Accept"), "text/html") && !isAssetExt(path.Ext(rel))) {
		serveFile(w, r, filepath.Join(root, "index.html"))
		if err := s.metadb.RecordView(token, userID, label); err != nil {
			log.Printf("record view user=%s label=%s: %v", userID, label, err)
		}
		return
	}
	writeErr(w, http.StatusNotFound, "not found")
}

func isAssetExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
		".woff", ".woff2", ".ttf", ".map", ".json", ".txt", ".xml", ".webp", ".avif":
		return true
	}
	return false
}

// isDocumentNav reports whether a preview file hit is a document
// navigation worth recording: extensionless routes or Accept text/html
// documents that are not known asset types.
func isDocumentNav(rel string, r *http.Request) bool {
	if path.Ext(rel) == "" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html") && !isAssetExt(path.Ext(rel))
}

// previewPathHasEncodedDot rejects %2e (encoded ".") anywhere in the raw
// escaped path, plus encoded slashes that could smuggle separators, before
// path.Clean normalizes them away.
func previewPathHasEncodedDot(r *http.Request) bool {
	escaped := strings.ToLower(r.URL.EscapedPath())
	return strings.Contains(escaped, "%2e") || strings.Contains(escaped, "%252e") ||
		strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c")
}

func serveFile(w http.ResponseWriter, r *http.Request, full string) {
	f, err := os.Open(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Shared parent-domain risk: previews run as <label>.<BaseDomain>,
	// sibling to the central host under one parent domain, so a malicious
	// preview could otherwise frame-bust, clickjack, or script against
	// same-site context. Sandbox without allow-same-origin keeps each
	// preview in an opaque origin (its JS cannot reach cookies, which are
	// HttpOnly anyway, nor other previews), and DENY keeps previews
	// unframeable. allow-scripts stays so static Vite bundles still run.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	w.Header().Set("X-Frame-Options", "DENY")
	// Conservative MIME: known web types by extension, octet-stream
	// otherwise, so uploaded content can never sniff into script.
	if ct := mime.TypeByExtension(filepath.Ext(strings.ToLower(full))); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func hostOnly(hostport string) string {
	// Kept for compatibility; new code uses normalizeHost which also
	// lowercases and validates bracketed IPv6.
	if h, ok := normalizeHost(hostport); ok {
		return h
	}
	return strings.ToLower(strings.TrimSpace(hostport))
}

// normalizeHost lowercases a Host header value and strips a single :port
// or a [ipv6]:port bracket form. It reports false for anything else it
// cannot parse explicitly: bare IPv6 with multiple colons, empty hosts,
// empty or non-numeric ports, malformed brackets, or embedded spaces and
// userinfo separators. Callers reject !ok with a 404.
func normalizeHost(hostport string) (string, bool) {
	h := strings.ToLower(strings.TrimSpace(hostport))
	if h == "" {
		return "", false
	}
	if strings.HasPrefix(h, "[") {
		end := strings.Index(h, "]")
		if end < 0 {
			return "", false
		}
		host := h[1:end]
		if host == "" || strings.ContainsAny(host, " \t\r\n/@") {
			return "", false
		}
		rest := h[end+1:]
		if rest == "" {
			return host, true
		}
		if !strings.HasPrefix(rest, ":") {
			return "", false
		}
		port := rest[1:]
		if port == "" || !isNumericPort(port) {
			return "", false
		}
		return host, true
	}
	switch strings.Count(h, ":") {
	case 0:
		if strings.ContainsAny(h, " \t\r\n/@") {
			return "", false
		}
		return h, true
	case 1:
		i := strings.LastIndex(h, ":")
		host, port := h[:i], h[i+1:]
		if host == "" || port == "" || !isNumericPort(port) {
			return "", false
		}
		if strings.ContainsAny(host, " \t\r\n/@") {
			return "", false
		}
		return host, true
	default:
		// Bare IPv6 or garbage with multiple colons: reject explicitly
		// instead of guessing which colon starts a port.
		return "", false
	}
}

func isNumericPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validBaseDomain reports whether d is usable as the cookie/route parent
// domain: letters/digits/hyphens/dots only, no leading or trailing
// hyphen/dot, no empty labels, no port, max 253 chars. 127.0.0.1 and
// localhost stay allowed so dev on loopback keeps working; any other IP
// is rejected because browsers drop cookie domains set to an IP.
func validBaseDomain(d string) bool {
	if d == "127.0.0.1" || d == "localhost" {
		return true
	}
	if d == "" || len(d) > 253 {
		return false
	}
	for _, c := range d {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '.' {
			continue
		}
		return false
	}
	if strings.HasPrefix(d, "-") || strings.HasSuffix(d, "-") ||
		strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	if ip := net.ParseIP(d); ip != nil {
		return false
	}
	return true
}

// isLoopbackBind reports whether addr binds loopback only (127.0.0.1,
// ::1, or localhost). Empty hosts (":8093") and wildcard binds
// (0.0.0.0) are not loopback.
func isLoopbackBind(addr string) bool {
	host := strings.TrimSpace(addr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// isTestDomain reports whether domain is a test/local name: ends .test,
// or is localhost, or starts 127./::1.
func isTestDomain(domain string) bool {
	d := strings.ToLower(strings.TrimSpace(hostOnly(strings.TrimSpace(domain))))
	d = strings.Trim(d, "[]")
	d = strings.TrimSuffix(d, ".")
	if d == "localhost" {
		return true
	}
	if strings.HasSuffix(d, ".test") {
		return true
	}
	if strings.HasPrefix(d, "127.") {
		return true
	}
	if strings.HasPrefix(d, "::1") {
		return true
	}
	return false
}
