package server

import (
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/g-imhoff/yodea/internal/sites"
)

// handlePreview serves one site's static files to any logged-in user and
// records document-navigation visits in that viewer's personal history.
// Only document navigations (root, directory index, extensionless routes,
// or Accept text/html documents) record views; asset hits (.js/.css/images
// etc.) never do, so scrolling a preview's assets does not spam history.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, label string) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		host, ok := normalizeHost(r.Host)
		if !ok {
			writeErr(w, "unknown_site", "unknown site")
			return
		}
		next := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, "https://"+s.cfg.BaseDomain+"/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, "unknown_site", "unknown site")
		return
	}
	site := s.metadb.SiteByLabel(token, label)
	if site == nil {
		writeErr(w, "unknown_site", "unknown site")
		return
	}
	// Deny encoded traversal/dot tricks before Clean normalizes them away:
	// %2e decodes to "." so /%2e%2e/secret would otherwise collapse to a
	// legitimate-looking path and serve index.html with a recorded view.
	if previewPathHasEncodedDot(r) {
		writeErr(w, "not_found", "not found")
		return
	}
	root := sites.SiteDir(s.cfg.DataDir, label)
	upath := path.Clean("/" + r.URL.Path)
	rel := strings.TrimPrefix(upath, "/")
	if rel == "" {
		serveFile(w, r, filepath.Join(root, "index.html"))
		if err := s.metadb.RecordView(token, userID, label); err != nil {
			log.Printf("record view user=%s label=%s: %v", userID, label, err)
		}
		return
	}
	// Deny dot segments anywhere in the path before touching disk.
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			writeErr(w, "not_found", "not found")
			return
		}
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, root+string(filepath.Separator)) && full != root {
		writeErr(w, "not_found", "not found")
		return
	}
	if info, err := os.Stat(full); err == nil {
		if info.IsDir() {
			serveFile(w, r, filepath.Join(full, "index.html"))
			if err := s.metadb.RecordView(token, userID, label); err != nil {
				log.Printf("record view user=%s label=%s: %v", userID, label, err)
			}
			return
		}
		serveFile(w, r, full)
		// Asset hits serve bytes but record nothing: only documents count.
		if isDocumentNav(rel, r) {
			if err := s.metadb.RecordView(token, userID, label); err != nil {
				log.Printf("record view user=%s label=%s: %v", userID, label, err)
			}
		}
		return
	}
	// SPA fallback only for extensionless routes and HTML navigations.
	// Missing .js/.css return 404 so breakage stays visible.
	if path.Ext(rel) == "" || (strings.Contains(r.Header.Get("Accept"), "text/html") && !isAssetExt(path.Ext(rel))) {
		serveFile(w, r, filepath.Join(root, "index.html"))
		if err := s.metadb.RecordView(token, userID, label); err != nil {
			log.Printf("record view user=%s label=%s: %v", userID, label, err)
		}
		return
	}
	writeErr(w, "not_found", "not found")
}

func isAssetExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
		".woff", ".woff2", ".ttf", ".map", ".json", ".txt", ".xml", ".webp", ".avif":
		return true
	}
	return false
}

// isDocumentNav reports whether a preview file hit is a document
// navigation worth recording: extensionless routes or Accept text/html
// documents that are not known asset types.
func isDocumentNav(rel string, r *http.Request) bool {
	if path.Ext(rel) == "" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html") && !isAssetExt(path.Ext(rel))
}

// previewPathHasEncodedDot rejects %2e (encoded ".") anywhere in the raw
// escaped path, plus encoded slashes that could smuggle separators, before
// path.Clean normalizes them away.
func previewPathHasEncodedDot(r *http.Request) bool {
	escaped := strings.ToLower(r.URL.EscapedPath())
	return strings.Contains(escaped, "%2e") || strings.Contains(escaped, "%252e") ||
		strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c")
}

func serveFile(w http.ResponseWriter, r *http.Request, full string) {
	f, err := os.Open(full)
	if err != nil {
		writeErr(w, "not_found", "not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeErr(w, "not_found", "not found")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Shared parent-domain risk: previews run as <label>.<BaseDomain>,
	// sibling to the central host under one parent domain, so a malicious
	// preview could otherwise frame-bust, clickjack, or script against
	// same-site context. Sandbox without allow-same-origin keeps each
	// preview in an opaque origin (its JS cannot reach cookies, which are
	// HttpOnly anyway, nor other previews), and DENY keeps previews
	// unframeable. allow-scripts stays so static Vite bundles still run.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	w.Header().Set("X-Frame-Options", "DENY")
	// Conservative MIME: known web types by extension, octet-stream
	// otherwise, so uploaded content can never sniff into script.
	if ct := mime.TypeByExtension(filepath.Ext(strings.ToLower(full))); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
