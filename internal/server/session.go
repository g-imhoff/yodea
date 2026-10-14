package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/g-imhoff/yodea/internal/auth"
)

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

func cookieCount(r *http.Request, name string) int {
	n := 0
	for _, c := range r.Cookies() {
		if c.Name == name {
			n++
		}
	}
	return n
}

const maxExpiredCookiePaths = 32

func (s *Server) expireTossedCookies(w http.ResponseWriter, r *http.Request, name string, keepBaseRoot, hostOnly bool) {
	domains := []string{s.cfg.BaseDomain}
	if net.ParseIP(s.cfg.BaseDomain) == nil {
		for d := s.cfg.BaseDomain; ; {
			_, parent, ok := strings.Cut(d, ".")
			if !ok || !strings.Contains(parent, ".") {
				break
			}
			domains = append(domains, parent)
			d = parent
		}
	}
	escaped := r.URL.EscapedPath()
	paths := []string{"/"}
	if strings.HasPrefix(escaped, "/") {
		for i := 1; i < len(escaped) && len(paths) < maxExpiredCookiePaths; i++ {
			if escaped[i] == '/' {
				paths = append(paths, escaped[:i], escaped[:i+1])
			}
		}
		if len(paths) < maxExpiredCookiePaths && !strings.HasSuffix(escaped, "/") {
			paths = append(paths, escaped)
		}
	}
	if hostOnly {
		domains = append(domains, "")
	}
	host, _ := normalizeHost(r.Host)
	for _, domain := range domains {
		for _, p := range paths {
			if keepBaseRoot && p == "/" && (domain == s.cfg.BaseDomain || (domain == "" && host == s.cfg.BaseDomain)) {
				continue
			}
			http.SetCookie(w, &http.Cookie{
				Name:     name,
				Value:    "",
				Path:     p,
				Domain:   domain,
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   s.cfg.SecureCookies,
				SameSite: http.SameSiteLaxMode,
			})
		}
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireCentralHost(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, "method_not_allowed", "method not allowed")
		return
	}
	var body loginBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, "bad_request", "bad request")
		return
	}
	if s.cfg.DevNoAuth {
		token, userID, err := auth.DevTokenFor(body.Email)
		if err != nil {
			writeErr(w, "bad_request", err.Error())
			return
		}
		s.setSessionCookies(w, token, 3600, "")
		writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "expires_in": 3600, "csrf_token": csrfTokenFor(token)})
		return
	}
	if s.authc == nil {
		writeErr(w, "auth_not_configured", "auth not configured")
		return
	}
	sess, err := s.authc.Login(body.Email, body.Password)
	if err != nil {
		writeErr(w, "invalid_credentials", "invalid email or password")
		return
	}
	userID, err := s.verifier.Verify(sess.AccessToken)
	if err != nil {
		writeErr(w, "verify_failed", "could not verify new session")
		return
	}
	s.setSessionCookies(w, sess.AccessToken, sess.ExpiresIn, sess.RefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "expires_in": sess.ExpiresIn, "csrf_token": csrfTokenFor(sess.AccessToken)})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireCentralHost(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, "method_not_allowed", "method not allowed")
		return
	}
	// Cookie-authed refresh needs the synchronizer token so a sibling
	// preview page cannot force session rotation with ambient cookies.
	// Bearer-only calls with no session cookie stay exempt.
	if !s.checkCSRF(w, r, "") {
		return
	}
	if cookieCount(r, "yodea_refresh") > 1 {
		writeErr(w, "login_required", "login required")
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
		writeErr(w, "login_required", "no refresh token")
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
		writeErr(w, "auth_not_configured", "auth not configured")
		return
	}
	sess, err := s.authc.Refresh(refresh)
	if err != nil {
		writeErr(w, "auth_expired", "session expired, log in again")
		return
	}
	s.setSessionCookies(w, sess.AccessToken, sess.ExpiresIn, sess.RefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"expires_in": sess.ExpiresIn, "csrf_token": csrfTokenFor(sess.AccessToken)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.requireCentralHost(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, "method_not_allowed", "method not allowed")
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
