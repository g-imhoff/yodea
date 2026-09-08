package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/g-imhoff/yodea/internal/auth"
	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

//go:embed web/login.html web/dashboard.html
var webFS embed.FS

// Config wires the server. SupabaseURL plus AnonKey come from env;
// DevNoAuth skips Supabase entirely for local testing.
type Config struct {
	Addr          string
	DataDir       string
	BaseDomain    string // e.g. previews.example.com
	SupabaseURL   string
	AnonKey       string
	DevNoAuth     bool
	SecureCookies bool
}

// Server serves the API, the dashboard, and the preview subdomains.
type Server struct {
	cfg      Config
	verifier *auth.Verifier
	store    *store.Store
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
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s.store = st
	s.routes()
	return s, nil
}

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
	m.HandleFunc("/login", s.handleLoginPage)
	m.HandleFunc("/", s.handleRoot)
}

func (s *Server) Run() error {
	srv := &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      s.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	log.Printf("yodead listening on %s for %s", s.cfg.Addr, s.cfg.BaseDomain)
	return srv.ListenAndServe()
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
		if body.Email == "" {
			writeErr(w, http.StatusBadRequest, "email required")
			return
		}
		s.setSessionCookies(w, "dev", 3600, "")
		writeJSON(w, http.StatusOK, map[string]any{"user_id": "dev-user", "expires_in": 3600})
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
	for _, name := range []string{"yodea_session", "yodea_refresh"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": s.store.ListSites(userID)})
}

// handleSite routes POST .../deploy and DELETE .../{project}.
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sites/")
	parts := strings.SplitN(rest, "/", 2)
	project := parts[0]
	if project == "" {
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

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, project string) {
	_, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	project = sites.Sanitize(project)
	if project == "" || len(project) > 40 {
		writeErr(w, http.StatusBadRequest, "bad project name")
		return
	}
	// Cap the request body just above the archive cap.
	r.Body = http.MaxBytesReader(w, r.Body, sites.MaxUploadBytes+1<<20)
	label := s.labelFor(userID, project)
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, http.StatusBadRequest, "bad label: "+err.Error())
		return
	}
	dest := filepath.Join(s.cfg.DataDir, "sites", label)
	res, err := sites.ExtractDist(r.Body, dest, 0, 0)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "rejected upload: "+err.Error())
		return
	}
	if err := s.store.UpsertSite(&store.Site{
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

// labelFor reuses the author's existing label for the same project so
// redeploys overwrite. New projects claim LabelFor(user, project),
// suffixed on collision with another author's label.
func (s *Server) labelFor(userID, project string) string {
	for _, site := range s.store.ListSites(userID) {
		if site.Project == project {
			return site.Label
		}
	}
	base := sites.LabelFor(auth.UserPart(userID), project)
	if other := s.store.SiteByLabel(base); other == nil {
		return base
	}
	for n := 2; n <= 20; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if len(candidate) > 63 {
			candidate = base[:63-len(fmt.Sprintf("-%d", n))] + fmt.Sprintf("-%d", n)
		}
		if other := s.store.SiteByLabel(candidate); other == nil {
			return candidate
		}
	}
	return base // UpsertSite will return 409 on genuine exhaustion.
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, project string) {
	_, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	site := s.store.DeleteSite(userID, sites.Sanitize(project))
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
	_, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": s.store.RecentViews(userID, 20)})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	if host != s.cfg.BaseDomain {
		// Deep-link return: central login redirects back to the preview host.
		next := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, "https://"+s.cfg.BaseDomain+"/login?next="+next, http.StatusFound)
		return
	}
	page, _ := webFS.ReadFile("web/login.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
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
		http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusFound)
		return
	}
	page, _ := webFS.ReadFile("web/dashboard.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// handlePreview serves one site's files to any logged-in team member and
// records the visit in that viewer's personal history.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, label string) {
	_, userID, err := s.tokenFor(r)
	if err != nil {
		next := "https://" + hostOnly(r.Host) + r.URL.RequestURI()
		http.Redirect(w, r, "https://"+s.cfg.BaseDomain+"/login?next="+next, http.StatusFound)
		return
	}
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	site := s.store.SiteByLabel(label)
	if site == nil {
		writeErr(w, http.StatusNotFound, "unknown site")
		return
	}
	root := filepath.Join(s.cfg.DataDir, "sites", label)
	upath := path.Clean("/" + r.URL.Path)
	rel := strings.TrimPrefix(upath, "/")
	if rel == "" {
		serveFile(w, r, filepath.Join(root, "index.html"))
		s.store.RecordView(userID, label)
		return
	}
	// Block dot segments anywhere in the path.
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
			s.store.RecordView(userID, label)
			return
		}
		serveFile(w, r, full)
		s.store.RecordView(userID, label)
		return
	}
	// SPA fallback only for extensionless routes and HTML navigations.
	// Missing .js/.css return 404 so breakage stays visible.
	if path.Ext(rel) == "" || strings.Contains(r.Header.Get("Accept"), "text/html") && isAssetExt(path.Ext(rel)) == false {
		serveFile(w, r, filepath.Join(root, "index.html"))
		s.store.RecordView(userID, label)
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
	if ct := mime.TypeByExtension(filepath.Ext(full)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func hostOnly(hostport string) string {
	if h := strings.Split(hostport, ":"); len(h) > 0 {
		// Split on last colon to survive IPv6; preview hosts are plain DNS.
		if i := strings.LastIndex(hostport, ":"); i >= 0 && !strings.HasSuffix(hostport, "]") {
			if strings.Count(hostport, ":") == 1 {
				return hostport[:i]
			}
		}
	}
	return hostport
}

var _ = io.Discard
