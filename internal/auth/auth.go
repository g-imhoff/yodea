// Package auth verifies Supabase access tokens and exchanges email plus
// password credentials for sessions through Supabase Auth (GoTrue).
//
// Production verifies RS256 access tokens against the project's JWKS and
// logs in via the public anon key. DevNoAuth mode (YODEA_DEV=1) skips
// Supabase entirely with synthetic per-viewer tokens so `go test` and local
// development run without network or secrets.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/golang-jwt/jwt/v5"
)

// Verifier checks access tokens and returns the user ID (sub claim).
type Verifier struct {
	jwks      *keyfunc.JWKS
	issuer    string
	devMode   bool
	devUserID string
}

// NewVerifier loads the JWKS for asymmetric Supabase signing keys and
// refreshes it in the background. jwksURL is
// {SUPABASE_URL}/auth/v1/.well-known/jwks.json and issuer is
// {SUPABASE_URL}/auth/v1.
func NewVerifier(jwksURL, issuer string) (*Verifier, error) {
	if jwksURL == "" || issuer == "" {
		return nil, errors.New("jwks URL and issuer are required")
	}
	jwks, err := keyfunc.Get(jwksURL, keyfunc.Options{
		Ctx:                 context.Background(),
		RefreshInterval:     10 * time.Minute,
		RefreshUnknownKID:   true, // pick up key rotations without restart
		RefreshErrorHandler: func(err error) {},
	})
	if err != nil {
		return nil, fmt.Errorf("load JWKS: %w", err)
	}
	return &Verifier{jwks: jwks, issuer: issuer}, nil
}

// NewDevVerifier accepts only synthetic dev tokens for local testing.
// It must never run in production.
func NewDevVerifier(userID string) *Verifier {
	if userID == "" {
		userID = "dev-user"
	}
	return &Verifier{devMode: true, devUserID: userID}
}

// Close stops the JWKS background refresh, if any.
func (v *Verifier) Close() {
	if v.jwks != nil {
		v.jwks.EndBackground()
	}
}

// Verify checks signature, expiry, and issuer, then returns sub.
func (v *Verifier) Verify(tokenString string) (string, error) {
	if v.devMode {
		return verifyDevToken(tokenString, v.devUserID)
	}
	if tokenString == "" {
		return "", errors.New("missing token")
	}
	token, err := jwt.Parse(tokenString, v.jwks.Keyfunc,
		jwt.WithValidMethods([]string{"RS256", "ES256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second), // 30s clock skew allowance
	)
	if err != nil {
		return "", fmt.Errorf("parse token: %w", err)
	}
	if !token.Valid {
		return "", errors.New("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("unexpected claims shape")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", errors.New("missing sub claim")
	}
	return sub, nil
}

// verifyDevToken accepts the literal token "dev" (mapping to defaultUser)
// plus per-viewer tokens of the form "dev:<user-id>" so tests can exercise
// per-viewer privacy with distinct users without Supabase.
func verifyDevToken(tokenString, defaultUser string) (string, error) {
	if tokenString == "dev" {
		return defaultUser, nil
	}
	rest, ok := strings.CutPrefix(tokenString, "dev:")
	if !ok || rest == "" || len(rest) > 40 {
		return "", errors.New("invalid dev token")
	}
	for _, r := range rest {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return "", fmt.Errorf("invalid dev user %q", rest)
	}
	if rest[0] == '-' || rest[len(rest)-1] == '-' {
		return "", fmt.Errorf("invalid dev user %q", rest)
	}
	return rest, nil
}

// DevTokenFor derives a stable per-viewer dev user ID from an email and
// returns the matching synthetic access token. Empty email is rejected so
// anonymous logins stay visible as 400s, not silent default users.
func DevTokenFor(email string) (token, userID string, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", "", errors.New("email required")
	}
	var b strings.Builder
	for _, r := range email {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	id := "dev-" + strings.Trim(collapse(b.String()), "-")
	if len(id) > 40 {
		id = id[:40]
	}
	id = strings.Trim(id, "-")
	if id == "" || id == "dev" {
		id = "dev-user"
		return "dev", id, nil
	}
	return "dev:" + id, id, nil
}

// UserPart derives a short DNS-safe handle from a user ID for preview
// subdomain labels.
func UserPart(userID string) string {
	s := strings.ToLower(userID)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(collapse(b.String()), "-")
	if len(out) > 12 {
		out = out[:12]
	}
	if out == "" {
		out = "user"
	}
	return out
}

func collapse(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if !prevDash {
				b.WriteRune(r)
			}
			prevDash = true
			continue
		}
		prevDash = false
		b.WriteRune(r)
	}
	return b.String()
}

// Session holds Supabase tokens. The refresh token never leaves the
// machine that owns it: an HttpOnly cookie for the dashboard, a local file
// for CLIs.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// Client talks to Supabase Auth (GoTrue) with the public anon key.
// The anon key is public by design; the service key must never reach this
// client or any browser path.
type Client struct {
	baseURL string
	anonKey string
	http    *http.Client
}

// NewClient builds a client for a Supabase project URL such as
// https://xyzcompany.supabase.co.
func NewClient(supabaseURL, anonKey string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(supabaseURL, "/"),
		anonKey: anonKey,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.baseURL+"/auth/v1"+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.anonKey)
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

func decodeError(resp *http.Response) error {
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var msg struct {
		Msg string `json:"msg"`
		Err string `json:"error_description"`
	}
	_ = json.Unmarshal(buf, &msg)
	detail := msg.Msg
	if detail == "" {
		detail = msg.Err
	}
	if detail == "" {
		detail = strings.TrimSpace(string(buf))
	}
	return fmt.Errorf("supabase auth: HTTP %d: %s", resp.StatusCode, detail)
}

// Login exchanges email plus password for tokens. Signups stay disabled in
// the Supabase dashboard; this is an invite-only product with no signup UI.
func (c *Client) Login(email, password string) (*Session, error) {
	resp, err := c.do(http.MethodPost, "/token?grant_type=password", map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeError(resp)
	}
	defer resp.Body.Close()
	var s Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	if s.AccessToken == "" {
		return nil, errors.New("supabase auth: empty access token")
	}
	return &s, nil
}

// Refresh rotates an expired access token.
func (c *Client) Refresh(refreshToken string) (*Session, error) {
	resp, err := c.do(http.MethodPost, "/token?grant_type=refresh_token", map[string]string{
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeError(resp)
	}
	defer resp.Body.Close()
	var s Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	if s.AccessToken == "" {
		return nil, errors.New("supabase auth: empty access token")
	}
	return &s, nil
}

// Logout revokes the session server-side. Best effort: cookie clearing is
// what actually signs the browser out.
func (c *Client) Logout(accessToken string) {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/auth/v1/logout", nil)
	if err != nil {
		return
	}
	req.Header.Set("apikey", c.anonKey)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// PublicConfig is served by yodead so API clients learn the Supabase
// project without shipping keys in the binary. The anon key is public by
// design; it only permits the auth endpoints plus RLS-scoped reads.
type PublicConfig struct {
	SupabaseURL string `json:"supabase_url"`
	AnonKey     string `json:"anon_key"`
}
