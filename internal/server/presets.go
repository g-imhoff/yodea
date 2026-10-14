package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

const (
	previewAPIPrefix = "/_yodea"
	presetBodyMax    = store.PresetDataMax
	presetNameMax    = 60
)

type presetView struct {
	Name      string          `json:"name"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func presetOut(p store.Preset) presetView {
	return presetView{Name: p.Name, Data: p.Data, UpdatedAt: p.UpdatedAt.UTC()}
}

func isPreviewAPIPath(p string) bool {
	return p == previewAPIPrefix || strings.HasPrefix(p, previewAPIPrefix+"/")
}

func validPresetName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > presetNameMax {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	first, _ := utf8.DecodeRuneInString(name)
	last, _ := utf8.DecodeLastRuneInString(name)
	return !unicode.IsSpace(first) && !unicode.IsSpace(last)
}

func fetchSiteAllowed(r *http.Request) bool {
	for _, v := range r.Header.Values("Sec-Fetch-Site") {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "same-origin", "none":
		default:
			return false
		}
	}
	return true
}

func sameOriginWrite(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) handlePreviewAPI(w http.ResponseWriter, r *http.Request, label string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !fetchSiteAllowed(r) {
		writeErr(w, "csrf_required", "cross-site request refused")
		return
	}
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, "login_required", "login required")
		return
	}
	if sites.ValidateLabel(label) != nil || s.metadb.SiteByLabel(token, label) == nil {
		writeErr(w, "unknown_site", "unknown site")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), previewAPIPrefix+"/")
	seg, isItem := strings.CutPrefix(rest, "presets/")
	switch {
	case ok && rest == "presets":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeErr(w, "method_not_allowed", "method not allowed")
			return
		}
		s.listPresets(w, token, userID, label)
	case ok && isItem && !strings.Contains(seg, "/"):
		if r.Method != http.MethodPut && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "PUT, DELETE")
			writeErr(w, "method_not_allowed", "method not allowed")
			return
		}
		if !sameOriginWrite(r) {
			writeErr(w, "csrf_required", "same-origin Origin header required")
			return
		}
		name, err := url.PathUnescape(seg)
		if err != nil || !validPresetName(name) {
			writeErr(w, "bad_request", "bad preset name")
			return
		}
		if r.Method == http.MethodPut {
			s.putPreset(w, r, token, userID, label, name)
		} else {
			s.deletePreset(w, token, userID, label, name)
		}
	default:
		writeErr(w, "not_found", "not found")
	}
}

func (s *Server) listPresets(w http.ResponseWriter, token, userID, label string) {
	list, err := s.metadb.ListPresets(token, userID, label)
	if err != nil {
		log.Printf("preset list failed user=%s label=%s cause=%v", userID, label, err)
		writeErr(w, "unknown", "could not list presets")
		return
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].Name < list[j].Name
	})
	out := make([]presetView, 0, len(list))
	for _, p := range list {
		out = append(out, presetOut(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": out})
}

func (s *Server) putPreset(w http.ResponseWriter, r *http.Request, token, userID, label, name string) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErr(w, "bad_request", "content type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, presetBodyMax))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, "upload_too_large", "preset exceeds 16384 bytes")
		} else {
			writeErr(w, "bad_request", "could not read body")
		}
		return
	}
	var data bytes.Buffer
	if !utf8.Valid(body) || json.Compact(&data, body) != nil || data.Len() == 0 || data.Bytes()[0] != '{' {
		writeErr(w, "bad_request", "body must be a JSON object")
		return
	}
	p, err := s.metadb.PutPreset(token, store.Preset{UserID: userID, Label: label, Name: name, Data: data.Bytes()})
	if errors.Is(err, store.ErrPresetLimit) {
		writeErr(w, "preset_limit", "preset limit reached")
		return
	}
	if err != nil {
		log.Printf("preset save failed user=%s label=%s name=%q cause=%v", userID, label, name, err)
		writeErr(w, "unknown", "could not save preset")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preset": presetOut(p)})
}

func (s *Server) deletePreset(w http.ResponseWriter, token, userID, label, name string) {
	ok, err := s.metadb.DeletePreset(token, userID, label, name)
	if err != nil {
		log.Printf("preset delete failed user=%s label=%s name=%q cause=%v", userID, label, name, err)
		writeErr(w, "unknown", "could not delete preset")
		return
	}
	if !ok {
		writeErr(w, "not_found", "no such preset")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
