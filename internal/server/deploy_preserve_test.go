package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/auth"
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

type nukeStagingStore struct {
	store.Storage
	dataDir string
}

func (n *nukeStagingStore) UpsertSite(token string, site *store.Site) error {
	matches, _ := filepath.Glob(filepath.Join(n.dataDir, "sites", ".stage-*"))
	for _, m := range matches {
		os.RemoveAll(m)
	}
	return n.Storage.UpsertSite(token, site)
}

func TestReplaceSiteFailureRestoresOldCounts(t *testing.T) {
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
	oldSite := s.metadb.SiteByLabel(alice, dep.Label)
	if oldSite == nil {
		t.Fatal("missing catalog row after first deploy")
	}
	oldFiles, oldBytes := oldSite.Files, oldSite.Bytes
	indexPath := filepath.Join(sites.SiteDir(s.cfg.DataDir, dep.Label), "index.html")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}

	orig := s.metadb
	s.metadb = &nukeStagingStore{Storage: orig, dataDir: s.cfg.DataDir}
	rec2 := deploy(t, s, alice, "blog", tarGz(t, map[string]string{
		"index.html": "<h1>v2 with much longer body</h1>",
		"app.js":     "console.log(2)",
	}))
	s.metadb = orig
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("failed redeploy = %d, want 500: %s", rec2.Code, rec2.Body.String())
	}

	got := s.metadb.SiteByLabel(alice, dep.Label)
	if got == nil {
		t.Fatal("catalog row missing after failed redeploy")
	}
	if got.Files != oldFiles || got.Bytes != oldBytes {
		t.Fatalf("catalog after failed redeploy = files=%d bytes=%d, want files=%d bytes=%d",
			got.Files, got.Bytes, oldFiles, oldBytes)
	}

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("live file missing after failed redeploy: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("live file changed after failed redeploy: was %q, now %q", string(before), string(after))
	}

	rec3 := doReq(s, http.MethodGet, dep.Label+"."+testDomain, "/", alice, nil, "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("preview after failed redeploy = %d: %s", rec3.Code, rec3.Body.String())
	}
	if !strings.Contains(rec3.Body.String(), "v1") {
		t.Fatalf("preview body = %q, want v1", rec3.Body.String())
	}
	if strings.Contains(rec3.Body.String(), "v2") {
		t.Fatalf("preview serves v2 after failed redeploy: %q", rec3.Body.String())
	}
}

func TestReplaceSiteFailureCleansNewPlaceholder(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	_, aliceID, _ := auth.DevTokenFor("alice@example.com")

	orig := s.metadb
	s.metadb = &nukeStagingStore{Storage: orig, dataDir: s.cfg.DataDir}
	rec := deploy(t, s, alice, "fresh", tarGz(t, map[string]string{"index.html": "<h1>new</h1>"}))
	s.metadb = orig
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed new deploy = %d, want 500: %s", rec.Code, rec.Body.String())
	}

	for _, site := range s.metadb.ListSites(alice, aliceID) {
		if site.Project == "fresh" {
			t.Fatalf("placeholder row remains after failed new deploy: %+v", site)
		}
	}
	wantLabel := sites.LabelFor(auth.UserPart(aliceID), "fresh")
	if got := s.metadb.SiteByLabel(alice, wantLabel); got != nil {
		t.Fatalf("placeholder label %q remains: %+v", wantLabel, got)
	}
	if _, err := os.Stat(sites.SiteDir(s.cfg.DataDir, wantLabel)); !os.IsNotExist(err) {
		t.Fatalf("dest dir exists after failed new deploy: %v", err)
	}
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

// ownerFlipStore simulates the R2 label race at the deploy layer: the two
// Upserts succeed, but by the time handleDeploy re-reads before ReplaceSite
// the label is owned by someone else. The deploy must abort with 409 and
// leave the live files untouched (staging removed, dest never touched).
type ownerFlipStore struct {
	store.Storage
	upserts int
}

func (f *ownerFlipStore) UpsertSite(token string, site *store.Site) error {
	f.upserts++
	return f.Storage.UpsertSite(token, site)
}

func (f *ownerFlipStore) SiteByLabel(token, label string) *store.Site {
	s := f.Storage.SiteByLabel(token, label)
	if s == nil {
		return nil
	}
	if f.upserts >= 2 {
		cp := *s
		cp.UserID = "other-user"
		return &cp
	}
	return s
}

func TestDeployAbortsWhenOwnerChangesBeforeReplace(t *testing.T) {
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
	indexPath := filepath.Join(sites.SiteDir(s.cfg.DataDir, dep.Label), "index.html")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}

	orig := s.metadb
	flip := &ownerFlipStore{Storage: orig}
	s.metadb = flip
	rec2 := deploy(t, s, alice, "blog", tarGz(t, map[string]string{"index.html": "<h1>v2</h1>"}))
	s.metadb = orig
	if rec2.Code != http.StatusConflict {
		t.Fatalf("ownership-changed deploy = %d, want 409: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "label is taken") {
		t.Fatalf("ownership-changed body = %q, want label is taken", rec2.Body.String())
	}

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("live file missing after aborted deploy: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("live file changed after ownership abort: was %q, now %q", string(before), string(after))
	}
	if strings.Contains(string(after), "v2") {
		t.Fatalf("live file contains v2 after ownership abort: %q", string(after))
	}
}
