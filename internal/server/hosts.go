package server

import (
	"net"
	"net/http"
	"strings"
)

func hostOnly(hostport string) string {
	// Helper for isTestDomain only: strips an optional port via
	// normalizeHost, falling back to a lowercased trim.
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

func (s *Server) previewLabel(hostport string) (string, bool) {
	host, ok := normalizeHost(hostport)
	if !ok {
		return "", false
	}
	label, ok := strings.CutSuffix(host, "."+s.cfg.BaseDomain)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

func (s *Server) requireCentralHost(w http.ResponseWriter, r *http.Request) bool {
	if host, ok := normalizeHost(r.Host); !ok || host != s.cfg.BaseDomain {
		writeErr(w, "not_found", "not found")
		return false
	}
	return true
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
