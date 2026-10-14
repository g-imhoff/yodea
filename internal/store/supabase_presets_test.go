package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakePresetREST struct {
	t            *testing.T
	mu           sync.Mutex
	rows         []Preset
	requests     int
	posts        int
	fail         bool
	jsonbSpacing bool
	listSelects  []string
}

func jsonbText(raw json.RawMessage) json.RawMessage {
	out := make([]byte, 0, len(raw)*3/2)
	inString, escaped := false, false
	for _, c := range raw {
		out = append(out, c)
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && (c == ',' || c == ':'):
			out = append(out, ' ')
		}
	}
	return out
}

var presetColumns = []string{"user_id", "label", "name", "data", "updated_at"}

func (f *fakePresetREST) project(rows []Preset, sel string) ([]byte, error) {
	cols := presetColumns
	if sel != "" {
		cols = strings.Split(sel, ",")
		for _, c := range cols {
			if !slices.Contains(presetColumns, c) {
				return nil, fmt.Errorf("unknown column %q", c)
			}
		}
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, p := range rows {
		buf, _ := json.Marshal(p)
		var all map[string]json.RawMessage
		_ = json.Unmarshal(buf, &all)
		if f.jsonbSpacing {
			all["data"] = jsonbText(all["data"])
		}
		if i > 0 {
			b.WriteString(", \n ")
		}
		b.WriteByte('{')
		for j, c := range cols {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%q:", c)
			b.Write(all[c])
		}
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

func (f *fakePresetREST) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.fail {
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		return
	}
	if r.URL.Path != "/rest/v1/presets" {
		f.t.Errorf("unexpected path %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("apikey") != "server-key" || r.Header.Get("Authorization") != "Bearer viewer-jwt" {
		f.t.Errorf("bad credentials apikey=%q auth=%q", r.Header.Get("apikey"), r.Header.Get("Authorization"))
	}
	q := r.URL.Query()
	match := func(p Preset) bool {
		for col, val := range map[string]string{"user_id": p.UserID, "label": p.Label, "name": p.Name} {
			if want, ok := q[col]; ok && want[0] != "eq."+val {
				return false
			}
		}
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		if q.Get("user_id") == "" || q.Get("label") == "" {
			f.t.Errorf("GET without user_id/label scope: %s", r.URL.RawQuery)
		}
		out := []Preset{}
		for _, p := range f.rows {
			if match(p) {
				out = append(out, p)
			}
		}
		if q.Get("order") == "name.asc" {
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		}
		if l := q.Get("limit"); l != "" {
			n, _ := strconv.Atoi(l)
			if len(out) > n {
				out = out[:n]
			}
		}
		if q.Get("order") == "name.asc" {
			f.listSelects = append(f.listSelects, q.Get("select"))
		}
		body, err := f.project(out, q.Get("select"))
		if err != nil {
			f.t.Errorf("GET %s: %v", r.URL.RawQuery, err)
			http.Error(w, `{"message":"bad select"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write(body)
	case http.MethodPost:
		f.posts++
		if q.Get("on_conflict") != "user_id,label,name" {
			f.t.Errorf("on_conflict = %q", q.Get("on_conflict"))
		}
		prefer := r.Header.Get("Prefer")
		if !strings.Contains(prefer, "resolution=merge-duplicates") || !strings.Contains(prefer, "return=representation") {
			f.t.Errorf("Prefer = %q", prefer)
		}
		buf, _ := io.ReadAll(r.Body)
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(buf, &raw); err != nil || len(raw["data"]) == 0 || raw["data"][0] != '{' {
			f.t.Errorf("POST body data must be a JSON object, got %s", buf)
		}
		var p Preset
		if err := json.Unmarshal(buf, &p); err != nil {
			f.t.Errorf("POST body %s: %v", buf, err)
		}
		if p.UpdatedAt.IsZero() {
			f.t.Errorf("POST body without updated_at: %s", buf)
		}
		replaced := false
		for i, cur := range f.rows {
			if cur.UserID == p.UserID && cur.Label == p.Label && cur.Name == p.Name {
				f.rows[i] = p
				replaced = true
			}
		}
		if !replaced {
			f.rows = append(f.rows, p)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode([]Preset{p})
	case http.MethodDelete:
		if r.Header.Get("Prefer") != "return=representation" {
			f.t.Errorf("DELETE Prefer = %q", r.Header.Get("Prefer"))
		}
		if q.Get("user_id") == "" || q.Get("label") == "" || q.Get("name") == "" {
			f.t.Errorf("DELETE without full key: %s", r.URL.RawQuery)
		}
		removed := []Preset{}
		kept := f.rows[:0]
		for _, p := range f.rows {
			if match(p) {
				removed = append(removed, p)
			} else {
				kept = append(kept, p)
			}
		}
		f.rows = kept
		_ = json.NewEncoder(w).Encode(removed)
	default:
		f.t.Errorf("unexpected %s", r.Method)
	}
}

func newFakePresetStore(t *testing.T) (*SupabaseStore, *fakePresetREST) {
	t.Helper()
	fake := &fakePresetREST{t: t}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return NewSupabaseStore(srv.URL, "server-key"), fake
}

func TestSupabasePresetRoundTrip(t *testing.T) {
	s, fake := newFakePresetStore(t)
	if got := mustList(t, s, "alice", "lbl"); len(got) != 0 {
		t.Fatalf("fresh list = %+v", got)
	}
	tricky := "v1.2, (x) & y=z"
	p := mustPut(t, s, preset("alice", "lbl", tricky, `{"a":1,"big":12345678901234567890}`))
	if p.Name != tricky || string(p.Data) != `{"a":1,"big":12345678901234567890}` || p.UpdatedAt.IsZero() {
		t.Fatalf("put = %+v", p)
	}
	mustPut(t, s, preset("alice", "lbl", "another", `{}`))
	mustPut(t, s, preset("alice", "other", "elsewhere", `{}`))
	mustPut(t, s, preset("bob", "lbl", "bobs", `{}`))
	if got := names(mustList(t, s, "alice", "lbl")); got != "another,"+tricky {
		t.Fatalf("alice/lbl = %q", got)
	}
	u := mustPut(t, s, preset("alice", "lbl", tricky, `{"b":2}`))
	if string(u.Data) != `{"b":2}` {
		t.Fatalf("update = %+v", u)
	}
	if got := mustList(t, s, "alice", "lbl"); len(got) != 2 {
		t.Fatalf("update created a duplicate: %+v", got)
	}
	if ok, err := s.DeletePreset("viewer-jwt", "alice", "lbl", tricky); err != nil || !ok {
		t.Fatalf("delete = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, err := s.DeletePreset("viewer-jwt", "alice", "lbl", tricky); err != nil || ok {
		t.Fatalf("second delete = (%v,%v), want (false,nil)", ok, err)
	}
	if got := names(mustList(t, s, "alice", "lbl")); got != "another" {
		t.Fatalf("after delete = %q", got)
	}
	if got := names(mustList(t, s, "bob", "lbl")); got != "bobs" {
		t.Fatalf("bob = %q", got)
	}
	if fake.posts != 5 {
		t.Fatalf("posts = %d, want 5", fake.posts)
	}
}

func TestSupabasePresetLimitChecksBeforeInsert(t *testing.T) {
	s, fake := newFakePresetStore(t)
	for i := 0; i < PresetLimit; i++ {
		fake.rows = append(fake.rows, preset("bob", "lbl", fmt.Sprintf("p%03d", i), `{}`))
	}
	if _, err := s.PutPreset("viewer-jwt", preset("bob", "lbl", "extra", `{}`)); !errors.Is(err, ErrPresetLimit) {
		t.Fatalf("101st = %v, want ErrPresetLimit", err)
	}
	if fake.posts != 0 {
		t.Fatalf("limit hit still POSTed %d times", fake.posts)
	}
	mustPut(t, s, preset("bob", "lbl", "p099", `{"updated":true}`))
	mustPut(t, s, preset("bob", "other", "extra", `{}`))
	mustPut(t, s, preset("carol", "lbl", "extra", `{}`))
	if fake.posts != 3 {
		t.Fatalf("posts = %d, want 3", fake.posts)
	}
}

func TestSupabasePresetsRequireCallerToken(t *testing.T) {
	s, fake := newFakePresetStore(t)
	for _, token := range []string{"", SystemToken} {
		if _, err := s.ListPresets(token, "alice", "lbl"); err == nil {
			t.Fatalf("ListPresets(%q) = nil error", token)
		}
		if _, err := s.PutPreset(token, preset("alice", "lbl", "n", `{}`)); err == nil {
			t.Fatalf("PutPreset(%q) = nil error", token)
		}
		if _, err := s.DeletePreset(token, "alice", "lbl", "n"); err == nil {
			t.Fatalf("DeletePreset(%q) = nil error", token)
		}
	}
	if _, err := s.PutPreset("viewer-jwt", preset("alice", "lbl", "n", `{`)); err == nil {
		t.Fatal("PutPreset with invalid data = nil error")
	}
	if fake.requests != 0 {
		t.Fatalf("rejected calls made %d requests", fake.requests)
	}
}

func TestSupabasePresetErrorsSurface(t *testing.T) {
	s, fake := newFakePresetStore(t)
	fake.fail = true
	if _, err := s.ListPresets("viewer-jwt", "alice", "lbl"); err == nil {
		t.Fatal("ListPresets on 500 = nil error")
	}
	if _, err := s.PutPreset("viewer-jwt", preset("alice", "lbl", "n", `{}`)); err == nil {
		t.Fatal("PutPreset on 500 = nil error")
	}
	if _, err := s.DeletePreset("viewer-jwt", "alice", "lbl", "n"); err == nil {
		t.Fatal("DeletePreset on 500 = nil error")
	}
}

func maxPresetBody(t *testing.T, i int) string {
	t.Helper()
	var body string
	if i%2 == 0 {
		body = `{"v":"` + strings.Repeat("x", PresetDataMax-8) + `"}`
	} else {
		body = `{"vv":[0` + strings.Repeat(",0", (PresetDataMax-10)/2) + `]}`
	}
	if len(body) != PresetDataMax || !json.Valid([]byte(body)) {
		t.Fatalf("body %d is %d bytes, valid=%v", i, len(body), json.Valid([]byte(body)))
	}
	return body
}

func TestSupabaseListPresetsAtLimitWithMaxBodies(t *testing.T) {
	s, fake := newFakePresetStore(t)
	fake.jsonbSpacing = true
	bodies := map[string]string{}
	for i := 0; i < PresetLimit; i++ {
		name := fmt.Sprintf("p%03d", i)
		bodies[name] = maxPresetBody(t, i)
		mustPut(t, s, preset("bob", "lbl", name, bodies[name]))
	}
	if _, err := s.PutPreset("viewer-jwt", preset("bob", "lbl", "extra", `{}`)); !errors.Is(err, ErrPresetLimit) {
		t.Fatalf("101st = %v, want ErrPresetLimit", err)
	}
	got, err := s.ListPresets("viewer-jwt", "bob", "lbl")
	if err != nil {
		t.Fatalf("ListPresets with %d x %d-byte presets = %v", PresetLimit, PresetDataMax, err)
	}
	if len(got) != PresetLimit {
		t.Fatalf("listed %d presets, want %d", len(got), PresetLimit)
	}
	total := 0
	for _, p := range got {
		total += len(p.Data)
		var compact bytes.Buffer
		if err := json.Compact(&compact, p.Data); err != nil || compact.String() != bodies[p.Name] {
			t.Fatalf("preset %q data mismatch (err=%v)", p.Name, err)
		}
		if p.UpdatedAt.IsZero() || p.UserID != "bob" || p.Label != "lbl" {
			t.Fatalf("preset %q = user %q label %q updated_at %v", p.Name, p.UserID, p.Label, p.UpdatedAt)
		}
	}
	if total <= 1<<20 {
		t.Fatalf("listed data is only %d bytes; the test no longer exercises a large list", total)
	}
	if len(fake.listSelects) == 0 {
		t.Fatal("no list GET recorded")
	}
	for _, sel := range fake.listSelects {
		if sel != "name,data,updated_at" {
			t.Fatalf("list GET select = %q, want name,data,updated_at", sel)
		}
	}
}

func TestSupabaseListPresetsOversizeIsExplicitError(t *testing.T) {
	s, fake := newFakePresetStore(t)
	fake.jsonbSpacing = true
	for i := 0; i < 2*PresetLimit; i++ {
		fake.rows = append(fake.rows, preset("bob", "lbl", fmt.Sprintf("p%03d", i), maxPresetBody(t, 1)))
	}
	got, err := s.ListPresets("viewer-jwt", "bob", "lbl")
	if err == nil {
		t.Fatalf("ListPresets over the response cap = %d presets, nil error", len(got))
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ListPresets over the response cap = %v, want an explicit size error", err)
	}
}
