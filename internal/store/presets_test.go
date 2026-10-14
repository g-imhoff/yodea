package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func preset(user, label, name, data string) Preset {
	return Preset{UserID: user, Label: label, Name: name, Data: json.RawMessage(data)}
}

func mustPut(t *testing.T, s Storage, p Preset) Preset {
	t.Helper()
	got, err := s.PutPreset("viewer-jwt", p)
	if err != nil {
		t.Fatalf("PutPreset(%s/%s/%s) = %v", p.UserID, p.Label, p.Name, err)
	}
	return got
}

func mustList(t *testing.T, s Storage, user, label string) []Preset {
	t.Helper()
	got, err := s.ListPresets("viewer-jwt", user, label)
	if err != nil {
		t.Fatalf("ListPresets(%s,%s) = %v", user, label, err)
	}
	if got == nil {
		t.Fatalf("ListPresets(%s,%s) = nil, want non-nil slice", user, label)
	}
	return got
}

func names(list []Preset) string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.Name)
	}
	return strings.Join(out, ",")
}

func TestLocalPresetRoundTrip(t *testing.T) {
	s := openTest(t)
	if got := mustList(t, s, "alice", "lbl"); len(got) != 0 {
		t.Fatalf("fresh list = %+v", got)
	}
	before := time.Now().UTC()
	p := mustPut(t, s, preset("alice", "lbl", "warm", `{"a": 1, "big": 12345678901234567890, "s": "<&>"}`))
	if p.UserID != "alice" || p.Label != "lbl" || p.Name != "warm" || p.UpdatedAt.Before(before) {
		t.Fatalf("put = %+v", p)
	}
	if string(p.Data) != `{"a":1,"big":12345678901234567890,"s":"<&>"}` {
		t.Fatalf("data = %s, want the same object", p.Data)
	}
	p.Data[1] = 'X'
	list := mustList(t, s, "alice", "lbl")
	if len(list) != 1 || string(list[0].Data) != `{"a":1,"big":12345678901234567890,"s":"<&>"}` || !list[0].UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("list = %+v (returned data must not alias stored state)", list)
	}
	list[0].Data[1] = 'Y'
	if again := mustList(t, s, "alice", "lbl"); string(again[0].Data) != `{"a":1,"big":12345678901234567890,"s":"<&>"}` {
		t.Fatalf("list aliases stored state: %s", again[0].Data)
	}

	time.Sleep(2 * time.Millisecond)
	u := mustPut(t, s, preset("alice", "lbl", "warm", `{"b":2}`))
	if !u.UpdatedAt.After(p.UpdatedAt) {
		t.Fatalf("updated_at %v not after %v", u.UpdatedAt, p.UpdatedAt)
	}
	if list := mustList(t, s, "alice", "lbl"); len(list) != 1 || string(list[0].Data) != `{"b":2}` {
		t.Fatalf("after update = %+v", list)
	}
	if ok, err := s.DeletePreset("", "alice", "lbl", "warm"); err != nil || !ok {
		t.Fatalf("delete = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, err := s.DeletePreset("", "alice", "lbl", "warm"); err != nil || ok {
		t.Fatalf("second delete = (%v,%v), want (false,nil)", ok, err)
	}
	if list := mustList(t, s, "alice", "lbl"); len(list) != 0 {
		t.Fatalf("after delete = %+v", list)
	}
}

func TestLocalPresetValidation(t *testing.T) {
	s := openTest(t)
	for _, p := range []Preset{
		preset("", "lbl", "n", `{}`),
		preset("alice", "", "n", `{}`),
		preset("alice", "lbl", "", `{}`),
		preset("alice", "lbl", "n", `{`),
		{UserID: "alice", Label: "lbl", Name: "n"},
	} {
		if _, err := s.PutPreset("", p); err == nil {
			t.Fatalf("PutPreset(%+v) = nil, want error", p)
		}
	}
	if list := mustList(t, s, "alice", "lbl"); len(list) != 0 {
		t.Fatalf("invalid puts stored %+v", list)
	}
}

func TestLocalPresetLimit(t *testing.T) {
	s := openTest(t)
	for i := 0; i < PresetLimit; i++ {
		mustPut(t, s, preset("bob", "lbl", fmt.Sprintf("p%03d", i), `{}`))
	}
	if _, err := s.PutPreset("", preset("bob", "lbl", "extra", `{}`)); !errors.Is(err, ErrPresetLimit) {
		t.Fatalf("101st preset = %v, want ErrPresetLimit", err)
	}
	if n := len(mustList(t, s, "bob", "lbl")); n != PresetLimit {
		t.Fatalf("count after refused create = %d", n)
	}
	mustPut(t, s, preset("bob", "lbl", "p042", `{"updated":true}`))
	mustPut(t, s, preset("bob", "other", "extra", `{}`))
	mustPut(t, s, preset("carol", "lbl", "extra", `{}`))
	if ok, err := s.DeletePreset("", "bob", "lbl", "p000"); err != nil || !ok {
		t.Fatalf("delete = (%v,%v)", ok, err)
	}
	mustPut(t, s, preset("bob", "lbl", "extra", `{}`))
	if _, err := s.PutPreset("", preset("bob", "lbl", "extra-2", `{}`)); !errors.Is(err, ErrPresetLimit) {
		t.Fatalf("refill past limit = %v, want ErrPresetLimit", err)
	}
}

func TestLocalPresetIsolation(t *testing.T) {
	s := openTest(t)
	mustPut(t, s, preset("alice", "lbl-a", "same", `{"who":"alice-a"}`))
	mustPut(t, s, preset("alice", "lbl-b", "same", `{"who":"alice-b"}`))
	mustPut(t, s, preset("bob", "lbl-a", "same", `{"who":"bob-a"}`))
	for _, tc := range []struct{ user, label, want string }{
		{"alice", "lbl-a", `{"who":"alice-a"}`},
		{"alice", "lbl-b", `{"who":"alice-b"}`},
		{"bob", "lbl-a", `{"who":"bob-a"}`},
	} {
		list := mustList(t, s, tc.user, tc.label)
		if len(list) != 1 || string(list[0].Data) != tc.want || list[0].UserID != tc.user || list[0].Label != tc.label {
			t.Fatalf("%s/%s = %+v, want only %s", tc.user, tc.label, list, tc.want)
		}
	}
	if list := mustList(t, s, "bob", "lbl-b"); len(list) != 0 {
		t.Fatalf("bob/lbl-b = %+v, want empty", list)
	}
	if ok, _ := s.DeletePreset("", "bob", "lbl-b", "same"); ok {
		t.Fatal("bob deleted a preset he does not own on lbl-b")
	}
	if ok, err := s.DeletePreset("", "bob", "lbl-a", "same"); err != nil || !ok {
		t.Fatalf("bob delete own = (%v,%v)", ok, err)
	}
	if list := mustList(t, s, "alice", "lbl-a"); len(list) != 1 {
		t.Fatalf("alice lost lbl-a after bob's delete: %+v", list)
	}
}

func TestLocalDBWithoutPresetsKeyLoads(t *testing.T) {
	dir := t.TempDir()
	legacy := `{
  "sites": {"alice-blog": {"user_id": "alice", "project": "blog", "label": "alice-blog", "updated_at": "2026-01-02T03:04:05Z", "files": 1, "bytes": 2}},
  "views": {"bob": [{"label": "alice-blog", "at": "2026-01-02T03:04:05Z"}]},
  "favorites": {"bob": [{"user_id": "bob", "label": "alice-blog", "created_at": "2026-01-02T03:04:05Z"}]}
}`
	if err := os.WriteFile(filepath.Join(dir, "db.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open legacy db.json = %v", err)
	}
	defer s.Close()
	if s.SiteByLabel("", "alice-blog") == nil || len(s.ListFavorites("", "bob")) != 1 || len(s.RecentViews("", "bob", ViewReturn)) != 1 {
		t.Fatal("legacy rows missing after Open")
	}
	if list := mustList(t, s, "bob", "alice-blog"); len(list) != 0 {
		t.Fatalf("legacy presets = %+v, want empty", list)
	}
	mustPut(t, s, preset("bob", "alice-blog", "first", `{"x":1}`))
	buf, err := os.ReadFile(filepath.Join(dir, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != legacy {
		t.Fatalf("preset save rewrote db.json: %s", buf)
	}
	pbuf, err := os.ReadFile(filepath.Join(dir, "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(pbuf, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || raw["presets"] == nil {
		t.Fatalf("presets.json = %s, want only a presets key", pbuf)
	}
	if err := s.RecordView("", "bob", "alice-blog"); err != nil {
		t.Fatal(err)
	}
	buf, err = os.ReadFile(filepath.Join(dir, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = nil
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 || raw["sites"] == nil || raw["views"] == nil || raw["favorites"] == nil {
		t.Fatalf("db.json keys after save = %s, want sites, views, favorites only", buf)
	}
	if s.SiteByLabel("", "alice-blog") == nil || len(s.ListFavorites("", "bob")) != 1 {
		t.Fatal("legacy rows lost after preset save")
	}
}

func TestLocalPresetsSurviveOlderBinaryRewritingDB(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, preset("bob", "alice-blog", "keep", `{"k":1}`))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	older := `{"sites":{"alice-blog":{"user_id":"alice","project":"blog","label":"alice-blog","updated_at":"2026-01-02T03:04:05Z","files":1,"bytes":2}},"views":{"carol":[{"label":"alice-blog","at":"2026-01-02T03:04:05Z"}]},"favorites":{}}`
	if err := os.Rename(filepath.Join(dir, "db.json"), filepath.Join(dir, "db.json.bak")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.json"), []byte(older), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if list := mustList(t, s2, "bob", "alice-blog"); len(list) != 1 || list[0].Name != "keep" || string(list[0].Data) != `{"k":1}` {
		t.Fatalf("presets after an older binary rewrote db.json = %+v", list)
	}
	if len(s2.RecentViews("", "carol", ViewReturn)) != 1 {
		t.Fatal("older binary's db.json rows not loaded")
	}
}

func TestLocalDeepPresetStaysBodySizedOnDisk(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertSite("", &Site{UserID: "alice", Project: "blog", Label: "alice-blog"}); err != nil {
		t.Fatal(err)
	}
	dbBefore, err := os.ReadFile(filepath.Join(dir, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	const depth = 8180
	body := `{"a":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `,"s":"<&>\u2028"}`
	for _, name := range []string{"d1", "d2", "d3"} {
		mustPut(t, s, preset("bob", "alice-blog", name, body))
	}
	if err := s.RecordView("", "bob", "alice-blog"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"presets.json", "presets.json.bak"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if limit := int64(3*len(body) + 1024); st.Size() > limit {
			t.Fatalf("%s = %d bytes after three %d-byte presets, want at most %d", f, st.Size(), len(body), limit)
		}
	}
	for _, f := range []string{"db.json", "db.json.bak"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if limit := int64(len(dbBefore) + 1024); st.Size() > limit {
			t.Fatalf("%s = %d bytes, want preset data kept out of it (at most %d)", f, st.Size(), limit)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list := mustList(t, s2, "bob", "alice-blog")
	if len(list) != 3 {
		t.Fatalf("reopened %d deep presets, want 3", len(list))
	}
	for _, p := range list {
		if string(p.Data) != body {
			t.Fatalf("reopened %s data differs from the stored body (len %d vs %d)", p.Name, len(p.Data), len(body))
		}
	}
}

func TestLocalPresetsCorruptPrimaryUsesBackup(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, preset("alice", "lbl", "first", `{"v":1}`))
	mustPut(t, s, preset("alice", "lbl", "second", `{"v":2}`))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "presets.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open with corrupt presets.json = %v, want backup fallback", err)
	}
	if got := names(mustList(t, s2, "alice", "lbl")); got != "first" {
		t.Fatalf("names from backup = %q, want first", got)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "presets.json.bak"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open with corrupt presets.json and backup succeeded, want error")
	}
}

func TestLocalPresetWriteErrorsPropagate(t *testing.T) {
	s := openTest(t)
	mustPut(t, s, preset("alice", "lbl", "keep", `{}`))
	badDir := filepath.Join(t.TempDir(), "no-such-dir")
	s.presetsPath = filepath.Join(badDir, "presets.json")
	s.presetsBakPath = filepath.Join(badDir, "presets.json.bak")
	s.dir = badDir
	if _, err := s.PutPreset("", preset("alice", "lbl", "new", `{}`)); err == nil {
		t.Fatal("PutPreset with unwritable dir = nil, want error")
	}
	if _, err := s.PutPreset("", preset("alice", "lbl", "keep", `{"v":2}`)); err == nil {
		t.Fatal("PutPreset update with unwritable dir = nil, want error")
	}
	if _, err := s.DeletePreset("", "alice", "lbl", "keep"); err == nil {
		t.Fatal("DeletePreset with unwritable dir = nil, want error")
	}
}

func TestLocalPresetsPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := mustPut(t, s, preset("alice", "lbl", "b name", `{"nested":{"arr":[1,2.5,"x"]},"big":12345678901234567890}`))
	mustPut(t, s, preset("alice", "lbl", "a.name", `{}`))
	mustPut(t, s, preset("bob", "lbl", "bob's", `{"k":"v"}`))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list := mustList(t, s2, "alice", "lbl")
	if names(list) != "a.name,b name" {
		t.Fatalf("reopened names = %q", names(list))
	}
	if string(list[1].Data) != `{"nested":{"arr":[1,2.5,"x"]},"big":12345678901234567890}` || !list[1].UpdatedAt.Equal(a.UpdatedAt) {
		t.Fatalf("reopened preset = %s @ %v, want %s @ %v", list[1].Data, list[1].UpdatedAt, a.Data, a.UpdatedAt)
	}
	if list := mustList(t, s2, "bob", "lbl"); len(list) != 1 || string(list[0].Data) != `{"k":"v"}` {
		t.Fatalf("bob after reopen = %+v", list)
	}
}

func TestLocalPresetReloadsBeforeMutate(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustPut(t, s, preset("alice", "lbl", "mine", `{}`))
	path := filepath.Join(dir, "presets.json")
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pd presetData
	if err := json.Unmarshal(buf, &pd); err != nil {
		t.Fatal(err)
	}
	pd.Presets["alice"] = append(pd.Presets["alice"], preset("alice", "lbl", "external", `{"e":1}`))
	nbuf, _ := json.Marshal(pd)
	if err := os.WriteFile(path, nbuf, 0o600); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, preset("alice", "lbl", "next", `{}`))
	if got := names(mustList(t, s, "alice", "lbl")); got != "external,mine,next" {
		t.Fatalf("names = %q, want the external write kept", got)
	}
}

func TestLocalPresetsOnlyBackupUsedForEmptyPrimary(t *testing.T) {
	dir := t.TempDir()
	bak := `{"presets":{"alice":[{"user_id":"alice","label":"lbl","name":"saved","data":{"v":1},"updated_at":"2026-01-02T03:04:05Z"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "presets.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "presets.json.bak"), []byte(bak), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if list := mustList(t, s, "alice", "lbl"); len(list) != 1 || list[0].Name != "saved" || string(list[0].Data) != `{"v":1}` {
		t.Fatalf("presets from backup = %+v", list)
	}
}
