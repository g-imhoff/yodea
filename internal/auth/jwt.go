package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/golang-jwt/jwt/v5"
)

// Verifier checks Supabase access tokens and returns the user ID (sub).
type Verifier struct {
	jwks      *keyfunc.JWKS
	issuer    string
	devMode   bool
	devUserID string
}

// NewVerifier loads the JWKS for asymmetric Supabase signing keys and
// refreshes it in the background. jwksURL is
// {SUPABASE_URL}/auth/v1/.well-known/jwks.json and issuer is
// {SUPABASE_URL}/auth/v1.
func NewVerifier(jwksURL, issuer string) (*Verifier, error) {
	if jwksURL == "" || issuer == "" {
		return nil, errors.New("jwks URL and issuer are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel // refresh loop ends with the process; EndBackground is wired in server shutdown below.
	jwks, err := keyfunc.Get(jwksURL, keyfunc.Options{
		Ctx:                 ctx,
		RefreshInterval:     10 * time.Minute, // matches Supabase edge cache
		RefreshUnknownKID:   true,             // pick up rotations without restart
		RefreshErrorHandler: func(err error) {},
	})
	if err != nil {
		return nil, fmt.Errorf("load JWKS: %w", err)
	}
	return &Verifier{jwks: jwks, issuer: issuer}, nil
}

// NewDevVerifier accepts only the literal token "dev" for local testing.
// It must never run in production.
func NewDevVerifier(userID string) *Verifier {
	if userID == "" {
		userID = "dev-user"
	}
	return &Verifier{devMode: true, devUserID: userID}
}

// Close stops the JWKS background refresh.
func (v *Verifier) Close() {
	if v.jwks != nil {
		v.jwks.EndBackground()
	}
}

// Verify checks signature, expiry, and issuer, then returns sub.
func (v *Verifier) Verify(tokenString string) (string, error) {
	if v.devMode {
		if tokenString == "dev" {
			return v.devUserID, nil
		}
		return "", errors.New("invalid dev token")
	}
	if tokenString == "" {
		return "", errors.New("missing token")
	}
	token, err := jwt.Parse(tokenString, v.jwks.Keyfunc)
	if err != nil {
		return "", fmt.Errorf("parse token: %w", err)
	}
	if !token.Valid {
		return "", errors.New("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("unexpected claims shape")
	}
	iss, _ := claims["iss"].(string)
	if iss != v.issuer {
		return "", fmt.Errorf("wrong issuer %q", iss)
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", errors.New("missing sub claim")
	}
	if exp, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() > int64(exp)+30 { // 30s clock skew allowance
			return "", errors.New("token expired")
		}
	}
	return sub, nil
}

// UserPart derives a DNS-safe handle from a user ID for subdomain labels.
func UserPart(userID string) string {
	s := strings.ToLower(userID)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(collapse(b.String()), "-")
	if len(out) > 12 {
		out = out[:12]
	}
	if out == "" {
		out = "user"
	}
	return out
}

func collapse(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if !prevDash {
				b.WriteRune(r)
			}
			prevDash = true
			continue
		}
		prevDash = false
		b.WriteRune(r)
	}
	return b.String()
}
