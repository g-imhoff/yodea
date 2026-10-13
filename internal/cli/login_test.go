package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLoginRejectsPasswordFlag(t *testing.T) {
	isolateSession(t)
	env := map[string]string{"YODEA_PASSWORD": "env-secret"}
	_, _, err := runCLI(t, env, "--server", "http://127.0.0.1:8093", "login", "--email", "a@b.c", "--password", "argv-secret")
	if err == nil {
		t.Fatal("expected --password flag to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "not defined") && !strings.Contains(err.Error(), "password") {
		t.Fatalf("want unknown-flag error for --password, got %v", err)
	}
}

func TestLoginRejectsPositional(t *testing.T) {
	isolateSession(t)
	env := map[string]string{"YODEA_EMAIL": "a@b.c", "YODEA_PASSWORD": "env-secret"}
	_, _, err := runCLI(t, env, "--server", "http://127.0.0.1:8093", "login", "extra")
	if err == nil {
		t.Fatal("expected login with positional to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for login with positional, got %v", err)
	}
}

func TestLoginQuotesHostileIdentity(t *testing.T) {
	isolateSession(t)
	hostileWho := "evil\tuser\ninject"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "yodea_session", Value: "tok"})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"user_id":%q}`, hostileWho)
	}))
	defer srv.Close()
	env := map[string]string{"YODEA_PASSWORD": "secret"}
	out, _, err := runCLI(t, env, "--server", srv.URL, "login", "--email", "a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	wantWho := fmt.Sprintf("%q", hostileWho)
	wantServer := fmt.Sprintf("%q", srv.URL)
	if !strings.Contains(out, wantWho) {
		t.Fatalf("login output should quote identity as %s, got %q", wantWho, out)
	}
	if !strings.Contains(out, wantServer) {
		t.Fatalf("login output should quote server as %s, got %q", wantServer, out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("quoted login output should be one line, got %q", out)
	}
}

func TestLoginPromptNeedsTerminal(t *testing.T) {
	isolateSession(t)
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	const notFile = "reading password: stdin is not a terminal"
	cases := []struct {
		name    string
		in      io.Reader
		wantErr string // prefix
	}{
		{"reader", strings.NewReader(""), notFile},
		// A file that is not a terminal fails inside term.ReadPassword with
		// the OS error, on Linux "inappropriate ioctl for device".
		{"file", devNull, "reading password: "},
	}
	for _, tc := range cases {
		a, out, errOut := newTestApp(t, nil)
		a.io.In = tc.in
		err := a.run([]string{"--server", "http://127.0.0.1:1", "login", "--email", "a@b.c"})
		if err == nil || !strings.HasPrefix(err.Error(), tc.wantErr) || (tc.in == devNull && err.Error() == notFile) {
			t.Errorf("%s: want a password prompt error starting %q, got %v", tc.name, tc.wantErr, err)
		}
		if errOut.String() != "password: \n" {
			t.Errorf("%s: prompt should go to stderr, got %q", tc.name, errOut.String())
		}
		if out.String() != "" {
			t.Errorf("%s: stdout should stay empty, got %q", tc.name, out.String())
		}
	}
}
