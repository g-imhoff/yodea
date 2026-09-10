package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDevTokensDistinctPerViewer(t *testing.T) {
	v := NewDevVerifier("")
	aTok, aID, err := DevTokenFor("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	bTok, bID, err := DevTokenFor("bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if aID == bID {
		t.Fatalf("dev users collide: %q", aID)
	}
	if got, err := v.Verify(aTok); err != nil || got != aID {
		t.Fatalf("verify alice = %q, %v", got, err)
	}
	if got, err := v.Verify(bTok); err != nil || got != bID {
		t.Fatalf("verify bob = %q, %v", got, err)
	}
	if _, err := v.Verify("forged"); err == nil {
		t.Fatal("forged dev token accepted")
	}
	if _, _, err := DevTokenFor(""); err == nil {
		t.Fatal("empty email accepted")
	}
}

func TestLoginShape(t *testing.T) {
	var sawKey, sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("apikey")
		sawPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","refresh_token":"ref","expires_in":3600}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "anon-key")
	sess, err := c.Login("a@b.c", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if sess.AccessToken != "tok" || sess.RefreshToken != "ref" {
		t.Fatalf("bad session: %+v", sess)
	}
	if sawKey != "anon-key" || !strings.Contains(sawPath, "grant_type=password") {
		t.Fatalf("bad upstream call: key=%q path=%q", sawKey, sawPath)
	}
}
