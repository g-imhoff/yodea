package cli

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/client"
)

// testEnv returns an env map with a fresh session file plus a getenv
// reading it. Tests seed sessions with client.SaveSession(s, getenv) and
// run the CLI with runCLI(t, env, ...): the process environment stays
// untouched, so the test can be parallel.
func testEnv(t *testing.T) (map[string]string, func(string) string) {
	t.Helper()
	env := map[string]string{"YODEA_SESSION_FILE": filepath.Join(t.TempDir(), "session.json")}
	return env, func(key string) string { return env[key] }
}

// newTestApp returns an app that writes to buffers and reads every
// variable from env.
func newTestApp(t *testing.T, env map[string]string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a := &app{
		io:     IOStreams{In: strings.NewReader(""), Out: &stdout, ErrOut: &stderr},
		getenv: func(key string) string { return env[key] },
	}
	return a, &stdout, &stderr
}

// runCLI runs yodea with args and returns what it wrote plus the error
// Main would print. env is as for newTestApp.
func runCLI(t *testing.T, env map[string]string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	a, out, errOut := newTestApp(t, env)
	err = a.run(args)
	return out.String(), errOut.String(), err
}

func TestMainExitStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args       []string
		code       int
		wantStdout string // prefix
		wantStderr string
	}{
		{nil, 2, "", "yodea: usage: yodea [--server URL] <login|init|push|list|delete> [options]\n"},
		{[]string{"help"}, 0, "yodea [--server URL] <command> [options]\n", ""},
		{[]string{"bogus"}, 1, "", "yodea: unknown command \"bogus\" (want login, init, push, list, or delete)\n"},
		{[]string{"help", "bogus"}, 1, "", "yodea: unknown command \"bogus\" (want login, init, push, list, or delete)\n"},
		{[]string{"--server"}, 2, "", "yodea: usage: yodea --server URL <command> [options]\n"},
		// A bad flag prints the flag error plus the command help once, on
		// stderr, and Main adds no "yodea:" line of its own.
		{[]string{"push", "--bad-flag"}, 2, "", "flag provided but not defined: -bad-flag\n" + golden(t, "push")},
		// The flag package prints this help on stderr, and Main adds nothing.
		{[]string{"list", "--h"}, 0, "", golden(t, "list")},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		streams := IOStreams{In: strings.NewReader(""), Out: &stdout, ErrOut: &stderr}
		code := Main(tc.args, streams, func(string) string { return "" })
		if code != tc.code {
			t.Errorf("Main(%q) = %d, want %d", tc.args, code, tc.code)
		}
		if !strings.HasPrefix(stdout.String(), tc.wantStdout) {
			t.Errorf("Main(%q) stdout = %q, want prefix %q", tc.args, stdout.String(), tc.wantStdout)
		}
		if stderr.String() != tc.wantStderr {
			t.Errorf("Main(%q) stderr = %q, want %q", tc.args, stderr.String(), tc.wantStderr)
		}
	}
}

func TestParseGlobal(t *testing.T) {
	t.Parallel()
	const url = "http://127.0.0.1:8093"
	cases := []struct {
		args     []string
		want     globalOptions
		wantRest []string
		wantErr  bool
	}{
		{args: []string{"list"}, wantRest: []string{"list"}},
		{args: []string{"--server", url, "list"}, want: globalOptions{url, true}, wantRest: []string{"list"}},
		{args: []string{"-server", url, "list", "x"}, want: globalOptions{url, true}, wantRest: []string{"list", "x"}},
		{args: []string{"--server=" + url, "list"}, want: globalOptions{url, true}, wantRest: []string{"list"}},
		{args: []string{"-server=" + url, "list"}, want: globalOptions{url, true}, wantRest: []string{"list"}},
		{args: []string{"--server=a=b", "list"}, want: globalOptions{"a=b", true}, wantRest: []string{"list"}},
		// An empty value is accepted in the two-argument form only.
		{args: []string{"--server", "", "list"}, want: globalOptions{"", true}, wantRest: []string{"list"}},
		{args: []string{"--server=", "list"}, wantErr: true},
		{args: []string{"--server"}, wantErr: true},
		{args: []string{"--server", url}, wantErr: true},
		{args: []string{"--server=" + url}, wantErr: true},
		// Only the first argument is a global option.
		{args: []string{"list", "--server", url}, wantRest: []string{"list", "--server", url}},
		{args: []string{"--server", url, "--server", url, "list"}, want: globalOptions{url, true}, wantRest: []string{"--server", url, "list"}},
		{args: []string{"--bogus", "list"}, wantRest: []string{"--bogus", "list"}},
	}
	for _, tc := range cases {
		got, rest, err := parseGlobal(tc.args)
		if tc.wantErr {
			if err == nil || err.Error() != "usage: yodea --server URL <command> [options]" {
				t.Errorf("parseGlobal(%q) error = %v, want the --server usage error", tc.args, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGlobal(%q) error = %v", tc.args, err)
			continue
		}
		if got != tc.want || !reflect.DeepEqual(rest, tc.wantRest) {
			t.Errorf("parseGlobal(%q) = %+v, %q; want %+v, %q", tc.args, got, rest, tc.want, tc.wantRest)
		}
	}
}

func TestAuthedExplicitFlagWins(t *testing.T) {
	t.Parallel()
	env, getenv := testEnv(t)
	if err := client.SaveSession(client.Session{Server: "http://saved.example", Token: "tok", UserID: "u"}, getenv); err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t, env)
	a.global = globalOptions{server: "http://flag.example", serverSet: true}
	c, err := a.authed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://flag.example" {
		t.Fatalf("explicit --server flag lost: got %q want flag", c.Server)
	}
}

func TestAuthedFallsBackToSessionOnlyWithoutFlagOrEnv(t *testing.T) {
	t.Parallel()
	env, getenv := testEnv(t)
	if err := client.SaveSession(client.Session{Server: "http://saved.example", Token: "tok", UserID: "u"}, getenv); err != nil {
		t.Fatal(err)
	}
	a, _, _ := newTestApp(t, env)
	c, err := a.authed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://saved.example" {
		t.Fatalf("want saved session server, got %q", c.Server)
	}
	// Explicit env beats the saved session.
	env["YODEA_SERVER"] = "http://env.example"
	c, err = a.authed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Server != "http://env.example" {
		t.Fatalf("want env server, got %q", c.Server)
	}
}
