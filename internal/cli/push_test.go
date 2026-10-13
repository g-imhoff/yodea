package cli

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/client"
)

func TestPushRejectsPositional(t *testing.T) {
	isolateSession(t)
	_, _, err := runCLI(t, nil, "--server", "http://127.0.0.1:8093", "push", "extra")
	if err == nil {
		t.Fatal("expected push with positional to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for push with positional, got %v", err)
	}
	if !strings.Contains(err.Error(), "[--static]") {
		t.Fatalf("push positional usage should include --static, got %v", err)
	}
	if !strings.Contains(err.Error(), "already built dist/") || !strings.Contains(err.Error(), "does not build") {
		t.Fatalf("push positional usage should explain prepared dist behavior, got %v", err)
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
	out, _, err := runCLI(t, nil, "--server", srv.URL, "push", "--dir", dir, "--project", "demo")
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

func writeReactPushMarkers(t *testing.T, dir string, missing string) {
	t.Helper()
	if missing == "absent" {
		return
	}
	if missing == "malformed" {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	pkg := `{"dependencies":{"react":"^18","react-dom":"^18"},"devDependencies":{"vite":"^5"}}`
	if missing == "react" {
		pkg = `{"dependencies":{"react-dom":"^18"},"devDependencies":{"vite":"^5"}}`
	}
	if missing == "react-dom" {
		pkg = `{"dependencies":{"react":"^18"},"devDependencies":{"vite":"^5"}}`
	}
	if missing == "vite" {
		pkg = `{"dependencies":{"react":"^18","react-dom":"^18"}}`
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if missing != "vite-config" {
		if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(`export default {}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if missing != "tsconfig" {
		if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if missing != "tsx" {
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "App.tsx"), []byte(`export default function App() { return null }`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeStaticDist(t *testing.T, dir string) string {
	t.Helper()
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(filepath.Join(dist, "assets", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets", "styles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<h1>static</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("console.log('static')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "modules", "app.mjs"), []byte("export const mode = 'static';"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "styles", "site.css"), []byte("body { color: rebeccapurple; }"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dist
}

func TestUsageDescribesStaticPush(t *testing.T) {
	out, _, err := runCLI(t, nil, "help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"push [--dir D] [--project P] [--static]", "already built", "dist/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage missing %q: %q", want, out)
		}
	}
}

func TestPushFlagHelpDescribesStaticMode(t *testing.T) {
	out, _, err := runCLI(t, nil, "--server", "http://127.0.0.1:8093", "push", "--help")
	if err != nil {
		t.Fatalf("push --help returned error: %v", err)
	}
	for _, want := range []string{"-static", "prepared dist/", "without React TypeScript validation"} {
		if !strings.Contains(out, want) {
			t.Fatalf("push flag help missing %q: %q", want, out)
		}
	}
}

func TestPushStaticUploadsOnlyDistAndUsesProjectPrecedence(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	var gotPath string
	var gotContentType string
	var gotNames []string
	gotFiles := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("tar request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			gotNames = append(gotNames, h.Name)
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Errorf("read %q: %v", h.Name, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			gotFiles[h.Name] = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://demo.example/","project":"explicit","files":2,"bytes":10}`))
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "yodea.json"), []byte(`{"project":"config"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStaticDist(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "dist")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, nil, "--server", srv.URL, "push", "--dir", dir, "--project", "explicit", "--static"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/sites/explicit/deploy" {
		t.Fatalf("deploy path = %q, want explicit project", gotPath)
	}
	if gotContentType != "application/gzip" {
		t.Fatalf("content type = %q, want application/gzip", gotContentType)
	}
	wantNames := []string{"assets/app.js", "assets/modules/app.mjs", "assets/styles/site.css", "index.html"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("archive names = %q, want %q", gotNames, wantNames)
	}
	wantFiles := map[string]string{
		"assets/app.js":          "console.log('static')",
		"assets/modules/app.mjs": "export const mode = 'static';",
		"assets/styles/site.css": "body { color: rebeccapurple; }",
		"index.html":             "<h1>static</h1>",
	}
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Fatalf("archive files = %#v, want %#v", gotFiles, wantFiles)
	}
	for _, name := range gotNames {
		if strings.Contains(name, "package.json") || strings.Contains(name, "yodea.json") {
			t.Fatalf("archive leaked project-root file %q", name)
		}
	}
}

func TestPushStaticRejectsArchiveGuardsWithoutUpload(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://demo.example/","project":"demo","files":1,"bytes":1}`))
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{name: "missing index", setup: func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory index", setup: func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "dist", "index.html"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "dotfile", setup: func(t *testing.T, dir string) {
			dist := writeStaticDist(t, dir)
			if err := os.WriteFile(filepath.Join(dist, ".env"), []byte("secret"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "dot directory", setup: func(t *testing.T, dir string) {
			dist := writeStaticDist(t, dir)
			if err := os.MkdirAll(filepath.Join(dist, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dist, ".git", "config"), []byte("secret"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, dir string) {
			dist := writeStaticDist(t, dir)
			target := filepath.Join(dir, "outside.js")
			if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dist, "link.js")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlinked dist", setup: func(t *testing.T, dir string) {
			target := filepath.Join(dir, "built-dist")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("<h1>static</h1>"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "dist")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invalid project", setup: func(t *testing.T, dir string) {
			writeStaticDist(t, dir)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests = 0
			dir := t.TempDir()
			tc.setup(t, dir)
			args := []string{"--server", srv.URL, "push", "--dir", dir, "--project", "demo", "--static"}
			if tc.name == "invalid project" {
				args = []string{"--server", srv.URL, "push", "--dir", dir, "--project", "-bad-", "--static"}
			}
			if _, _, err := runCLI(t, nil, args...); err == nil {
				t.Fatal("expected static push guard error")
			}
			if requests != 0 {
				t.Fatalf("guard made %d HTTP requests", requests)
			}
		})
	}
}

func TestPushDefaultAndStaticFalseRejectReactValidationMatrixWithoutUpload(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://demo.example/","project":"demo","files":1,"bytes":1}`))
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	markers := []struct {
		missing string
		want    string
	}{
		{missing: "absent", want: "not a Vite React TS app: no package.json"},
		{missing: "malformed", want: "not a Vite React TS app: package.json does not parse"},
		{missing: "react", want: `not a Vite React TS app: missing package.json dependency "react"`},
		{missing: "react-dom", want: `not a Vite React TS app: missing package.json dependency "react-dom"`},
		{missing: "vite", want: `not a Vite React TS app: missing package.json devDependency "vite"`},
		{missing: "vite-config", want: "not a Vite React TS app: missing vite config (vite.config.ts)"},
		{missing: "tsconfig", want: "not a Vite React TS app: missing tsconfig.json (TypeScript marker)"},
		{missing: "tsx", want: "not a Vite React TS app: missing a .tsx source file (React marker)"},
	}
	for _, staticArg := range []string{"", "--static=false"} {
		for _, marker := range markers {
			t.Run(marker.missing+"/"+staticArg, func(t *testing.T) {
				requests = 0
				dir := t.TempDir()
				writeReactPushMarkers(t, dir, marker.missing)
				writeStaticDist(t, dir)
				args := []string{"--server", srv.URL, "push", "--dir", dir, "--project", "demo"}
				if staticArg != "" {
					args = append(args, staticArg)
				}
				if _, _, err := runCLI(t, nil, args...); err == nil || !strings.Contains(err.Error(), marker.want) {
					t.Fatalf("push error = %v, want React TypeScript validation error containing %q", err, marker.want)
				}
				if requests != 0 {
					t.Fatalf("validation made %d HTTP requests", requests)
				}
			})
		}
	}
}
