package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/server"
)

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	required := []string{"--data-dir", "/srv/yodea", "--domain", "previews.example.com"}
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want server.Config
	}{
		{
			name: "defaults",
			args: required,
			want: server.Config{Addr: "127.0.0.1:8093", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local", SecureCookies: true},
		},
		{
			name: "env only",
			env: map[string]string{
				"YODEA_DEV": "1", "YODEA_ADDR": "127.0.0.1:9000", "YODEA_DATA_DIR": "/data", "YODEA_BASE_DOMAIN": "previews.test",
				"YODEA_STORE": "supabase", "SUPABASE_URL": "https://sb.example", "SUPABASE_ANON_KEY": "anon", "SUPABASE_SERVICE_KEY": "service",
			},
			want: server.Config{
				Addr: "127.0.0.1:9000", DataDir: "/data", BaseDomain: "previews.test", StoreBackend: "supabase",
				SupabaseURL: "https://sb.example", AnonKey: "anon", SupabaseKey: "service", DevNoAuth: true,
			},
		},
		{
			name: "flags beat env",
			args: []string{"--dev=false", "--addr", ":7000", "--data-dir", "/flag", "--domain", "flag.example.com", "--store", "local"},
			env:  map[string]string{"YODEA_DEV": "1", "YODEA_ADDR": ":9000", "YODEA_DATA_DIR": "/env", "YODEA_BASE_DOMAIN": "env.example.com", "YODEA_STORE": "supabase"},
			want: server.Config{Addr: ":7000", DataDir: "/flag", BaseDomain: "flag.example.com", StoreBackend: "local", SecureCookies: true},
		},
		{
			name: "YODEA_DEV is only 1",
			args: required,
			env:  map[string]string{"YODEA_DEV": "true"},
			want: server.Config{Addr: "127.0.0.1:8093", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local", SecureCookies: true},
		},
		{
			name: "PORT alone is a loopback port",
			args: required,
			env:  map[string]string{"PORT": "8080"},
			want: server.Config{Addr: "127.0.0.1:8080", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local", SecureCookies: true},
		},
		{
			name: "PORT with a colon is an address",
			args: required,
			env:  map[string]string{"PORT": "0.0.0.0:8080"},
			want: server.Config{Addr: "0.0.0.0:8080", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local", SecureCookies: true},
		},
		{
			name: "YODEA_ADDR beats PORT",
			args: required,
			env:  map[string]string{"PORT": "8080", "YODEA_ADDR": "127.0.0.1:9000"},
			want: server.Config{Addr: "127.0.0.1:9000", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local", SecureCookies: true},
		},
		{
			name: "secure cookies off",
			args: required,
			env:  map[string]string{"YODEA_SECURE_COOKIES": " No "},
			want: server.Config{Addr: "127.0.0.1:8093", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local"},
		},
		{
			name: "secure cookies forced in dev",
			args: append([]string{"--dev"}, required...),
			env:  map[string]string{"YODEA_SECURE_COOKIES": "TRUE"},
			want: server.Config{Addr: "127.0.0.1:8093", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local", DevNoAuth: true, SecureCookies: true},
		},
		{
			name: "unknown secure cookies value keeps the default",
			args: append([]string{"--dev"}, required...),
			env:  map[string]string{"YODEA_SECURE_COOKIES": "on"},
			want: server.Config{Addr: "127.0.0.1:8093", DataDir: "/srv/yodea", BaseDomain: "previews.example.com", StoreBackend: "local", DevNoAuth: true},
		},
	}
	for _, tc := range cases {
		args := append([]string{"yodead"}, tc.args...)
		got, err := loadConfig(args, func(k string) string { return tc.env[k] }, io.Discard)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestLoadConfigAllowInsecureDevPublic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{"default off", nil, "", false},
		{"env 1", nil, "1", true},
		{"env is only 1", nil, "true", false},
		{"flag", []string{"--allow-insecure-dev-public"}, "", true},
		{"flag beats env", []string{"--allow-insecure-dev-public=false"}, "1", false},
	}
	for _, tc := range cases {
		args := append([]string{"yodead", "--data-dir", "/data", "--domain", "previews.example.com"}, tc.args...)
		env := map[string]string{"YODEA_ALLOW_INSECURE_DEV_PUBLIC": tc.env}
		got, err := loadConfig(args, func(k string) string { return env[k] }, io.Discard)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got.AllowInsecureDevPublic != tc.want {
			t.Errorf("%s: AllowInsecureDevPublic = %v, want %v", tc.name, got.AllowInsecureDevPublic, tc.want)
		}
	}
}

func TestLoadConfigErrors(t *testing.T) {
	t.Parallel()
	noEnv := func(string) string { return "" }

	_, err := loadConfig([]string{"yodead", "--data-dir", "/srv/yodea"}, noEnv, io.Discard)
	if err == nil || err.Error() != "--data-dir and --domain (or YODEA_DATA_DIR/YODEA_BASE_DOMAIN) are required" {
		t.Errorf("missing --domain: got %v", err)
	}

	var out bytes.Buffer
	_, err = loadConfig([]string{"yodead", "-h"}, noEnv, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: got %v, want flag.ErrHelp", err)
	}
	if !strings.HasPrefix(out.String(), "Usage of yodead:\n  -addr string\n") {
		t.Errorf("-h usage: got %q", out.String())
	}

	out.Reset()
	_, err = loadConfig([]string{"yodead", "--bogus"}, noEnv, &out)
	if !errors.As(err, new(flagError)) {
		t.Errorf("--bogus: got %v, want a flagError", err)
	}
	if !strings.HasPrefix(out.String(), "flag provided but not defined: -bogus\nUsage of yodead:\n") {
		t.Errorf("--bogus output: got %q", out.String())
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		code int
	}{
		{nil, 0},
		{flagError{flag.ErrHelp}, 0},
		{flagError{errors.New("flag provided but not defined: -bogus")}, 2},
		{errors.New("unknown store backend \"x\""), 1},
	}
	for _, tc := range cases {
		if got := exitCode(tc.err); got != tc.code {
			t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.code)
		}
	}
}

func TestRunReturnsServerErrors(t *testing.T) {
	t.Parallel()
	args := []string{"yodead", "--dev", "--data-dir", t.TempDir(), "--domain", "previews.test", "--store", "bogus"}
	err := run(context.Background(), args, func(string) string { return "" }, io.Discard)
	if err == nil || err.Error() != `unknown store backend "bogus"` {
		t.Fatalf("run: got %v, want the server.New error", err)
	}
}
