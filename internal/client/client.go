// Package client is the yodea CLI backend: session handling plus thin
// wrappers over the frozen yodead HTTP API (POST /api/session,
// GET /api/sites, POST /api/sites/{project}/deploy,
// DELETE /api/sites/{project}).
//
// Deploy contract: the server accepts both raw tarballs and multipart
// uploads, but this CLI standardizes on ONE form: a raw gzipped tar body
// with Content-Type application/gzip. That is the documented contract;
// the server keeps accepting multipart only for backward compatibility.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultProdServer is used when neither --server, YODEA_SERVER, nor
// YODEA_DEV points elsewhere.
const DefaultProdServer = "https://previews.example.com"

// DefaultDevServer matches `yodead --dev` (listen 127.0.0.1:8093).
const DefaultDevServer = "http://127.0.0.1:8093"

// MaxUploadBytes mirrors the server cap: deploys over 30MB are rejected.
const MaxUploadBytes = 30 << 20

// ResolveServer picks the API base URL: explicit flag first, then
// YODEA_SERVER, then the dev default when YODEA_DEV=1, else production.
func ResolveServer(flag string) string {
	if strings.TrimSpace(flag) != "" {
		return strings.TrimSuffix(strings.TrimSpace(flag), "/")
	}
	if v := strings.TrimSpace(os.Getenv("YODEA_SERVER")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if os.Getenv("YODEA_DEV") == "1" {
		return DefaultDevServer
	}
	return DefaultProdServer
}

// EffectiveServer resolves the server for an authenticated command given
// whether --server was explicitly passed plus the saved session server.
// An explicit flag always wins. Otherwise YODEA_SERVER, then the YODEA_DEV
// default, then the saved session server (only when no flag and no env
// override), else production.
func EffectiveServer(flag string, flagSet bool, sessServer string) string {
	if flagSet {
		return ResolveServer(flag)
	}
	if v := strings.TrimSpace(os.Getenv("YODEA_SERVER")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if os.Getenv("YODEA_DEV") == "1" {
		return DefaultDevServer
	}
	if strings.TrimSpace(sessServer) != "" {
		return strings.TrimSuffix(strings.TrimSpace(sessServer), "/")
	}
	return DefaultProdServer
}

// Client talks to one yodead server with a stored session token.
type Client struct {
	Server string
	Token  string
	HTTP   *http.Client
}

// New builds a client for server with an optional existing token.
func New(server, token string) *Client {
	return &Client{
		Server: strings.TrimSuffix(server, "/"),
		Token:  token,
		HTTP:   &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// apiError extracts the server's {"error": ...} message, if any.
func apiError(status int, body []byte) error {
	var v struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &v); err == nil && v.Error != "" {
		return fmt.Errorf("server error (HTTP %d): %s", status, v.Error)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("server error (HTTP %d): %s", status, msg)
}

func readBody(resp *http.Response) []byte {
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return buf
}

// Login exchanges email plus password against POST /api/session and
// persists the session locally (0600 file). The session token arrives as
// the yodea_session Set-Cookie value; the JSON body carries only metadata.
func (c *Client) Login(email, password string) error {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequest(http.MethodPost, c.Server+"/api/session", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	body := readBody(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed: %w", apiError(resp.StatusCode, body))
	}
	token := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == "yodea_session" && ck.Value != "" {
			token = ck.Value
		}
	}
	if token == "" {
		return fmt.Errorf("login failed: server did not return a session cookie")
	}
	var v struct {
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(body, &v)
	c.Token = token
	return SaveSession(Session{Server: c.Server, Token: token, UserID: v.UserID})
}

// Site mirrors the server's site record for `yodea list`.
type Site struct {
	UserID    string `json:"user_id"`
	Project   string `json:"project"`
	Label     string `json:"label"`
	UpdatedAt string `json:"updated_at"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
}

// List fetches the caller's personal site list (GET /api/sites).
func (c *Client) List() ([]Site, error) {
	req, err := http.NewRequest(http.MethodGet, c.Server+"/api/sites", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list request failed: %w", err)
	}
	body := readBody(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, body)
	}
	var v struct {
		Sites []Site `json:"sites"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("bad list response: %w", err)
	}
	if v.Sites == nil {
		v.Sites = []Site{}
	}
	return v.Sites, nil
}

// Delete removes one project (DELETE /api/sites/{project}).
func (c *Client) Delete(project string) error {
	if err := CheckProject(project); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodDelete, c.Server+"/api/sites/"+url.PathEscape(project), nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("delete request failed: %w", err)
	}
	body := readBody(resp)
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("no such project %q", project)
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp.StatusCode, body)
	}
	return nil
}

// DeployResult is the server's deploy answer; URL is the preview URL.
type DeployResult struct {
	URL     string `json:"url"`
	Project string `json:"project"`
	Label   string `json:"label"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// Deploy packs distDir as a gzipped tar and uploads it as a RAW tarball
// body (Content-Type application/gzip) to
// POST /api/sites/{project}/deploy. Raw upload is the CLI's documented
// contract; multipart stays server-supported but unused by this CLI.
func (c *Client) Deploy(project, distDir string) (*DeployResult, error) {
	if err := CheckProject(project); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := PackDist(distDir, &buf); err != nil {
		return nil, err
	}
	if buf.Len() > MaxUploadBytes {
		return nil, fmt.Errorf("packed dist is %d bytes, over the 30MB deploy limit", buf.Len())
	}
	req, err := http.NewRequest(http.MethodPost,
		c.Server+"/api/sites/"+url.PathEscape(project)+"/deploy", &buf)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deploy request failed: %w", err)
	}
	body := readBody(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, body)
	}
	var res DeployResult
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("bad deploy response: %w", err)
	}
	if res.URL == "" {
		return nil, fmt.Errorf("bad deploy response: missing preview URL")
	}
	return &res, nil
}
