package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/sites"
	"github.com/g-imhoff/yodea/internal/store"
)

type failUpsertStore struct {
	store.Storage
	err error
}

func (f *failUpsertStore) UpsertSite(_ string, _ *store.Site) error {
	return f.err
}

func TestFailedUpsertPreservesLiveSite(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")

	rec := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "<h1>v1</h1>"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("first deploy = %d: %s", rec.Code, rec.Body.String())
	}
	var dep struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dep); err != nil {
		t.Fatal(err)
	}
	if dep.Label == "" {
		t.Fatal("empty label")
	}
	indexPath := filepath.Join(sites.SiteDir(s.cfg.DataDir, dep.Label), "index.html")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}
	if !strings.Contains(string(before), "v1") {
		t.Fatalf("live file = %q, want v1", string(before))
	}

	orig := s.metadb
	s.metadb = &failUpsertStore{Storage: orig, err: errors.New("label is taken")}
	rec2 := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "<h1>v2</h1>"}))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("failed deploy = %d, want 409: %s", rec2.Code, rec2.Body.String())
	}
	s.metadb = orig

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("live file missing after failed deploy: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("live file changed after failed deploy: was %q, now %q", string(before), string(after))
	}
	if strings.Contains(string(after), "v2") {
		t.Fatalf("live file contains v2 after failed deploy: %q", string(after))
	}

	rec3 := doReq(s, http.MethodGet, dep.Label+"."+testDomain, "/", alice, nil, "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("preview after failed deploy = %d: %s", rec3.Code, rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), "v1") {
		t.Fatalf("preview body = %q, want v1", rec3.Body.String())
	}
	if strings.Contains(rec3.Body.String(), "v2") {
		t.Fatalf("preview serves v2 after failed deploy: %q", rec3.Body.String())
	}
}
