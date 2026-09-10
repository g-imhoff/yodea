package client

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateSession(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.json")
	t.Setenv("YODEA_SESSION_FILE", p)
	return p
}

func writeMinimalReactTS(t *testing.T, dir string) {
	t.Helper()
	pkg := `{"dependencies":{"react":"^18","react-dom":"^18"},"devDependencies":{"vite":"^5"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "App.tsx"), []byte("export default function App(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveServerDevDefault(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "1")
	if got := ResolveServer(""); got != "http://127.0.0.1:8093" {
		t.Fatalf("dev default = %q, want localhost:8093", got)
	}
}

func TestEffectiveServerExplicitFlagWins(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	if got := EffectiveServer("http://flag.example", true, "http://saved.example"); got != "http://flag.example" {
		t.Fatalf("explicit flag lost: got %q want flag", got)
	}
}

func TestEffectiveServerFallsBackToSessionOnlyWithoutFlagOrEnv(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	if got := EffectiveServer("", false, "http://saved.example"); got != "http://saved.example" {
		t.Fatalf("want saved session server, got %q", got)
	}
	// Env overrides session.
	t.Setenv("YODEA_SERVER", "http://env.example")
	if got := EffectiveServer("", false, "http://saved.example"); got != "http://env.example" {
		t.Fatalf("want env server, got %q", got)
	}
	// Dev default overrides session.
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "1")
	if got := EffectiveServer("", false, "http://saved.example"); got != DefaultDevServer {
		t.Fatalf("want dev server, got %q", got)
	}
}

func TestLoginBadAuthFailsCleanly(t *testing.T) {
	isolateSession(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid email or password"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	if err := c.Login("nobody@example.com", "wrong"); err == nil {
		t.Fatal("expected login error for bad credentials, got nil")
	}
}

func TestSessionRoundTrip0600(t *testing.T) {
	p := isolateSession(t)
	want := Session{Server: "http://127.0.0.1:8093", Token: "tok123", UserID: "u1"}
	if err := SaveSession(want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %o, want 600", fi.Mode().Perm())
	}
	got, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}

func TestSessionCorruptRejected(t *testing.T) {
	p := isolateSession(t)
	for name, body := range map[string]string{
		"bad json":    "{bad",
		"empty":       "",
		"missing tok": `{"server":"http://x","user_id":"u"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSession(); err == nil {
				t.Fatalf("expected corrupt session error for %q, got nil", name)
			}
		})
	}
}

func TestInitRefusesNonEmptyDir(t *testing.T) {
	isolateSession(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Scaffold(dir, "demo", false); err == nil {
		t.Fatal("expected Scaffold to refuse a non-empty dir without force, got nil")
	}
}

func TestLinkForceOverwritesConfig(t *testing.T) {
	isolateSession(t)
	dir := t.TempDir()
	writeMinimalReactTS(t, dir)
	if err := Init(dir, "proj-a", false, true); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "proj-b", true, true); err != nil {
		t.Fatalf("link --force should succeed, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "yodea.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"project\": \"proj-b\"}\n" {
		t.Fatalf("link --force did not overwrite yodea.json, got %q", string(data))
	}
	fi, err := os.Stat(filepath.Join(dir, "yodea.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("yodea.json mode = %o, want 644", fi.Mode().Perm())
	}
	// Without --force, re-link is refused.
	if err := Init(dir, "proj-c", false, true); err == nil {
		t.Fatal("expected re-link without --force to fail, got nil")
	}
}

func TestScaffoldNeverOverwritesUserFiles(t *testing.T) {
	isolateSession(t)
	dir := t.TempDir()
	keep := filepath.Join(dir, "src", "App.tsx")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Scaffold(dir, "demo", true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(keep)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mine" {
		t.Fatalf("scaffold overwrote user file, got %q", string(data))
	}
}

func TestScaffoldForceOverwritesOwnedConfig(t *testing.T) {
	isolateSession(t)
	dir := t.TempDir()
	stale := filepath.Join(dir, ConfigFile)
	if err := os.WriteFile(stale, []byte("{\"project\": \"stale\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "src", "App.tsx")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Scaffold(dir, "newproj", true); err != nil {
		t.Fatalf("Scaffold --force: %v", err)
	}
	data, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"project\": \"newproj\"}\n" {
		t.Fatalf("Scaffold --force kept stale yodea.json, got %q want new project", string(data))
	}
	kept, err := os.ReadFile(keep)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "mine" {
		t.Fatalf("Scaffold --force overwrote user file, got %q", string(kept))
	}
}

func TestCheckProjectRejectsBlankDotOnlyAndFallback(t *testing.T) {
	isolateSession(t)
	for _, raw := range []string{"", "   ", ".", "..", "...", "---", "!!!", "___", " - ", "??"} {
		t.Run("reject/"+raw, func(t *testing.T) {
			if err := CheckProject(raw); err == nil {
				t.Fatalf("CheckProject(%q) = nil, want error", raw)
			}
		})
	}
	for _, raw := range []string{"site", "blog", "my-app", "a1", "my.project"} {
		t.Run("accept/"+raw, func(t *testing.T) {
			if err := CheckProject(raw); err != nil {
				t.Fatalf("CheckProject(%q) = %v, want nil", raw, err)
			}
		})
	}
	// Case-variant still sanitizes to the reserved fallback.
	if err := CheckProject("Site"); err == nil {
		t.Fatal("CheckProject(Site) = nil, want error (sanitizes to reserved site)")
	}
}

func TestPushFastFailsNonReactTS(t *testing.T) {
	isolateSession(t)
	dir := t.TempDir() // no package.json, no vite config
	if err := ValidateReactTS(dir); err == nil {
		t.Fatal("expected ValidateReactTS to reject a non-React-TS dir, got nil")
	}
}

func TestReadProjectRejectsCorruptConfig(t *testing.T) {
	isolateSession(t)
	cases := map[string]string{
		"bad json":     "{bad json",
		"empty":        "",
		"blank":        "   \n",
		"missing name": `{}`,
		"empty name":   `{"project": "  "}`,
		"bad name":     `{"project": "-bad-"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// Use a valid folder name so fallback would succeed if attempted;
			// the test proves no fallback happens.
			valid := filepath.Join(dir, "validproj")
			if err := os.MkdirAll(valid, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(valid, "yodea.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := ReadProject(valid, "")
			if err == nil {
				t.Fatalf("expected hard error for %s yodea.json, got nil", name)
			}
			if !strings.Contains(err.Error(), ConfigFile) {
				t.Fatalf("error should name %s, got %v", ConfigFile, err)
			}
		})
	}
}

func TestReadProjectFallsBackToFolderWhenNoConfig(t *testing.T) {
	isolateSession(t)
	dir := filepath.Join(t.TempDir(), "validproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ReadProject(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "validproj" {
		t.Fatalf("fallback = %q, want validproj", got)
	}
}

func TestDeploySendsRawTarball(t *testing.T) {
	isolateSession(t)
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://demo.example/","project":"demo","label":"demo","files":1,"bytes":10}`))
	}))
	defer srv.Close()

	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(srv.URL, "dev")
	res, err := c.Deploy("demo", dist)
	if err != nil {
		t.Fatal(err)
	}
	if res.URL == "" {
		t.Fatal("expected preview URL in deploy result")
	}
	if gotCT != "application/gzip" {
		t.Fatalf("Content-Type = %q, want raw tarball application/gzip", gotCT)
	}
}
