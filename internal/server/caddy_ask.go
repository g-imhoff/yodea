package server

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/g-imhoff/yodea/internal/sites"
)

// Caddy on-demand TLS ask abuse protection. Caddy v2.10/2.11
// `on_demand_tls` only supports `ask <url>` (burst/interval keys are
// rejected), so flood protection lives in the ask handler itself as a
// per-client-IP token bucket: refill 5 req/sec, burst 10, and a 1-minute
// block after 30 consecutive rejections. Idle buckets expire after
// caddyAskIdleTTL via lazy sweep (no goroutine, stdlib only).
const (
	caddyAskRefillPerSec    = 5.0
	caddyAskBurst           = 10
	caddyAskMaxRejections   = 30
	caddyAskBlockDuration   = time.Minute
	caddyAskIdleTTL         = 10 * time.Minute
	caddyAskCleanupInterval = time.Minute
)

// askBucket is one client's token-bucket state.
type askBucket struct {
	tokens       float64
	last         time.Time
	rejections   int
	blockedUntil time.Time
}

// caddyAskLimiter is a per-IP token bucket with block-on-abuse. Guarded
// by its own mutex; stdlib only (sync.Mutex + map + time).
type caddyAskLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*askBucket
	lastSweep time.Time
}

func newCaddyAskLimiter() *caddyAskLimiter {
	return &caddyAskLimiter{buckets: make(map[string]*askBucket)}
}

// allow reports whether ip may proceed now.
func (l *caddyAskLimiter) allow(ip string) bool {
	return l.allowAt(ip, time.Now())
}

// allowAt is allow with an injectable clock for deterministic tests.
func (l *caddyAskLimiter) allowAt(ip string, now time.Time) bool {
	if ip == "" {
		ip = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[string]*askBucket)
	}
	// Lazy expiry sweep so the map cannot grow unbounded.
	if now.Sub(l.lastSweep) >= caddyAskCleanupInterval {
		for k, b := range l.buckets {
			if !now.Before(b.blockedUntil) && now.Sub(b.last) > caddyAskIdleTTL {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[ip]
	if !ok {
		b = &askBucket{tokens: caddyAskBurst, last: now}
		l.buckets[ip] = b
	}
	// Active block: reject without consuming tokens.
	if now.Before(b.blockedUntil) {
		return false
	}
	// Expired block: lift it and reset the rejection streak.
	if !b.blockedUntil.IsZero() {
		b.blockedUntil = time.Time{}
		b.rejections = 0
	}
	// Refill proportional to elapsed time, capped at burst.
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * caddyAskRefillPerSec
		if b.tokens > caddyAskBurst {
			b.tokens = caddyAskBurst
		}
		b.last = now
	} else if elapsed < 0 {
		// Clock moved backwards: do not grant extra tokens.
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens -= 1
		b.rejections = 0
		return true
	}
	b.rejections++
	if b.rejections >= caddyAskMaxRejections {
		b.blockedUntil = now.Add(caddyAskBlockDuration)
	}
	return false
}

// caddyAskClientIP extracts the client IP for rate limiting from
// RemoteAddr only. Proxy headers (X-Forwarded-For) are deliberately
// ignored: Caddy calls back-channel over the container network and those
// headers are client-spoofable.
func caddyAskClientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if host != "" {
			return host
		}
	}
	if trimmed := strings.TrimSpace(r.RemoteAddr); trimmed != "" {
		return trimmed
	}
	return "unknown"
}

// handleCaddyAsk answers Caddy's on-demand TLS `ask` checks: 200 when
// domain is a live preview subdomain of this server
// (<label>.<BaseDomain> with an existing site), 404 otherwise. It takes
// no auth: Caddy calls it back-channel over the container network, and it
// reveals at most whether a preview exists (the preview URL is equally
// guessable, and previews themselves are team-visible by design).
func (s *Server) handleCaddyAsk(w http.ResponseWriter, r *http.Request) {
	if s.askLimiter != nil && !s.askLimiter.allow(caddyAskClientIP(r)) {
		log.Printf("caddy-ask rate limited ip=%s", caddyAskClientIP(r))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited", "code": "rate_limited"})
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, "method_not_allowed", "method not allowed")
		return
	}
	domain, ok := normalizeHost(r.URL.Query().Get("domain"))
	if !ok {
		writeErr(w, "not_found", "not a preview host")
		return
	}
	label, ok := strings.CutSuffix(domain, "."+strings.ToLower(s.cfg.BaseDomain))
	if !ok || label == "" || strings.Contains(label, ".") {
		writeErr(w, "not_found", "not a preview host")
		return
	}
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, "not_found", "bad label")
		return
	}
	// Existence check only. The token is empty so the local store ignores
	// it; the Supabase store falls back to its server-side key, which is
	// correct here because any existing site may get a certificate.
	if s.metadb.SiteByLabel("", label) == nil {
		writeErr(w, "unknown_site", "unknown site")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
