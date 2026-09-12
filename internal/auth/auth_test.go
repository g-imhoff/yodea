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

func TestUserPartDistinguishesLongNames(t *testing.T) {
	a := UserPart("dev-alice-long-x")
	b := UserPart("dev-alice-long-y")
	if a == b {
		t.Fatalf("long dev users collide: %q", a)
	}
	if len(a) > 20 || len(b) > 20 {
		t.Fatalf("user parts exceed 20 chars: %q %q", a, b)
	}
}

func TestUserPartDistinguishesUUIDPrefixPair(t *testing.T) {
	u1 := "123e4567-e89b-12d3-a456-426614174000"
	u2 := "123e4567-e89c-12d3-a456-426614174000"
	p1, p2 := UserPart(u1), UserPart(u2)
	if p1 == p2 {
		t.Fatalf("UUID-like users collide: %q for %q vs %q", p1, u1, u2)
	}
	if len(p1) > 20 || len(p2) > 20 {
		t.Fatalf("user parts exceed 20 chars: %q %q", p1, p2)
	}
}

func TestUserPartBudgetAndShape(t *testing.T) {
	long := UserPart("abcdefghijklmnopqrstuvwxyz-0123456789")
	if len(long) > 20 {
		t.Fatalf("user part too long: %q (%d)", long, len(long))
	}
	if strings.HasPrefix(long, "-") || strings.HasSuffix(long, "-") {
		t.Fatalf("user part has edge hyphen: %q", long)
	}
	// Truncation must not leave a trailing hyphen.
	trimmed := UserPart(strings.Repeat("a", 19) + "-bbb")
	if strings.HasSuffix(trimmed, "-") {
		t.Fatalf("truncated part has trailing hyphen: %q", trimmed)
	}
	if len(trimmed) > 20 {
		t.Fatalf("trimmed part too long: %q", trimmed)
	}
	for _, tc := range []string{"", "---", "!!!"} {
		if got := UserPart(tc); got == "" || strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Fatalf("UserPart(%q) = %q, want safe fallback", tc, got)
		}
	}
	for _, id := range []string{"Dev_Alice.Long-X", "123E4567-E89B-xyz"} {
		got := UserPart(id)
		for _, r := range got {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
				continue
			}
			t.Fatalf("UserPart(%q) = %q has unsafe rune %q", id, got, r)
		}
	}
}
