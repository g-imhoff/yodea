package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/g-imhoff/yodea/internal/client"
)

func TestDeleteRejectsExtraPositionals(t *testing.T) {
	isolateSession(t)
	_, _, err := runCLI(t, nil, "--server", "http://127.0.0.1:8093", "delete", "my", "app")
	if err == nil {
		t.Fatal("expected delete with two projects to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for delete with extra positionals, got %v", err)
	}
	_, _, err = runCLI(t, nil, "--server", "http://127.0.0.1:8093", "delete")
	if err == nil {
		t.Fatal("expected delete with no project to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for delete with no project, got %v", err)
	}
}

func TestDeleteQuotesName(t *testing.T) {
	isolateSession(t)
	t.Setenv("YODEA_SERVER", "")
	t.Setenv("YODEA_DEV", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := client.SaveSession(client.Session{Server: srv.URL, Token: "tok", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, nil, "--server", srv.URL, "delete", "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `deleted "myapp"`) {
		t.Fatalf("delete output should quote name, got %q", out)
	}
}
