package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/g-imhoff/yodea/internal/auth"
	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

func isLabelTaken(err error) bool {
	return err != nil && strings.Contains(err.Error(), "label is taken")
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, project string) {
	token, userID, err := s.tokenFor(r)
	if err != nil {
		writeErr(w, "login_required", "login required")
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", "unknown", project, "", err)
		return
	}
	if !s.checkCSRF(w, r, token) {
		return
	}
	if !validProject(project) {
		writeErr(w, "bad_project", "bad project name")
		log.Printf("deploy failed user=%s project=%s label=%s cause=%s", userID, project, "", "bad project name")
		return
	}
	project = sites.Sanitize(project)
	arc, err := readArchive(w, r)
	if err != nil {
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, "", err)
		return // readArchive already answered
	}
	label := s.labelFor(token, userID, project)
	if err := sites.ValidateLabel(label); err != nil {
		writeErr(w, "bad_request", "bad label: "+err.Error())
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		return
	}
	// Reserve the label in storage before touching disk. For a redeploy
	// keep the existing file counts so a later extraction failure leaves
	// metadata consistent with the live files on disk. For a new label the
	// placeholder has zero counts and is cleaned up if we fail before the
	// final Upsert.
	existingBefore := s.metadb.SiteByLabel(token, label)
	reserveFiles := 0
	var reserveBytes int64
	if existingBefore != nil && existingBefore.UserID == userID {
		reserveFiles = existingBefore.Files
		reserveBytes = existingBefore.Bytes
	}
	isNew := existingBefore == nil
	cleanupPlaceholder := func() {
		if !isNew {
			return
		}
		if cur := s.metadb.SiteByLabel(token, label); cur != nil && cur.UserID == userID && cur.Files == 0 && cur.Bytes == 0 {
			s.metadb.DeleteSite(token, userID, project)
		}
	}
	if err := s.metadb.UpsertSite(token, &store.Site{
		UserID:  userID,
		Project: project,
		Label:   label,
		Files:   reserveFiles,
		Bytes:   reserveBytes,
	}); err != nil {
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		if isLabelTaken(err) {
			writeErr(w, "label_taken", err.Error())
		} else {
			writeErr(w, "deploy_failed", "deploy failed")
		}
		return
	}
	// Each deploy extracts to its own unique staging dir so concurrent
	// deploys for one label never share a path.
	staging, err := sites.NewStagingDir(s.cfg.DataDir)
	if err != nil {
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		cleanupPlaceholder()
		writeErr(w, "deploy_failed", "deploy failed")
		return
	}
	res, err := sites.ExtractToStaging(bytes.NewReader(arc), staging, 0, 0)
	if err != nil {
		os.RemoveAll(staging)
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		cleanupPlaceholder()
		if sites.IsValidationError(err) {
			writeErr(w, "rejected_upload", "rejected upload: "+err.Error())
		} else {
			writeErr(w, "deploy_failed", "deploy failed")
		}
		return
	}
	if err := s.metadb.UpsertSite(token, &store.Site{
		UserID:  userID,
		Project: project,
		Label:   label,
		Files:   res.Files,
		Bytes:   res.Bytes,
	}); err != nil {
		// Conflict: delete only this deploy's staging dir, never dest, so
		// the previously deployed files stay live.
		os.RemoveAll(staging)
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		if isLabelTaken(err) {
			writeErr(w, "label_taken", err.Error())
		} else {
			cleanupPlaceholder()
			writeErr(w, "deploy_failed", "deploy failed")
		}
		return
	}
	// Owner recheck closes the file-swap half of the label race: even if
	// storage let a concurrent writer claim the label between our Upsert
	// and the disk swap, abort with 409 deleting only staging, never dest.
	if cur := s.metadb.SiteByLabel(token, label); cur == nil || cur.UserID != userID {
		os.RemoveAll(staging)
		log.Printf("deploy failed user=%s project=%s label=%s cause=%s", userID, project, label, "label is taken (owner changed)")
		writeErr(w, "label_taken", "label is taken")
		return
	}
	if err := sites.ReplaceSite(s.cfg.DataDir, label, staging); err != nil {
		os.RemoveAll(staging)
		log.Printf("deploy failed user=%s project=%s label=%s cause=%v", userID, project, label, err)
		if isNew {
			cleanupPlaceholder()
			// cleanupPlaceholder only drops zero-count reserves, but the
			// final Upsert already overwrote the reserve with new counts,
			// so ensure our placeholder row is gone.
			if cur := s.metadb.SiteByLabel(token, label); cur != nil && cur.UserID == userID {
				if _, derr := s.metadb.DeleteSite(token, userID, project); derr != nil {
					log.Printf("deploy cleanup failed user=%s project=%s label=%s cause=%v", userID, project, label, derr)
				}
			}
		} else if rerr := s.metadb.UpsertSite(token, &store.Site{
			UserID:  userID,
			Project: project,
			Label:   label,
			Files:   reserveFiles,
			Bytes:   reserveBytes,
		}); rerr != nil {
			log.Printf("deploy restore failed user=%s project=%s label=%s cause=%v", userID, project, label, rerr)
		}
		writeErr(w, "deploy_failed", "deploy failed")
		return
	}
	url := "https://" + label + "." + s.cfg.BaseDomain + "/"
	log.Printf("deploy user=%s project=%s label=%s files=%d bytes=%d", userID, project, label, res.Files, res.Bytes)
	writeJSON(w, http.StatusOK, map[string]any{
		"url":     url,
		"project": project,
		"label":   label,
		"files":   res.Files,
		"bytes":   res.Bytes,
	})
}

// readArchive accepts a raw gzipped tar body or a multipart upload with the
// archive in the "archive" (or "file", or first file) part. Oversized
// payloads are rejected before extraction.
func readArchive(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	limit := int64(sites.MaxUploadBytes) + 1
	if ctype := r.Header.Get("Content-Type"); strings.HasPrefix(ctype, "multipart/") {
		// Total cap applies before multipart parsing so a huge ignored
		// field cannot exhaust time/memory while the per-part cap never
		// applies.
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		mr, err := r.MultipartReader()
		if err != nil {
			if isBodyTooLarge(err) {
				writeErr(w, "upload_too_large", "upload exceeds 30MB")
			} else {
				writeErr(w, "bad_request", "bad multipart body")
			}
			return nil, err
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if isBodyTooLarge(err) {
					writeErr(w, "upload_too_large", "upload exceeds 30MB")
				} else {
					writeErr(w, "bad_request", "bad multipart body")
				}
				return nil, err
			}
			name, file := part.FormName(), part.FileName()
			if file == "" && name != "archive" && name != "file" {
				n, err := io.Copy(io.Discard, io.LimitReader(part, limit))
				if err != nil {
					if isBodyTooLarge(err) {
						writeErr(w, "upload_too_large", "upload exceeds 30MB")
					} else {
						writeErr(w, "bad_request", "could not read upload")
					}
					return nil, err
				}
				if n >= limit {
					writeErr(w, "upload_too_large", "upload exceeds 30MB")
					return nil, errors.New("upload too large")
				}
				continue
			}
			buf, err := io.ReadAll(io.LimitReader(part, limit))
			if err != nil {
				if isBodyTooLarge(err) {
					writeErr(w, "upload_too_large", "upload exceeds 30MB")
				} else {
					writeErr(w, "bad_request", "could not read upload")
				}
				return nil, err
			}
			if int64(len(buf)) >= limit {
				writeErr(w, "upload_too_large", "upload exceeds 30MB")
				return nil, errors.New("upload too large")
			}
			return buf, nil
		}
		writeErr(w, "bad_request", "no archive file in upload")
		return nil, errors.New("no archive part")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	buf, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, "upload_too_large", "upload exceeds 30MB")
		return nil, err
	}
	if int64(len(buf)) >= limit {
		writeErr(w, "upload_too_large", "upload exceeds 30MB")
		return nil, errors.New("upload too large")
	}
	return buf, nil
}

func isBodyTooLarge(err error) bool {
	return err != nil && strings.Contains(err.Error(), "request body too large")
}

// labelFor reuses the author's existing label for the same project so
// redeploys overwrite. New projects claim LabelFor(user, project),
// suffixed on collision with another author's label. The suffix range runs
// to 999 with 63-char truncation of the suffixed candidate; genuine
// exhaustion still falls through to base so UpsertSite returns 409.
func (s *Server) labelFor(token, userID, project string) string {
	for _, site := range s.metadb.ListSites(token, userID) {
		if site.Project == project {
			return site.Label
		}
	}
	base := sites.LabelFor(auth.UserPart(userID), project)
	if other := s.metadb.SiteByLabel(token, base); other == nil {
		return base
	}
	for n := 2; n <= 999; n++ {
		suffix := fmt.Sprintf("-%d", n)
		candidate := base + suffix
		if len(candidate) > 63 {
			trunc := strings.TrimRight(base[:63-len(suffix)], "-")
			if trunc == "" {
				trunc = base[:63-len(suffix)]
			}
			candidate = trunc + suffix
		}
		if other := s.metadb.SiteByLabel(token, candidate); other == nil {
			return candidate
		}
	}
	return base // UpsertSite will return 409 on genuine exhaustion.
}
