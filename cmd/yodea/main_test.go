package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/client"
)

func isolateSession(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.json")
	t.Setenv("YODEA_SESSION_FILE", p)
	return p
}

func TestAuthedExplicitFlagWins(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	if err := client.SaveSession(client.Session{Server: "http://saved.example", Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	c, err := authed("http://flag.example", true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://flag.example" {
		t.Fatalf("explicit --server flag lost: got %q want flag", c.Server)
	}
}

func TestAuthedFallsBackToSessionOnlyWithoutFlagOrEnv(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	if err := client.SaveSession(client.Session{Server: "http://saved.example", Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	c, err := authed(client.ResolveServer(""), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://saved.example" {
		t.Fatalf("want saved session server, got %q", c.Server)
	}
	// Explicit env beats the saved session.
	t.Setenv("YODEA_SERVER", "http://env.example")
	c, err = authed(client.ResolveServer(""), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://env.example" {
		t.Fatalf("want env server, got %q", c.Server)
	}
}

func TestLoginRejectsPasswordFlag(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_PASSWORD", "env-secret")
	err := cmdLogin("http://127.0.0.1:8093", []string{"--email", "a@b.c", "--password", "argv-secret"})
	if err == nil {
		t.Fatal("expected --password flag to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "password") {
		t.Fatalf("want unknown-flag error for --password, got %v", err)
	}
}

func TestInitRejectsFlagAfterProject(t *testing.T) {
	t.Chdir(t.TempDir())
	err := cmdInit([]string{"demo", "--dir", t.TempDir()})
	if err == nil {
		t.Fatal("expected init with flag after project to fail, got nil")
	}
	if !strings.Contains(err.Error(), "before") {
		t.Fatalf("want flag-order error mentioning flags go before project, got %v", err)
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage in flag-order error, got %v", err)
	}
}

func TestInitRejectsTwoProjects(t *testing.T) {
	t.Chdir(t.TempDir())
	err := cmdInit([]string{"a", "b"})
	if err == nil {
		t.Fatal("expected init with two projects to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for two projects, got %v", err)
	}
}

func TestDeleteRejectsExtraPositionals(t *testing.T) {
	isolateSession(t)
	err := cmdDelete("http://127.0.0.1:8093", false, []string{"my", "app"})
	if err == nil {
		t.Fatal("expected delete with two projects to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for delete with extra positionals, got %v", err)
	}
	err = cmdDelete("http://127.0.0.1:8093", false, nil)
	if err == nil {
		t.Fatal("expected delete with no project to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for delete with no project, got %v", err)
	}
}

func TestPushRejectsPositional(t *testing.T) {
	isolateSession(t)
	err := cmdPush("http://127.0.0.1:8093", false, []string{"extra"})
	if err == nil {
		t.Fatal("expected push with positional to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for push with positional, got %v", err)
	}
}

func TestListRejectsPositional(t *testing.T) {
	isolateSession(t)
	err := cmdList("http://127.0.0.1:8093", false, []string{"extra"})
	if err == nil {
		t.Fatal("expected list with positional to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for list with positional, got %v", err)
	}
}

func TestLoginRejectsPositional(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_EMAIL", "a@b.c")
	t.Setenv("YODEA_PASSWORD", "env-secret")
	err := cmdLogin("http://127.0.0.1:8093", []string{"extra"})
	if err == nil {
		t.Fatal("expected login with positional to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for login with positional, got %v", err)
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fnErr := fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), fnErr
}

func TestListQuotesNames(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sites":[{"project":"a\tb","label":"x\ny","files":1}]}`))
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdList(srv.URL, true, nil) })
	if err != nil {
		t.Fatal(err)
	}
	wantProject := fmt.Sprintf("%q", "a\tb")
	wantLabel := fmt.Sprintf("%q", "x\ny")
	if !strings.Contains(out, wantProject) {
		t.Fatalf("list output should quote project as %s, got %q", wantProject, out)
	}
	if !strings.Contains(out, wantLabel) {
		t.Fatalf("list output should quote label as %s, got %q", wantLabel, out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("quoted list output should be one line, got %q", out)
	}
}

func TestDeleteQuotesName(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdDelete(srv.URL, true, []string{"myapp"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `deleted "myapp"`) {
		t.Fatalf("delete output should quote name, got %q", out)
	}
}

func TestPushQuotesHostileProjectAndURL(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	hostileProject := "evil\tproj\ninject"
	hostileURL := "https://example.test/a\tb\nc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"url":%q,"project":%q,"label":"x","files":2,"bytes":10}`, hostileURL, hostileProject)
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pkg := `{"dependencies":{"react":"^18","react-dom":"^18"},"devDependencies":{"vite":"^5"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(`export default {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "App.tsx"), []byte(`export default function App() { return null }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return cmdPush(srv.URL, true, []string{"--dir", dir, "--project", "demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	wantProject := fmt.Sprintf("%q", hostileProject)
	wantURL := fmt.Sprintf("%q", hostileURL)
	if !strings.Contains(out, wantProject) {
		t.Fatalf("push output should quote project as %s, got %q", wantProject, out)
	}
	if !strings.Contains(out, wantURL) {
		t.Fatalf("push output should quote URL as %s, got %q", wantURL, out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("quoted push output should be one line, got %q", out)
	}
}
