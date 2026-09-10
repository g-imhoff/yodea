package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/client"
)

func isolateSession(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.json")
	t.Setenv("YODEA_SESSION_FILE", p)
	return p
}

func TestAuthedExplicitFlagWins(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	if err := client.SaveSession(client.Session{Server: "http://saved.example", Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	c, err := authed("http://flag.example", true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://flag.example" {
		t.Fatalf("explicit --server flag lost: got %q want flag", c.Server)
	}
}

func TestAuthedFallsBackToSessionOnlyWithoutFlagOrEnv(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	if err := client.SaveSession(client.Session{Server: "http://saved.example", Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	c, err := authed(client.ResolveServer(""), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://saved.example" {
		t.Fatalf("want saved session server, got %q", c.Server)
	}
	// Explicit env beats the saved session.
	t.Setenv("YODEA_SERVER", "http://env.example")
	c, err = authed(client.ResolveServer(""), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://env.example" {
		t.Fatalf("want env server, got %q", c.Server)
	}
}

func TestLoginRejectsPasswordFlag(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_PASSWORD", "env-secret")
	err := cmdLogin("http://127.0.0.1:8093", []string{"--email", "a@b.c", "--password", "argv-secret"})
	if err == nil {
		t.Fatal("expected --password flag to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "password") {
		t.Fatalf("want unknown-flag error for --password, got %v", err)
	}
}
