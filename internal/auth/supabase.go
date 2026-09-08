package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Session holds Supabase tokens. The refresh token never leaves the
// machine that owns it: CLI config file or HttpOnly cookie.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// Client talks to Supabase Auth (GoTrue) with the public anon key.
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
// the Supabase dashboard; only invited users can sign in.
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
		return nil, fmt.Errorf("supabase auth: empty access token")
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
		return nil, fmt.Errorf("supabase auth: empty access token")
	}
	return &s, nil
}

// Logout revokes the session server-side. Best effort for the CLI.
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

// PublicConfig is served by yodead so the CLI learns the Supabase project
// without shipping keys in the binary. The anon key is public by design.
type PublicConfig struct {
	SupabaseURL string `json:"supabase_url"`
	AnonKey     string `json:"anon_key"`
}

// RedactedURL hides all but the host for logs.
func RedactedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid url)"
	}
	return u.Host
}
