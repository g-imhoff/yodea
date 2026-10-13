package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

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
// OPTIONS never mutate, and the token-issuing endpoint (/api/session) is
// exempt so a client can obtain a token. /api/session/refresh is NOT
// exempt: when the yodea_session cookie is present without a Bearer
// header, the X-Yodea-CSRF header must equal csrfTokenFor(token);
// otherwise 403. Bearer-only refresh calls with no session cookie stay
// exempt.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	if r.URL.Path == "/api/session" {
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
		writeErr(w, "csrf_required", "csrf required")
		return false
	}
	return true
}
