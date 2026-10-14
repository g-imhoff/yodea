package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/g-imhoff/yodea/internal/auth"
)

// handleHealth is readiness, not just liveness: 200 {"status":"ok"}
// only when the data dir is usable and the metadata store answers. Dead
// disk or dead store reads 503 so traffic moves elsewhere.

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if err := s.checkReady(); err != nil {
		log.Printf("healthz unavailable: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// checkReady probes the data dir (stat, is-dir, writable) plus one store
// Probe call. Empty (len 0) stays healthy here.
func (s *Server) checkReady() error {
	info, err := os.Stat(s.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if !info.IsDir() {
		return errors.New("data dir is not a directory")
	}
	// Deploys extract under DataDir/sites and the local store rewrites
	// db.json here, so a read-only DataDir must read unready.
	probe, err := os.CreateTemp(s.cfg.DataDir, ".healthz-*")
	if err != nil {
		return fmt.Errorf("data dir not writable: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	if s.metadb == nil {
		return errors.New("metadata store not configured")
	}
	if err := s.metadb.Probe(); err != nil {
		return fmt.Errorf("metadata store unreachable: %w", err)
	}
	return nil
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireCentralHost(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, auth.PublicConfig{
		SupabaseURL: s.cfg.SupabaseURL,
		AnonKey:     s.cfg.AnonKey,
	})
}
