package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/client"
)

func TestListRejectsPositional(t *testing.T) {
	isolateSession(t)
	_, _, err := runCLI(t, nil, "--server", "http://127.0.0.1:8093", "list", "extra")
	if err == nil {
		t.Fatal("expected list with positional to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for list with positional, got %v", err)
	}
}

func TestListQuotesNames(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sites":[{"project":"a\tb","label":"x\ny","files":1}]}`))
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "--server", srv.URL, "list")
	if err != nil {
		t.Fatal(err)
	}
	wantProject := fmt.Sprintf("%q", "a\tb")
	wantLabel := fmt.Sprintf("%q", "x\ny")
	if !strings.Contains(out, wantProject) {
		t.Fatalf("list output should quote project as %s, got %q", wantProject, out)
	}
	if !strings.Contains(out, wantLabel) {
		t.Fatalf("list output should quote label as %s, got %q", wantLabel, out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("quoted list output should be one line, got %q", out)
	}
}
