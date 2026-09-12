package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/server"
)

const integrationBaseDomain = "previews.example.test"

// TestClientServerIntegrationLoginPushListPreviewDelete drives the real
// server handler (DevNoAuth) with the real CLI client over a loopback
// httptest server: login -> push -> list -> preview-fetch -> delete.
// Preview fetches override req.Host because the server routes previews by
// subdomain; API calls use the httptest URL directly.
func TestClientServerIntegrationLoginPushListPreviewDelete(t *testing.T) {
	isolateSession(t)

	srv, err := server.New(server.Config{
		DataDir:    t.TempDir(),
		BaseDomain: integrationBaseDomain,
		DevNoAuth:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := New(ts.URL, "")
	if err := c.Login("alice@example.com", "x"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if c.Token == "" {
		t.Fatal("login left empty token")
	}
	sess, err := LoadSession()
	if err != nil {
		t.Fatalf("load saved session: %v", err)
	}
	if sess.Server != ts.URL || sess.Token != c.Token {
		t.Fatalf("saved session = %+v, want server %q token %q", sess, ts.URL, c.Token)
	}

	dist := t.TempDir()
	const previewBody = "<h1>hello-integration</h1>"
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(previewBody), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := c.Deploy("blog", dist)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Label == "" || res.Project != "blog" {
		t.Fatalf("bad deploy result: %+v", res)
	}
	if !strings.Contains(res.URL, res.Label+"."+integrationBaseDomain) {
		t.Fatalf("deploy URL %q missing label host %q", res.URL, res.Label+"."+integrationBaseDomain)
	}

	list, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Label != res.Label || list[0].Project != "blog" {
		t.Fatalf("list = %+v, want one entry for label %q", list, res.Label)
	}

	fetchPreview := func() (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = res.Label + "." + integrationBaseDomain
		req.Header.Set("Authorization", "Bearer "+c.Token)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, string(body)
	}

	if code, body := fetchPreview(); code != http.StatusOK || !strings.Contains(body, previewBody) {
		t.Fatalf("preview = %d %q, want 200 with %q", code, body, previewBody)
	}

	if err := c.Delete("blog"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	after, err := c.List()
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("list after delete = %+v, want empty", after)
	}
	if code, _ := fetchPreview(); code != http.StatusNotFound {
		t.Fatalf("preview after delete = %d, want 404", code)
	}
	if err := c.Delete("blog"); err == nil || !strings.Contains(err.Error(), "no such project") {
		t.Fatalf("second delete = %v, want no-such-project error", err)
	}
}
