package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/g-imhoff/yodea/internal/store"
)

func yodeaDo(t *testing.T, s *Server, method, host, path, token, body string, kv ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = host
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Set(kv[i], kv[i+1])
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	assertPresetAPIHeaders(t, method+" "+host+path, rec)
	return rec
}

func assertPresetAPIHeaders(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if got := h.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("%s: Cache-Control = %q, want no-store", what, got)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("%s: X-Content-Type-Options = %q, want nosniff", what, got)
	}
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Fatalf("%s: CORS header %s: %q, want none", what, k, h.Values(k))
		}
	}
	if h.Get("Location") != "" || h.Get("Set-Cookie") != "" {
		t.Fatalf("%s: Location %q Set-Cookie %q, want neither", what, h.Get("Location"), h.Get("Set-Cookie"))
	}
	if rec.Code == http.StatusNoContent {
		if rec.Body.Len() != 0 {
			t.Fatalf("%s: 204 body = %q, want empty", what, rec.Body.String())
		}
		return
	}
	if ct := h.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s: Content-Type = %q, want application/json (status %d body %q)", what, ct, rec.Code, rec.Body.String())
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("%s: body is not JSON: %q", what, rec.Body.String())
	}
}

func presetPath(name string) string { return "/_yodea/presets/" + url.PathEscape(name) }

func sameOriginHeaders(host string) []string {
	return []string{"Content-Type", "application/json", "Origin", "https://" + host, "Sec-Fetch-Site", "same-origin"}
}

func putPresetReq(t *testing.T, s *Server, host, token, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	return yodeaDo(t, s, http.MethodPut, host, presetPath(name), token, body, sameOriginHeaders(host)...)
}

func deletePresetReq(t *testing.T, s *Server, host, token, name string) *httptest.ResponseRecorder {
	t.Helper()
	return yodeaDo(t, s, http.MethodDelete, host, presetPath(name), token, "", "Origin", "https://"+host)
}

type presetJSON struct {
	Name      string          `json:"name"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt string          `json:"updated_at"`
}

func listPresetsReq(t *testing.T, s *Server, host, token string) []presetJSON {
	t.Helper()
	rec := yodeaDo(t, s, http.MethodGet, host, "/_yodea/presets", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET presets = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["presets"] == nil || string(body["presets"]) == "null" {
		t.Fatalf("GET presets body = %s, want exactly {\"presets\":[...]}", rec.Body.String())
	}
	var list []presetJSON
	if err := json.Unmarshal(body["presets"], &list); err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		checkPresetShape(t, p)
	}
	return list
}

func checkPresetShape(t *testing.T, p presetJSON) {
	t.Helper()
	if _, err := time.Parse(time.RFC3339, p.UpdatedAt); err != nil {
		t.Fatalf("updated_at %q is not RFC3339: %v", p.UpdatedAt, err)
	}
	if len(p.Data) == 0 || p.Data[0] != '{' {
		t.Fatalf("data = %s, want a JSON object", p.Data)
	}
}

func presetNames(list []presetJSON) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.Name)
	}
	return out
}

func decodeJSONValue(t *testing.T, raw string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return v
}

func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	return reflect.DeepEqual(decodeJSONValue(t, got), decodeJSONValue(t, want))
}

func decodePutPreset(t *testing.T, rec *httptest.ResponseRecorder) presetJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT preset = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("PUT body %q: %v", rec.Body.String(), err)
	}
	inner, ok := body["preset"]
	if len(body) != 1 || !ok || len(inner) != 3 || inner["name"] == nil || inner["data"] == nil || inner["updated_at"] == nil {
		t.Fatalf("PUT body = %s, want exactly {\"preset\":{name,data,updated_at}}", rec.Body.String())
	}
	var p presetJSON
	if err := json.Unmarshal(body["preset"]["name"], &p.Name); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body["preset"]["updated_at"], &p.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	p.Data = body["preset"]["data"]
	checkPresetShape(t, p)
	return p
}

type presetFixture struct {
	s             *Server
	alice, bob    string
	host, label   string
	host2, label2 string
}

func newPresetFixture(t *testing.T) presetFixture {
	t.Helper()
	s := newTestServer(t)
	f := presetFixture{s: s, alice: devToken(t, "alice@example.com"), bob: devToken(t, "bob@example.com")}
	f.label = deployLabel(t, s, f.alice, "blog")
	f.host = f.label + "." + testDomain
	f.label2 = deployLabel(t, s, f.alice, "shop")
	f.host2 = f.label2 + "." + testDomain
	return f
}

func TestPresetsRequireLoginJSON(t *testing.T) {
	f := newPresetFixture(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/_yodea/presets"},
		{http.MethodPut, "/_yodea/presets/warm"},
		{http.MethodDelete, "/_yodea/presets/warm"},
		{http.MethodPost, "/_yodea/presets"},
		{http.MethodOptions, "/_yodea/presets/warm"},
		{http.MethodGet, "/_yodea"},
		{http.MethodGet, "/_yodea/"},
		{http.MethodGet, "/_yodea/unknown"},
		{http.MethodGet, "/_yodea/presets/a%2Fb"},
	} {
		for _, token := range []string{"", "not-a-dev-token"} {
			rec := yodeaDo(t, f.s, tc.method, f.host, tc.path, token, `{"a":1}`, sameOriginHeaders(f.host)...)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s token %q = %d (%s), want 401", tc.method, tc.path, token, rec.Code, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); !sameJSON(t, got, `{"error":"login required","code":"login_required"}`) {
				t.Fatalf("%s %s body = %s, want login_required JSON", tc.method, tc.path, got)
			}
		}
	}
	if rec := doReq(f.s, http.MethodGet, f.host, "/", "", nil, ""); rec.Code != http.StatusFound {
		t.Fatalf("anon preview page = %d, want 302 login redirect", rec.Code)
	}
}

func TestPresetsUnknownSite(t *testing.T) {
	f := newPresetFixture(t)
	for _, host := range []string{"missing." + testDomain, "-bad-." + testDomain} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			path := "/_yodea/presets"
			if method != http.MethodGet {
				path = presetPath("warm")
			}
			rec := yodeaDo(t, f.s, method, host, path, f.bob, `{"a":1}`, sameOriginHeaders(host)...)
			assertCode(t, rec.Code, rec.Body.String(), "unknown_site", http.StatusNotFound)
		}
	}
}

func TestPresetsCRUD(t *testing.T) {
	f := newPresetFixture(t)
	rec := yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/presets", f.bob, "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"presets":[]}` {
		t.Fatalf("empty list = %d %q, want 200 {\"presets\":[]}", rec.Code, rec.Body.String())
	}

	v1 := `{"hue": 12.5, "stops": [{"at": 0, "c": "#fff"}, {"at": 1, "c": "<b>&"}],
		"big": 12345678901234567890, "nested": {"ok": true, "none": null}, "s": "café"}`
	created := decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "Warm sunset", v1))
	if created.Name != "Warm sunset" || !sameJSON(t, string(created.Data), v1) {
		t.Fatalf("created = %+v, want name Warm sunset with data %s", created, v1)
	}
	if !strings.Contains(string(created.Data), "12345678901234567890") {
		t.Fatalf("big number not kept verbatim: %s", created.Data)
	}
	list := listPresetsReq(t, f.s, f.host, f.bob)
	if len(list) != 1 || list[0].Name != "Warm sunset" || !sameJSON(t, string(list[0].Data), v1) || list[0].UpdatedAt != created.UpdatedAt {
		t.Fatalf("list after create = %+v", list)
	}

	time.Sleep(5 * time.Millisecond)
	v2 := `{"hue": 200}`
	updated := decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "Warm sunset", v2))
	if !sameJSON(t, string(updated.Data), v2) {
		t.Fatalf("updated data = %s, want %s", updated.Data, v2)
	}
	t1, _ := time.Parse(time.RFC3339, created.UpdatedAt)
	t2, _ := time.Parse(time.RFC3339, updated.UpdatedAt)
	if !t2.After(t1) {
		t.Fatalf("updated_at %s not after %s", updated.UpdatedAt, created.UpdatedAt)
	}
	list = listPresetsReq(t, f.s, f.host, f.bob)
	if len(list) != 1 || !sameJSON(t, string(list[0].Data), v2) {
		t.Fatalf("list after update = %+v, want one preset with %s", list, v2)
	}

	rec = deletePresetReq(t, f.s, f.host, f.bob, "Warm sunset")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %q, want 204", rec.Code, rec.Body.String())
	}
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}
	rec = deletePresetReq(t, f.s, f.host, f.bob, "Warm sunset")
	assertCode(t, rec.Code, rec.Body.String(), "not_found", http.StatusNotFound)
}

func TestPresetsSortedByName(t *testing.T) {
	f := newPresetFixture(t)
	for _, name := range []string{"beta", "Zed", "alpha", "Gamma", "Alpha", "a.b", "10", "2", "_x", "ALPHA"} {
		decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, name, `{}`))
	}
	got := presetNames(listPresetsReq(t, f.s, f.host, f.bob))
	want := []string{"10", "2", "_x", "a.b", "ALPHA", "Alpha", "alpha", "beta", "Gamma", "Zed"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestPresetsLimitPerViewerAndPreview(t *testing.T) {
	f := newPresetFixture(t)
	for i := 0; i < store.PresetLimit; i++ {
		decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, fmt.Sprintf("p%03d", i), `{"i":1}`))
	}
	rec := putPresetReq(t, f.s, f.host, f.bob, "one-too-many", `{}`)
	assertCode(t, rec.Code, rec.Body.String(), "preset_limit", http.StatusConflict)
	if n := len(listPresetsReq(t, f.s, f.host, f.bob)); n != store.PresetLimit {
		t.Fatalf("presets after refused create = %d, want %d", n, store.PresetLimit)
	}
	updated := decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "p050", `{"i":2}`))
	if !sameJSON(t, string(updated.Data), `{"i":2}`) {
		t.Fatalf("update at limit = %s", updated.Data)
	}
	decodePutPreset(t, putPresetReq(t, f.s, f.host2, f.bob, "other-preview", `{}`))
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.alice, "other-viewer", `{}`))
	if rec := deletePresetReq(t, f.s, f.host, f.bob, "p000"); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "after-delete", `{}`))
	rec = putPresetReq(t, f.s, f.host, f.bob, "again-too-many", `{}`)
	assertCode(t, rec.Code, rec.Body.String(), "preset_limit", http.StatusConflict)
}

func TestPresetsBodyLimit(t *testing.T) {
	f := newPresetFixture(t)
	obj := func(size int) string {
		return `{"pad":"` + strings.Repeat("x", size-10) + `"}`
	}
	if len(obj(presetBodyMax)) != 16384 {
		t.Fatalf("fixture size = %d", len(obj(presetBodyMax)))
	}
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "max", obj(16384)))
	rec := putPresetReq(t, f.s, f.host, f.bob, "too-big", obj(16385))
	assertCode(t, rec.Code, rec.Body.String(), "upload_too_large", http.StatusRequestEntityTooLarge)
	rec = putPresetReq(t, f.s, f.host, f.bob, "max", obj(1<<20))
	assertCode(t, rec.Code, rec.Body.String(), "upload_too_large", http.StatusRequestEntityTooLarge)
	list := listPresetsReq(t, f.s, f.host, f.bob)
	if len(list) != 1 || list[0].Name != "max" || !sameJSON(t, string(list[0].Data), obj(16384)) {
		t.Fatalf("list = %d presets, want only the 16384-byte one intact", len(list))
	}
}

func TestPresetsDeepBodyStaysBodySizedOnDisk(t *testing.T) {
	f := newPresetFixture(t)
	depth := (presetBodyMax - len(`{"a":}`)) / 2
	body := `{"a":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `}`
	if len(body) > presetBodyMax || len(body) < presetBodyMax-1 {
		t.Fatalf("fixture size = %d", len(body))
	}
	for _, name := range []string{"d1", "d2"} {
		decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, name, body))
	}
	if rec := doReq(f.s, http.MethodGet, f.host, "/", f.bob, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("preview page = %d", rec.Code)
	}
	for _, file := range []string{"db.json", "db.json.bak", "presets.json", "presets.json.bak"} {
		st, err := os.Stat(filepath.Join(f.s.cfg.DataDir, file))
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > 3*presetBodyMax {
			t.Fatalf("%s = %d bytes after two %d-byte presets", file, st.Size(), len(body))
		}
	}
	list := listPresetsReq(t, f.s, f.host, f.bob)
	if len(list) != 2 || string(list[0].Data) != body || string(list[1].Data) != body {
		t.Fatalf("deep presets not returned verbatim")
	}
}

func TestPresetsBadRequests(t *testing.T) {
	f := newPresetFixture(t)
	origin := "https://" + f.host
	for _, ctype := range []string{"", "text/plain", "application/jsonx", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/json; charset", "text/json"} {
		rec := yodeaDo(t, f.s, http.MethodPut, f.host, presetPath("warm"), f.bob, `{"a":1}`, "Content-Type", ctype, "Origin", origin)
		assertCode(t, rec.Code, rec.Body.String(), "bad_request", http.StatusBadRequest)
	}
	for _, body := range []string{"", " ", "not json", "[1,2]", `"str"`, "42", "null", "true", "{} {}", "{", `{"a":1}x`, `{"a":"` + "\xff" + `"}`, `{"a":1,}`} {
		rec := yodeaDo(t, f.s, http.MethodPut, f.host, presetPath("warm"), f.bob, body, "Content-Type", "application/json", "Origin", origin)
		assertCode(t, rec.Code, rec.Body.String(), "bad_request", http.StatusBadRequest)
	}
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 0 {
		t.Fatalf("bad requests stored %+v", list)
	}
	for _, ctype := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		rec := yodeaDo(t, f.s, http.MethodPut, f.host, presetPath("ok"), f.bob, " {\n\"a\" : [1, 2]\n} ", "Content-Type", ctype, "Origin", origin)
		if p := decodePutPreset(t, rec); !sameJSON(t, string(p.Data), `{"a":[1,2]}`) {
			t.Fatalf("data = %s", p.Data)
		}
	}
}

func TestPresetNameValidation(t *testing.T) {
	f := newPresetFixture(t)
	for _, seg := range []string{
		"a%2Fb", "%2F", "a%2fb", "a%5Cb", "%5C", "a%00b", "a%07", "%7F", "a%C2%85b", "a%C2%9F",
		"%20a", "a%20", "%09a", "a%0A", "%E3%80%80a", "a%C2%A0",
		"%FF", "a%C3", "",
		strings.Repeat("a", 61), strings.Repeat("%C3%A9", 61),
	} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			rec := yodeaDo(t, f.s, method, f.host, "/_yodea/presets/"+seg, f.bob, `{}`, sameOriginHeaders(f.host)...)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s name segment %q = %d (%s), want 400", method, seg, rec.Code, rec.Body.String())
			}
			assertCode(t, rec.Code, rec.Body.String(), "bad_request", http.StatusBadRequest)
		}
	}
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 0 {
		t.Fatalf("invalid names stored: %q", presetNames(list))
	}

	accepted := []string{
		strings.Repeat("a", 60), strings.Repeat("é", 60), strings.Repeat("日", 60),
		"v1.2", "my.preset.json", "a..b", "...", ".hidden", "trailing.", "with space", "café ☕",
		"100%", "a?b#c", "semi;colon", "+plus", "x", "A&B=C",
	}
	for _, name := range accepted {
		p := decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, name, `{}`))
		if p.Name != name {
			t.Fatalf("stored name = %q, want %q", p.Name, name)
		}
	}
	p := decodePutPreset(t, yodeaDo(t, f.s, http.MethodPut, f.host, "/_yodea/presets/v1%2E3", f.bob, `{}`, sameOriginHeaders(f.host)...))
	if p.Name != "v1.3" {
		t.Fatalf("encoded-dot name = %q, want v1.3", p.Name)
	}
	if got := len(listPresetsReq(t, f.s, f.host, f.bob)); got != len(accepted)+1 {
		t.Fatalf("stored %d presets, want %d", got, len(accepted)+1)
	}
	for _, name := range append(accepted, "v1.3") {
		if rec := deletePresetReq(t, f.s, f.host, f.bob, name); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %q = %d (%s), want 204", name, rec.Code, rec.Body.String())
		}
	}
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 0 {
		t.Fatalf("left after deletes: %q", presetNames(list))
	}
}

func TestPresetsWritesRequireSameOrigin(t *testing.T) {
	f := newPresetFixture(t)
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "keep", `{"v":1}`))
	for _, origin := range []string{
		"", "null", "https://" + f.host2, "https://" + testDomain, "https://" + f.host + ".evil.test",
		"https://evil.test", "https://" + f.host + ":8443", "https://x" + f.host, "not a url", "https://",
	} {
		kv := []string{"Content-Type", "application/json", "Sec-Fetch-Site", "same-origin"}
		if origin != "" {
			kv = append(kv, "Origin", origin)
		}
		rec := yodeaDo(t, f.s, http.MethodPut, f.host, presetPath("keep"), f.bob, `{"v":2}`, kv...)
		assertCode(t, rec.Code, rec.Body.String(), "csrf_required", http.StatusForbidden)
		rec = yodeaDo(t, f.s, http.MethodPut, f.host, presetPath("new"), f.bob, `{"v":2}`, kv...)
		assertCode(t, rec.Code, rec.Body.String(), "csrf_required", http.StatusForbidden)
		rec = yodeaDo(t, f.s, http.MethodDelete, f.host, presetPath("keep"), f.bob, "", kv...)
		assertCode(t, rec.Code, rec.Body.String(), "csrf_required", http.StatusForbidden)
	}
	list := listPresetsReq(t, f.s, f.host, f.bob)
	if len(list) != 1 || !sameJSON(t, string(list[0].Data), `{"v":1}`) {
		t.Fatalf("refused writes changed state: %+v", list)
	}

	for _, tc := range []struct{ host, origin string }{
		{f.host, "https://" + f.host},
		{f.host, "http://" + f.host},
		{f.host, "HTTPS://" + strings.ToUpper(f.host)},
		{f.host + ":8097", "http://" + f.host + ":8097"},
	} {
		rec := yodeaDo(t, f.s, http.MethodPut, tc.host, presetPath("keep"), f.bob, `{"v":3}`, "Content-Type", "application/json", "Origin", tc.origin)
		decodePutPreset(t, rec)
	}
	rec := yodeaDo(t, f.s, http.MethodDelete, f.host+":8097", presetPath("keep"), f.bob, "", "Origin", "http://"+f.host+":8097")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE with matching origin and port = %d", rec.Code)
	}
	if rec := yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/presets", f.bob, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET without Origin = %d, want 200", rec.Code)
	}
}

func TestPresetsSecFetchSite(t *testing.T) {
	f := newPresetFixture(t)
	for _, site := range []string{"same-site", "cross-site", "SAME-SITE", "bogus", ""} {
		rec := yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/presets", f.bob, "", "Sec-Fetch-Site", site)
		assertCode(t, rec.Code, rec.Body.String(), "csrf_required", http.StatusForbidden)
		kv := append(sameOriginHeaders(f.host)[:4], "Sec-Fetch-Site", site)
		rec = yodeaDo(t, f.s, http.MethodPut, f.host, presetPath("x"), f.bob, `{}`, kv...)
		assertCode(t, rec.Code, rec.Body.String(), "csrf_required", http.StatusForbidden)
		rec = yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/presets", "", "", "Sec-Fetch-Site", site)
		assertCode(t, rec.Code, rec.Body.String(), "csrf_required", http.StatusForbidden)
	}
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 0 {
		t.Fatalf("cross-site PUT stored %+v", list)
	}
	for _, site := range []string{"same-origin", "none", "Same-Origin"} {
		if rec := yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/presets", f.bob, "", "Sec-Fetch-Site", site); rec.Code != http.StatusOK {
			t.Fatalf("GET Sec-Fetch-Site %q = %d, want 200", site, rec.Code)
		}
		kv := append(sameOriginHeaders(f.host)[:4], "Sec-Fetch-Site", site)
		decodePutPreset(t, yodeaDo(t, f.s, http.MethodPut, f.host, presetPath("x"), f.bob, `{}`, kv...))
	}
}

func TestPresetsUnknownPathsAndMethods(t *testing.T) {
	f := newPresetFixture(t)
	for _, p := range []string{"/_yodea", "/_yodea/", "/_yodea/presetsx", "/_yodea/other", "/_yodea/presets/a/b", "/_yodea/presets/a/", "/_yodea%2Fpresets", "/_yodea/index.html"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rec := yodeaDo(t, f.s, method, f.host, p, f.bob, `{}`, sameOriginHeaders(f.host)...)
			assertCode(t, rec.Code, rec.Body.String(), "not_found", http.StatusNotFound)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		rec := yodeaDo(t, f.s, method, f.host, "/_yodea/presets", f.bob, `{}`, sameOriginHeaders(f.host)...)
		assertCode(t, rec.Code, rec.Body.String(), "method_not_allowed", http.StatusMethodNotAllowed)
		if got := rec.Header().Get("Allow"); got != "GET" {
			t.Fatalf("Allow = %q, want GET", got)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodOptions} {
		rec := yodeaDo(t, f.s, method, f.host, presetPath("warm"), f.bob, `{}`, sameOriginHeaders(f.host)...)
		assertCode(t, rec.Code, rec.Body.String(), "method_not_allowed", http.StatusMethodNotAllowed)
		if got := rec.Header().Get("Allow"); got != "PUT, DELETE" {
			t.Fatalf("Allow = %q, want PUT, DELETE", got)
		}
	}
	rec := yodeaDo(t, f.s, http.MethodOptions, f.host, presetPath("warm"), "", "",
		"Origin", "https://"+f.host2, "Access-Control-Request-Method", "PUT", "Access-Control-Request-Headers", "content-type")
	assertCode(t, rec.Code, rec.Body.String(), "login_required", http.StatusUnauthorized)
}

func TestPresetsNonCanonicalPathsStayJSON(t *testing.T) {
	f := newPresetFixture(t)
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "keep", `{"v":1}`))
	for _, p := range []string{
		"/_yodea//presets", "/_yodea/./presets", "/_yodea/presets/./x", "/_yodea/presets//x",
		"/_yodea/presets/../presets", "/_yodea/../_yodea/presets", "//_yodea/presets",
		"/x/../_yodea/presets", "/_yodea/%2e/presets", "/_yodea/..", "/_yodea/.", "/_yodea/../index.html",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rec := yodeaDo(t, f.s, method, f.host, p, f.bob, `{"v":2}`, sameOriginHeaders(f.host)...)
			assertCode(t, rec.Code, rec.Body.String(), "not_found", http.StatusNotFound)
			rec = yodeaDo(t, f.s, method, f.host, p, "", `{"v":2}`, sameOriginHeaders(f.host)...)
			assertCode(t, rec.Code, rec.Body.String(), "login_required", http.StatusUnauthorized)
		}
	}
	for _, p := range []string{"/_yodea/presets/.", "/_yodea/presets/.."} {
		rec := yodeaDo(t, f.s, http.MethodGet, f.host, p, f.bob, "")
		assertCode(t, rec.Code, rec.Body.String(), "method_not_allowed", http.StatusMethodNotAllowed)
		name := strings.TrimPrefix(p, "/_yodea/presets/")
		if got := decodePutPreset(t, yodeaDo(t, f.s, http.MethodPut, f.host, p, f.bob, `{"dot":1}`, sameOriginHeaders(f.host)...)); got.Name != name {
			t.Fatalf("PUT %s stored name %q, want %q", p, got.Name, name)
		}
		if rec := yodeaDo(t, f.s, http.MethodDelete, f.host, p, f.bob, "", "Origin", "https://"+f.host); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d %s", p, rec.Code, rec.Body.String())
		}
	}
	list := listPresetsReq(t, f.s, f.host, f.bob)
	if len(list) != 1 || list[0].Name != "keep" || !sameJSON(t, string(list[0].Data), `{"v":1}`) {
		t.Fatalf("presets after non-canonical requests = %+v", list)
	}
	if rec := doReq(f.s, http.MethodGet, testDomain, "/api/views", f.bob, nil, ""); strings.Contains(rec.Body.String(), f.label) {
		t.Fatalf("non-canonical /_yodea hits recorded a view: %s", rec.Body.String())
	}
}

func TestPresetsArePersonalAndPerPreview(t *testing.T) {
	f := newPresetFixture(t)
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.alice, "shared", `{"owner":"alice"}`))
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 0 {
		t.Fatalf("bob sees alice's presets: %+v", list)
	}
	rec := deletePresetReq(t, f.s, f.host, f.bob, "shared")
	assertCode(t, rec.Code, rec.Body.String(), "not_found", http.StatusNotFound)
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "shared", `{"owner":"bob"}`))
	if list := listPresetsReq(t, f.s, f.host, f.alice); len(list) != 1 || !sameJSON(t, string(list[0].Data), `{"owner":"alice"}`) {
		t.Fatalf("alice's preset after bob's writes = %+v", list)
	}
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 1 || !sameJSON(t, string(list[0].Data), `{"owner":"bob"}`) {
		t.Fatalf("bob's preset = %+v", list)
	}
	if list := listPresetsReq(t, f.s, f.host2, f.alice); len(list) != 0 {
		t.Fatalf("alice's presets leak to another preview: %+v", list)
	}
	if rec := deletePresetReq(t, f.s, f.host2, f.alice, "shared"); rec.Code != http.StatusNotFound {
		t.Fatalf("delete on other preview = %d, want 404", rec.Code)
	}
	if list := listPresetsReq(t, f.s, f.host, f.alice); len(list) != 1 {
		t.Fatalf("alice lost her preset: %+v", list)
	}
}

func TestPresetsSurviveRedeploy(t *testing.T) {
	f := newPresetFixture(t)
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "keep", `{"v":1}`))
	if label := deployLabel(t, f.s, f.alice, "blog"); label != f.label {
		t.Fatalf("redeploy label = %q, want %q", label, f.label)
	}
	if list := listPresetsReq(t, f.s, f.host, f.bob); len(list) != 1 || list[0].Name != "keep" {
		t.Fatalf("presets after redeploy = %+v", list)
	}
}

func TestPresetsBrowserCookieFlow(t *testing.T) {
	f := newPresetFixture(t)
	sessionValue, _ := loginCookieAndCSRF(t, f.s, "carol@example.com")
	do := func(method, path, body string, kv ...string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Host = f.host
		req.AddCookie(&http.Cookie{Name: "yodea_session", Value: sessionValue})
		for i := 0; i+1 < len(kv); i += 2 {
			req.Header.Set(kv[i], kv[i+1])
		}
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, req)
		assertPresetAPIHeaders(t, method+" "+path, rec)
		return rec
	}
	if rec := do(http.MethodGet, "/_yodea/presets", "", "Sec-Fetch-Site", "same-origin"); rec.Code != http.StatusOK {
		t.Fatalf("cookie GET = %d: %s", rec.Code, rec.Body.String())
	}
	decodePutPreset(t, do(http.MethodPut, presetPath("from browser"), `{"b":1}`, sameOriginHeaders(f.host)...))
	if rec := do(http.MethodDelete, presetPath("from browser"), "", "Origin", "https://"+f.host, "Sec-Fetch-Site", "same-origin"); rec.Code != http.StatusNoContent {
		t.Fatalf("cookie DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	rec := do(http.MethodPut, presetPath("forged"), `{"b":1}`, "Content-Type", "application/json", "Origin", "https://"+f.host2, "Sec-Fetch-Site", "same-site")
	assertCode(t, rec.Code, rec.Body.String(), "csrf_required", http.StatusForbidden)
}

func TestPresetsDoNotRecordViews(t *testing.T) {
	f := newPresetFixture(t)
	views := func() int {
		rec := doReq(f.s, http.MethodGet, testDomain, "/api/views", f.bob, nil, "")
		var v struct {
			Views []any `json:"views"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return len(v.Views)
	}
	decodePutPreset(t, putPresetReq(t, f.s, f.host, f.bob, "a", `{}`))
	listPresetsReq(t, f.s, f.host, f.bob)
	yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/presets", f.bob, "", "Accept", "text/html")
	yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea", f.bob, "", "Accept", "text/html")
	yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/route", f.bob, "", "Accept", "text/html")
	deletePresetReq(t, f.s, f.host, f.bob, "a")
	if n := views(); n != 0 {
		t.Fatalf("views after /_yodea/ hits = %d, want 0", n)
	}
	if rec := doReq(f.s, http.MethodGet, f.host, "/", f.bob, nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("preview page = %d", rec.Code)
	}
	if n := views(); n != 1 {
		t.Fatalf("views after page = %d, want 1", n)
	}
}

func TestPresetsNeverServeUploadedFiles(t *testing.T) {
	s := newTestServer(t)
	alice := devToken(t, "alice@example.com")
	rec := deploy(t, s, alice, "tricky", tarGz(t, map[string]string{
		"index.html":        "<h1>tricky</h1>",
		"_yodea/presets":    "UPLOADED-PRESETS",
		"_yodea/index.html": "UPLOADED-INDEX",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy with _yodea/ entries = %d %s", rec.Code, rec.Body.String())
	}
	var dep struct {
		Label string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	host := dep.Label + "." + testDomain
	for _, p := range []string{"/_yodea/presets", "/_yodea/index.html", "/_yodea/", "/_yodea", "/%5Fyodea/presets", "/_yodea%2Fpresets"} {
		for _, token := range []string{alice, ""} {
			rec := yodeaDo(t, s, http.MethodGet, host, p, token, "")
			if strings.Contains(rec.Body.String(), "UPLOADED") {
				t.Fatalf("GET %s served uploaded file: %q", p, rec.Body.String())
			}
		}
	}
	if rec := doReq(s, http.MethodGet, host, "/", alice, nil, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "tricky") {
		t.Fatalf("preview root = %d %q", rec.Code, rec.Body.String())
	}
}

type failingPresetStore struct{ store.Storage }

func (failingPresetStore) ListPresets(_, _, _ string) ([]store.Preset, error) {
	return nil, errors.New("supabase down password=secret")
}

func (failingPresetStore) PutPreset(_ string, _ store.Preset) (store.Preset, error) {
	return store.Preset{}, errors.New("supabase down password=secret")
}

func (failingPresetStore) DeletePreset(_, _, _, _ string) (bool, error) {
	return false, errors.New("supabase down password=secret")
}

func TestPresetsStoreErrorsAreSanitized500(t *testing.T) {
	f := newPresetFixture(t)
	f.s.metadb = failingPresetStore{Storage: f.s.metadb}
	for _, rec := range []*httptest.ResponseRecorder{
		yodeaDo(t, f.s, http.MethodGet, f.host, "/_yodea/presets", f.bob, ""),
		putPresetReq(t, f.s, f.host, f.bob, "x", `{}`),
		deletePresetReq(t, f.s, f.host, f.bob, "x"),
	} {
		assertCode(t, rec.Code, rec.Body.String(), "unknown", http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "supabase") {
			t.Fatalf("error body leaks store detail: %s", rec.Body.String())
		}
	}
}
