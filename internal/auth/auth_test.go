package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/golang-jwt/jwt/v5"
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

const testIssuer = "https://test.supabase.co/auth/v1"

type testKeys struct {
	verifier   *Verifier
	rsaPriv    *rsa.PrivateKey
	hmacSecret []byte
}

func newTestVerifier(t *testing.T) testKeys {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("test-hmac-secret-32-bytes-long!!")
	jwks := keyfunc.NewGiven(map[string]keyfunc.GivenKey{
		"rsa-kid":  keyfunc.NewGivenRSA(&priv.PublicKey, keyfunc.GivenKeyOptions{Algorithm: "RS256"}),
		"hmac-kid": keyfunc.NewGivenHMAC(secret, keyfunc.GivenKeyOptions{Algorithm: "HS256"}),
	})
	return testKeys{
		verifier:   &Verifier{jwks: jwks, issuer: testIssuer},
		rsaPriv:    priv,
		hmacSecret: secret,
	}
}

func signRS256(t *testing.T, priv *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "rsa-kid"
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func signHS256(t *testing.T, secret []byte, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = "hmac-kid"
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyAcceptsValidRS256(t *testing.T) {
	tk := newTestVerifier(t)
	signed := signRS256(t, tk.rsaPriv, jwt.MapClaims{
		"iss": testIssuer,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	sub, err := tk.verifier.Verify(signed)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if sub != "user-123" {
		t.Fatalf("sub = %q", sub)
	}
}

func TestVerifyRejectsMissingExp(t *testing.T) {
	tk := newTestVerifier(t)
	signed := signRS256(t, tk.rsaPriv, jwt.MapClaims{
		"iss": testIssuer,
		"sub": "user-123",
	})
	if _, err := tk.verifier.Verify(signed); err == nil {
		t.Fatal("token without exp accepted")
	}
}

func TestVerifyRejectsWrongAlg(t *testing.T) {
	tk := newTestVerifier(t)
	// HS256 signature is valid against the JWKS HMAC key, so only the
	// parser's valid-methods pin can reject it.
	signed := signHS256(t, tk.hmacSecret, jwt.MapClaims{
		"iss": testIssuer,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := tk.verifier.Verify(signed); err == nil {
		t.Fatal("HS256 token accepted")
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	tk := newTestVerifier(t)
	signed := signRS256(t, tk.rsaPriv, jwt.MapClaims{
		"iss": "https://evil.example.com/auth/v1",
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := tk.verifier.Verify(signed); err == nil {
		t.Fatal("wrong-issuer token accepted")
	}
}

func TestVerifyClockSkewLeeway(t *testing.T) {
	tk := newTestVerifier(t)
	justExpired := signRS256(t, tk.rsaPriv, jwt.MapClaims{
		"iss": testIssuer,
		"sub": "user-123",
		"exp": time.Now().Add(-10 * time.Second).Unix(),
	})
	if _, err := tk.verifier.Verify(justExpired); err != nil {
		t.Fatalf("token 10s past exp rejected, leeway lost: %v", err)
	}
	longExpired := signRS256(t, tk.rsaPriv, jwt.MapClaims{
		"iss": testIssuer,
		"sub": "user-123",
		"exp": time.Now().Add(-60 * time.Second).Unix(),
	})
	if _, err := tk.verifier.Verify(longExpired); err == nil {
		t.Fatal("token 60s past exp accepted")
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
