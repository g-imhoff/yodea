package server

import (
	"log"
	"net/http"
	"strings"

	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	if !s.requireCentralHost(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, "method_not_allowed", "method not allowed")
		return
	}
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, "login_required", "login required")
		return
	}
	list := s.metadb.ListSites(token, userID)
	if list == nil {
		list = []*store.Site{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": list})
}

// handleSite routes POST .../deploy and DELETE .../{project}.
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	if !s.requireCentralHost(w, r) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/sites/")
	parts := strings.SplitN(rest, "/", 2)
	project := parts[0]
	if project == "" || strings.Contains(project, "/") {
		writeErr(w, "bad_project", "project required")
		return
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "deploy":
		s.handleDeploy(w, r, project)
	case r.Method == http.MethodDelete && len(parts) == 1:
		s.handleDelete(w, r, project)
	default:
		writeErr(w, "not_found", "not found")
	}
}

// validProject delegates to sites.CheckProjectName (single source of
// truth); the label builder sanitizes the rest into DNS-safe form.
func validProject(raw string) bool {
	return sites.CheckProjectName(raw) == nil
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, project string) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, "login_required", "login required")
		log.Printf("delete failed user=%s project=%s label=%s cause=%v", "unknown", project, "", err)
		return
	}
	if !s.checkCSRF(w, r, token) {
		return
	}
	if !validProject(project) {
		writeErr(w, "bad_project", "bad project name")
		log.Printf("delete failed user=%s project=%s label=%s cause=%s", userID, project, "", "bad project name")
		return
	}
	sanitized := sites.Sanitize(project)
	// Files first, then the catalog row: a disk failure leaves the row
	// so the user can retry instead of orphaning files with no record.
	var label, storedProject string
	for _, cand := range s.metadb.ListSites(token, userID) {
		if cand.Project == sanitized {
			label = cand.Label
			storedProject = cand.Project
			break
		}
	}
	if label == "" {
		writeErr(w, "no_such_project", "no such project")
		log.Printf("delete failed user=%s project=%s label=%s cause=%s", userID, project, "", "no such project")
		return
	}
	if err := sites.RemoveSite(s.cfg.DataDir, label); err != nil {
		log.Printf("delete failed user=%s project=%s label=%s cause=%v", userID, storedProject, label, err)
		writeErr(w, "unknown", "delete failed")
		return
	}
	site, err := s.metadb.DeleteSite(token, userID, sanitized)
	if err != nil {
		log.Printf("delete failed user=%s project=%s label=%s cause=%v", userID, project, "", err)
		writeErr(w, "unknown", "could not delete site")
		return
	}
	if site == nil {
		writeErr(w, "no_such_project", "no such project")
		log.Printf("delete failed user=%s project=%s label=%s cause=%s", userID, project, "", "no such project")
		return
	}
	log.Printf("delete user=%s project=%s label=%s", userID, site.Project, site.Label)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
