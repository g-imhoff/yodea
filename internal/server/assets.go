package server

import (
	"embed"
	"net/http"
)

// The dashboard bundle is built from the designer webui and copied to
// internal/server/web/dist BEFORE `go build`, e.g.:
//
//	cp -r <design-webui>/dist internal/server/web/dist
//	go build ./...
//
// The directory must exist at compile time for go:embed (kept via .gitkeep
// when no bundle is vendored); at runtime HasAssets reports whether a real
// bundle is present. A missing bundle never fails the boot: /login and the
// dashboard serve a clear message while the API and previews stay live.

//go:embed all:web/dist
var distFS embed.FS

// HasAssets reports whether the embedded dashboard bundle has index.html.
func HasAssets() bool {
	b, err := distFS.ReadFile("web/dist/index.html")
	return err == nil && len(b) > 0
}

// serveUI serves the dashboard bundle, or a clear degraded message when the
// UI was not built in. API and preview routes are unaffected.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if b, err := distFS.ReadFile("web/dist/index.html"); err == nil && len(b) > 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("yodea dashboard UI is not built into this server. " +
		"Copy the webui dist bundle to internal/server/web/dist and rebuild. " +
		"The API and existing previews keep working."))
}
