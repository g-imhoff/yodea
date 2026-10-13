package server

import (
	"net/http"
	"net/url"
	"strings"
)

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
		writeErr(w, "unknown_site", "unknown site")
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
		writeErr(w, "unknown_site", "unknown site")
		return
	}
	if host == s.cfg.BaseDomain {
		s.handleDashboard(w, r)
		return
	}
	label, ok := strings.CutSuffix(host, "."+s.cfg.BaseDomain)
	if !ok || label == "" || strings.Contains(label, ".") {
		writeErr(w, "unknown_site", "unknown site")
		return
	}
	s.handlePreview(w, r, label)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	host, ok := normalizeHost(r.Host)
	if !ok || host != s.cfg.BaseDomain {
		writeErr(w, "unknown_site", "unknown site")
		return
	}
	if r.URL.Path != "/" {
		writeErr(w, "not_found", "not found")
		return
	}
	if _, _, err := s.tokenFor(r); err != nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	s.serveUI(w, r)
}
