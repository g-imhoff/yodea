package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Limiter allows up to burst requests, then rejects until refill.
func TestCaddyAskLimiterBurstAndRefill(t *testing.T) {
	l := newCaddyAskLimiter()
	now := time.Now()
	ip := "10.0.0.1"
	for i := 0; i < caddyAskBurst; i++ {
		if !l.allowAt(ip, now) {
			t.Fatalf("request %d allowed = false, want true (burst %d)", i+1, caddyAskBurst)
		}
	}
	if l.allowAt(ip, now) {
		t.Fatalf("request %d allowed = true past burst, want false", caddyAskBurst+1)
	}
	// Refill one token after 1/refill seconds (200ms at 5/sec).
	later := now.Add(time.Duration(float64(time.Second) / caddyAskRefillPerSec))
	if !l.allowAt(ip, later) {
		t.Fatalf("refilled request allowed = false, want true after %v", later.Sub(now))
	}
}

// Per-IP isolation: exhausting one IP must not affect another.
func TestCaddyAskLimiterPerIP(t *testing.T) {
	l := newCaddyAskLimiter()
	now := time.Now()
	for i := 0; i < caddyAskBurst; i++ {
		l.allowAt("10.0.0.1", now)
	}
	if l.allowAt("10.0.0.1", now) {
		t.Fatal("exhausted IP allowed, want false")
	}
	if !l.allowAt("10.0.0.2", now) {
		t.Fatal("fresh IP rejected, want true")
	}
}

// 30 consecutive rejections trigger a 1-minute block; the block lifts
// after expiry and a success resets the streak.
func TestCaddyAskLimiterBlockAndExpiry(t *testing.T) {
	l := newCaddyAskLimiter()
	now := time.Now()
	ip := "10.0.0.9"
	// Drain the burst first.
	for i := 0; i < caddyAskBurst; i++ {
		if !l.allowAt(ip, now) {
			t.Fatalf("drain %d: want true", i+1)
		}
	}
	// Accumulate rejections at a frozen clock (no refill possible).
	for i := 0; i < caddyAskMaxRejections; i++ {
		if l.allowAt(ip, now) {
			t.Fatalf("rejection %d: allowed = true, want false", i+1)
		}
	}
	// Block is now active: even after a long refill window it rejects.
	if l.allowAt(ip, now.Add(10*time.Second)) {
		t.Fatal("blocked request allowed during block, want false")
	}
	// After the block expires, requests flow again.
	after := now.Add(caddyAskBlockDuration + time.Second)
	if !l.allowAt(ip, after) {
		t.Fatal("post-block request rejected, want true")
	}
}

// Lazy expiry: idle buckets are swept so the map cannot grow unbounded.
func TestCaddyAskLimiterIdleExpiry(t *testing.T) {
	l := newCaddyAskLimiter()
	now := time.Now()
	l.allowAt("10.0.0.5", now)
	if len(l.buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(l.buckets))
	}
	// Force the next call to sweep by aging lastSweep.
	l.mu.Lock()
	l.lastSweep = now
	l.mu.Unlock()
	l.allowAt("10.0.0.6", now.Add(caddyAskIdleTTL+2*caddyAskCleanupInterval))
	l.mu.Lock()
	_, stale := l.buckets["10.0.0.5"]
	n := len(l.buckets)
	l.mu.Unlock()
	if stale {
		t.Fatal("idle bucket not expired, want it swept")
	}
	if n != 1 {
		t.Fatalf("buckets after sweep = %d, want 1 (fresh IP only)", n)
	}
}

// Handler: burst past capacity returns 429 but the service keeps
// answering (existing 404 logic otherwise unchanged).
func TestCaddyAskHandlerRateLimited(t *testing.T) {
	s := newTestServer(t)
	var codes []int
	for i := 0; i < caddyAskBurst+5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/caddy-ask?domain=nope.invalid", nil)
		req.RemoteAddr = "192.0.2.99:1234"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	seen404, seen429 := false, false
	for _, c := range codes {
		switch c {
		case http.StatusNotFound:
			seen404 = true
		case http.StatusTooManyRequests:
			seen429 = true
		default:
			t.Fatalf("ask code = %d, want 404 or 429", c)
		}
	}
	if !seen404 {
		t.Fatal("no 404 in burst, want early requests to answer normally")
	}
	if !seen429 {
		t.Fatalf("no 429 in %d rapid requests, want limiter to engage", len(codes))
	}
	// A different client IP is unaffected.
	req := httptest.NewRequest(http.MethodGet, "/api/caddy-ask?domain=nope.invalid", nil)
	req.RemoteAddr = "192.0.2.100:1234"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fresh IP ask = %d, want 404", rec.Code)
	}
}
