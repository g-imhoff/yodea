package client

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/g-imhoff/yodea/internal/sites"
)

// ConfigFile links a folder to a yodea project.
const ConfigFile = "yodea.json"

// ProjectConfig is the on-disk link file. Only this file is ever written
// into an existing folder by link mode.
type ProjectConfig struct {
	Project string `json:"project"`
}

// CheckProject validates a project name. It delegates directly to
// sites.CheckProjectName with no local error text.
func CheckProject(raw string) error {
	return sites.CheckProjectName(raw)
}

// ValidateReactTS fast-fails when dir is not a Vite React TS app. It
// checks the markers push depends on: package.json with react plus
// react-dom and vite, a vite config, a tsconfig, and at least one .tsx
// source file. The returned error lists every missing marker.
func ValidateReactTS(dir string) error {
	var missing []string
	pkgPath := filepath.Join(dir, "package.json")
	pkgData, err := os.ReadFile(pkgPath)
	if err != nil {
		return fmt.Errorf("not a Vite React TS app: no package.json in %s", dir)
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(pkgData, &pkg); err != nil {
		return fmt.Errorf("not a Vite React TS app: package.json does not parse: %v", err)
	}
	has := func(deps map[string]string, name string) bool {
		_, ok := deps[name]
		return ok
	}
	if !has(pkg.Dependencies, "react") && !has(pkg.DevDependencies, "react") {
		missing = append(missing, "package.json dependency \"react\"")
	}
	if !has(pkg.Dependencies, "react-dom") && !has(pkg.DevDependencies, "react-dom") {
		missing = append(missing, "package.json dependency \"react-dom\"")
	}
	if !has(pkg.Dependencies, "vite") && !has(pkg.DevDependencies, "vite") {
		missing = append(missing, "package.json devDependency \"vite\"")
	}
	for _, name := range []string{"vite.config.ts", "vite.config.js", "vite.config.mts", "vite.config.mjs"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			goto haveViteConfig
		}
	}
	missing = append(missing, "vite config (vite.config.ts)")
haveViteConfig:
	foundTsconfig := false
	for _, name := range []string{"tsconfig.json", "tsconfig.app.json", "tsconfig.node.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			foundTsconfig = true
			break
		}
	}
	if !foundTsconfig {
		missing = append(missing, "tsconfig.json (TypeScript marker)")
	}
	foundTsx := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || foundTsx {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "dist" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".tsx") {
			foundTsx = true
		}
		return nil
	})
	if !foundTsx {
		missing = append(missing, "a .tsx source file (React marker)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("not a Vite React TS app: missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// ReadProject resolves the project name: explicit flag first, then the
// folder's yodea.json link file, then the directory base name. A present
// but unreadable, badly-formed, or invalid yodea.json is a hard error
// naming the file; the folder name is only a fallback when no link file
// exists at all.
func ReadProject(dir, flag string) (string, error) {
	if strings.TrimSpace(flag) != "" {
		name := strings.TrimSpace(flag)
		if err := CheckProject(name); err != nil {
			return "", err
		}
		return name, nil
	}
	cfgPath := filepath.Join(dir, ConfigFile)
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		var cfg ProjectConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("bad config %s: invalid JSON: %w", ConfigFile, err)
		}
		name := strings.TrimSpace(cfg.Project)
		if name == "" {
			return "", fmt.Errorf("bad config %s: missing \"project\" name", ConfigFile)
		}
		if err := CheckProject(name); err != nil {
			return "", fmt.Errorf("bad config %s: %w", ConfigFile, err)
		}
		return name, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("bad config %s: %w", ConfigFile, err)
	}
	// Resolve via absolute path (mirroring cmdInit) so dir="." infers the
	// current folder name instead of the literal ".".
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	base := filepath.Base(abs)
	if base == "." || base == "/" || base == "" {
		return "", fmt.Errorf("cannot infer a project name here; pass --project NAME")
	}
	if err := CheckProject(base); err != nil {
		return "", fmt.Errorf("cannot infer a project name from folder %q: %w; pass --project NAME", base, err)
	}
	return base, nil
}

// Init links or scaffolds dir. Folders that already contain package.json
// (or --link) take the link path: only yodea.json is written, user files
// are never touched. Otherwise dir is scaffolded as a fresh Vite React TS
// app; a non-empty target is refused unless force is given, and even then
// existing files are never overwritten.
func Init(dir, project string, force, link bool) error {
	if err := CheckProject(project); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if link {
		return linkDir(dir, project, force)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return linkDir(dir, project, force)
	}
	return Scaffold(dir, project, force)
}

func linkDir(dir, project string, force bool) error {
	if err := ValidateReactTS(dir); err != nil {
		return fmt.Errorf("cannot link: %w", err)
	}
	cfgPath := filepath.Join(dir, ConfigFile)
	if _, err := os.Stat(cfgPath); err == nil && !force {
		return fmt.Errorf("%s: already linked (yodea.json exists); use --force to re-link to a new project name", cfgPath)
	}
	// Link mode owns yodea.json: --force overwrites it. Scaffolded user
	// files are still never overwritten (see Scaffold).
	if err := os.WriteFile(cfgPath, []byte(`{"project": "`+project+"\"}\n"), 0o644); err != nil {
		return err
	}
	return os.Chmod(cfgPath, 0o644)
}

// Scaffold writes a fresh Vite React TS app into dir. Files that already
// exist are never overwritten, except the owned yodea.json link file which
// --force refreshes to the new project. A non-empty dir is refused unless
// force.
func Scaffold(dir, project string, force bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("target dir %s is not empty; use --force to scaffold into it (existing files are never overwritten) or --link to link this folder as-is", dir)
	}
	files := scaffoldFiles(project)
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Stat(full); err == nil {
			// Owned link file: --force refreshes it to the new
			// project so we never report a new name while keeping
			// a stale one. All other user files are never touched.
			if force && name == ConfigFile {
				if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
					return err
				}
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := writeIfMissing(full, content); err != nil {
			return err
		}
	}
	return nil
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // keep the user's file
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// PackDist writes a gzipped tar of distDir to w: regular files only,
// dotfiles refused (the server rejects them), index.html required at top
// level. Static dist only: symlinks and other specials are refused.
func PackDist(distDir string, w io.Writer) error {
	distInfo, err := os.Lstat(distDir)
	if err != nil || distInfo.Mode()&os.ModeSymlink != 0 || !distInfo.IsDir() {
		return fmt.Errorf("no regular dist/index.html in %s; prepare dist/index.html before pushing", distDir)
	}
	indexPath := filepath.Join(distDir, "index.html")
	indexInfo, err := os.Lstat(indexPath)
	if err != nil || !indexInfo.Mode().IsRegular() {
		return fmt.Errorf("no regular dist/index.html in %s; prepare dist/index.html before pushing", distDir)
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	count := 0
	walkErr := filepath.WalkDir(distDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(distDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".") {
			return fmt.Errorf("refusing dotfile %q (dist never needs dotfiles)", filepath.ToSlash(filepath.Join(distDir, rel)))
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular file %q (static dist only)", rel)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:    filepath.ToSlash(rel),
			Mode:    0o644,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = f.Close()
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		count++
		if count > sites.MaxFiles {
			return fmt.Errorf("dist exceeds %d files", sites.MaxFiles)
		}
		return nil
	})
	if walkErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		return walkErr
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// npmName maps a project name to a safe package.json name.
func npmName(project string) string {
	s := strings.ToLower(project)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "yodea-app"
	}
	return out
}

func scaffoldFiles(project string) map[string]string {
	name := npmName(project)
	return map[string]string{
		"package.json": `{
  "name": "` + name + `",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "react": "^18.3.1",
    "react-dom": "^18.3.1"
  },
  "devDependencies": {
    "@types/react": "^18.3.3",
    "@types/react-dom": "^18.3.0",
    "@vitejs/plugin-react": "^4.3.1",
    "typescript": "^5.5.3",
    "vite": "^5.4.0"
  }
}
`,
		"vite.config.ts": `import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
})
`,
		"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true
  },
  "include": ["src"]
}
`,
		"index.html": `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>` + name + `</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
`,
		"src/main.tsx": `import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
`,
		"src/App.tsx": `export default function App() {
  return (
    <main>
      <h1>Hello from yodea</h1>
      <p>Edit src/App.tsx, run npm run build, then yodea push.</p>
    </main>
  )
}
`,
		"src/index.css": `:root {
  font-family: system-ui, sans-serif;
}

main {
  max-width: 40rem;
  margin: 4rem auto;
  padding: 0 1rem;
}
`,
		"src/vite-env.d.ts": "/// <reference types=\"vite/client\" />\n",
		ConfigFile:          `{"project": "` + project + "\"}\n",
	}
}
