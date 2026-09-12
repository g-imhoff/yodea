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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
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
		if cfg.SupabaseURL == "" || cfg.SupabaseKey == "" {
			return nil, errors.New("supabase store needs SUPABASE_URL plus a server-side key")
		}
		s.metadb = store.NewSupabaseStore(cfg.SupabaseURL, cfg.SupabaseKey)
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

// Run serves until interrupted.
func (s *Server) Run() error {
	srv := &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      s.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	log.Printf("yodead listening on %s for %s (store=%s dev=%v assets=%v)",
		s.cfg.Addr, s.cfg.BaseDomain, s.storeName(), s.cfg.DevNoAuth, HasAssets())
	return srv.ListenAndServe()
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

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
			Name:     "yodea_refresh",
			Value:    refresh,
			Path:     "/api/session", // central host only, least privilege
			Domain:   domain,
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
		writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "expires_in": 3600})
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
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "expires_in": sess.ExpiresIn})
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
			writeJSON(w, http.StatusOK, map[string]any{"expires_in": 3600})
			return
		}
		s.setSessionCookies(w, "dev", 3600, "")
		writeJSON(w, http.StatusOK, map[string]any{"expires_in": 3600})
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
	writeJSON(w, http.StatusOK, map[string]any{"expires_in": sess.ExpiresIn})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
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

// validProject keeps project names single-segment and bounded; the label
// builder sanitizes the rest into DNS-safe form. Blank, dot-only, and
// names collapsing to the reserved "site" fallback (unless exactly "site")
// are rejected so distinct projects never share one sanitized identity.
func validProject(raw string) bool {
	if raw == "" || len(raw) > 40 {
		return false
	}
	if strings.TrimSpace(raw) == "" {
		return false
	}
	if strings.Trim(raw, ".") == "" {
		return false
	}
	if strings.HasPrefix(raw, "-") || strings.HasSuffix(raw, "-") {
		return false
	}
	if strings.ContainsAny(raw, "/\\?#") {
		return false
	}
	if sites.Sanitize(raw) == "site" && raw != "site" {
		return false
	}
	return true
}

func isLabelTaken(err error) bool {
	return err != nil && strings.Contains(err.Error(), "label is taken")
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, project string) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", "unknown", project, "", err)
		return
	}
	if !validProject(project) {
		writeErr(w, http.StatusBadRequest, "bad project name")
		log.Printf("deploy failed user=%s project=%s label=%s cause=%s", userID, project, "", "bad project name")
		return
	}
	project = sites.Sanitize(project)
	arc, err := readArchive(w, r)
	if err != nil {
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, "", err)
		return // readArchive already answered
	}
	label := s.labelFor(token, userID, project)
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, http.StatusBadRequest, "bad label: "+err.Error())
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		return
	}
	// Reserve the label in storage before touching disk. For a redeploy
	// keep the existing file counts so a later extraction failure leaves
	// metadata consistent with the live files on disk. For a new label the
	// placeholder has zero counts and is cleaned up if we fail before the
	// final Upsert.
	existingBefore := s.metadb.SiteByLabel(token, label)
	reserveFiles := 0
	var reserveBytes int64
	if existingBefore != nil && existingBefore.UserID == userID {
		reserveFiles = existingBefore.Files
		reserveBytes = existingBefore.Bytes
	}
	isNew := existingBefore == nil
	cleanupPlaceholder := func() {
		if !isNew {
			return
		}
		if cur := s.metadb.SiteByLabel(token, label); cur != nil && cur.UserID == userID && cur.Files == 0 && cur.Bytes == 0 {
			s.metadb.DeleteSite(token, userID, project)
		}
	}
	if err := s.metadb.UpsertSite(token, &store.Site{
		UserID:  userID,
		Project: project,
		Label:   label,
		Files:   reserveFiles,
		Bytes:   reserveBytes,
	}); err != nil {
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		if isLabelTaken(err) {
			writeErr(w, http.StatusConflict, err.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, "deploy failed: "+err.Error())
		}
		return
	}
	// Each deploy extracts to its own unique staging dir so concurrent
	// deploys for one label never share a path.
	staging, err := sites.NewStagingDir(s.cfg.DataDir)
	if err != nil {
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		cleanupPlaceholder()
		writeErr(w, http.StatusInternalServerError, "deploy failed: "+err.Error())
		return
	}
	res, err := sites.ExtractToStaging(bytes.NewReader(arc), staging, 0, 0)
	if err != nil {
		os.RemoveAll(staging)
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		cleanupPlaceholder()
		if sites.IsValidationError(err) {
			writeErr(w, http.StatusBadRequest, "rejected upload: "+err.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, "deploy failed: "+err.Error())
		}
		return
	}
	if err := s.metadb.UpsertSite(token, &store.Site{
		UserID:  userID,
		Project: project,
		Label:   label,
		Files:   res.Files,
		Bytes:   res.Bytes,
	}); err != nil {
		// Conflict: delete only this deploy's staging dir, never dest, so
		// the previously deployed files stay live.
		os.RemoveAll(staging)
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		if isLabelTaken(err) {
			writeErr(w, http.StatusConflict, err.Error())
		} else {
			cleanupPlaceholder()
			writeErr(w, http.StatusInternalServerError, "deploy failed: "+err.Error())
		}
		return
	}
	if err := sites.ReplaceSite(s.cfg.DataDir, label, staging); err != nil {
		os.RemoveAll(staging)
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		writeErr(w, http.StatusInternalServerError, "deploy failed: "+err.Error())
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
		mr, err := r.MultipartReader()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad multipart body")
			return nil, err
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad multipart body")
				return nil, err
			}
			name, file := part.FormName(), part.FileName()
			if file == "" && name != "archive" && name != "file" {
				_, _ = io.Copy(io.Discard, part)
				continue
			}
			buf, err := io.ReadAll(io.LimitReader(part, limit+1))
			if err != nil {
				writeErr(w, http.StatusBadRequest, "could not read upload")
				return nil, err
			}
			if int64(len(buf)) > int64(sites.MaxUploadBytes) {
				writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
				return nil, errors.New("upload too large")
			}
			return buf, nil
		}
		writeErr(w, http.StatusBadRequest, "no archive file in upload")
		return nil, errors.New("no archive part")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+1)
	buf, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
		return nil, err
	}
	if int64(len(buf)) > int64(sites.MaxUploadBytes) {
		writeErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 30MB")
		return nil, errors.New("upload too large")
	}
	return buf, nil
}

// labelFor reuses the author's existing label for the same project so
// redeploys overwrite. New projects claim LabelFor(user, project),
// suffixed on collision with another author's label.
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
	for n := 2; n <= 20; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if len(candidate) > 63 {
			candidate = base[:63-len(fmt.Sprintf("-%d", n))] + fmt.Sprintf("-%d", n)
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
		log.Printf("delete failed user=%s project=%s label=%s cause=%v", "unknown", project, "", err)
		return
	}
	if !validProject(project) {
		writeErr(w, http.StatusBadRequest, "bad project name")
		log.Printf("delete failed user=%s project=%s label=%s cause=%s", userID, project, "", "bad project name")
		return
	}
	site := s.metadb.DeleteSite(token, userID, sites.Sanitize(project))
	if site == nil {
		writeErr(w, http.StatusNotFound, "no such project")
		log.Printf("delete failed user=%s project=%s label=%s cause=%s", userID, project, "", "no such project")
		return
	}
	if err := sites.RemoveSite(s.cfg.DataDir, site.Label); err != nil {
		log.Printf("delete failed user=%s project=%s label=%s cause=%v", userID, site.Project, site.Label, err)
		writeErr(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}
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
	label := strings.TrimPrefix(r.URL.Path, "/api/favorites/")
	if strings.Contains(label, "/") || sites.ValidateLabel(label) != nil {
		writeErr(w, http.StatusBadRequest, "bad label")
		return
	}
	if !s.metadb.RemoveFavorite(token, userID, label) {
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
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
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
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return raw
	}
	if strings.HasPrefix(raw, "https://") {
		host := strings.TrimPrefix(raw, "https://")
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		if strings.EqualFold(host, s.cfg.BaseDomain) {
			// Strip any port or userinfo tricks before returning.
			if strings.ContainsAny(host, "@:") {
				return "/"
			}
			return raw
		}
	}
	return "/"
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
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
	host := hostOnly(r.Host)
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
		next := "https://" + hostOnly(r.Host) + r.URL.RequestURI()
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
	root := sites.SiteDir(s.cfg.DataDir, label)
	upath := path.Clean("/" + r.URL.Path)
	rel := strings.TrimPrefix(upath, "/")
	if rel == "" {
		serveFile(w, r, filepath.Join(root, "index.html"))
		s.metadb.RecordView(token, userID, label)
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
			s.metadb.RecordView(token, userID, label)
			return
		}
		serveFile(w, r, full)
		// Asset hits serve bytes but record nothing: only documents count.
		if isDocumentNav(rel, r) {
			s.metadb.RecordView(token, userID, label)
		}
		return
	}
	// SPA fallback only for extensionless routes and HTML navigations.
	// Missing .js/.css return 404 so breakage stays visible.
	if path.Ext(rel) == "" || (strings.Contains(r.Header.Get("Accept"), "text/html") && !isAssetExt(path.Ext(rel))) {
		serveFile(w, r, filepath.Join(root, "index.html"))
		s.metadb.RecordView(token, userID, label)
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
	// Preview hosts are plain DNS; strip a single :port if present.
	if strings.Count(hostport, ":") == 1 {
		if i := strings.LastIndex(hostport, ":"); i >= 0 {
			return hostport[:i]
		}
	}
	return hostport
}
