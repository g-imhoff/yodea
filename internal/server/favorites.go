package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

func (s *Server) handleViews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method_not_allowed", "method not allowed")
		return
	}
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, "login_required", "login required")
		return
	}
	views := s.metadb.RecentViews(token, userID, store.ViewReturn)
	if views == nil {
		views = []store.View{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": views})
}

type favoriteRow struct {
	Label   string `json:"label"`
	Link    string `json:"link"`
	Owner   string `json:"owner"`
	Project string `json:"project"`
}

func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, "login_required", "login required")
		return
	}
	if r.Method != http.MethodGet && !s.checkCSRF(w, r, token) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := []favoriteRow{}
		for _, f := range s.metadb.ListFavorites(token, userID) {
			// Skip orphans: deleted previews drop out of favorites.
			site := s.metadb.SiteByLabel(token, f.Label)
			if site == nil {
				continue
			}
			out = append(out, favoriteRow{
				Label:   f.Label,
				Link:    "https://" + f.Label + "." + s.cfg.BaseDomain + "/",
				Owner:   site.UserID,
				Project: site.Project,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"favorites": out})
	case http.MethodPost:
		var body struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeErr(w, "bad_request", "bad request")
			return
		}
		if err := sites.ValidateLabel(body.Label); err != nil {
			writeErr(w, "bad_request", "bad label: "+err.Error())
			return
		}
		// A viewer may favorite any preview they can open.
		site := s.metadb.SiteByLabel(token, body.Label)
		if site == nil {
			writeErr(w, "unknown_site", "unknown site")
			return
		}
		if err := s.metadb.AddFavorite(token, userID, body.Label); err != nil {
			log.Printf("favorite add failed user=%s label=%s cause=%v", userID, body.Label, err)
			writeErr(w, "unknown", "could not save favorite")
			return
		}
		writeJSON(w, http.StatusOK, favoriteRow{
			Label:   body.Label,
			Link:    "https://" + body.Label + "." + s.cfg.BaseDomain + "/",
			Owner:   site.UserID,
			Project: site.Project,
		})
	default:
		writeErr(w, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, "method_not_allowed", "method not allowed")
		return
	}
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, "login_required", "login required")
		return
	}
	if !s.checkCSRF(w, r, token) {
		return
	}
	label := strings.TrimPrefix(r.URL.Path, "/api/favorites/")
	if strings.Contains(label, "/") || sites.ValidateLabel(label) != nil {
		writeErr(w, "bad_request", "bad label")
		return
	}
	ok, err := s.metadb.RemoveFavorite(token, userID, label)
	if err != nil {
		log.Printf("favorite remove failed user=%s label=%s cause=%v", userID, label, err)
		writeErr(w, "unknown", "could not remove favorite")
		return
	}
	if !ok {
		writeErr(w, "no_such_favorite", "no such favorite")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
