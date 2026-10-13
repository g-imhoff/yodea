package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRejectsFlagAfterProject(t *testing.T) {
	t.Chdir(t.TempDir())
	_, _, err := runCLI(t, nil, "init", "demo", "--dir", t.TempDir())
	if err == nil {
		t.Fatal("expected init with flag after project to fail, got nil")
	}
	if !strings.Contains(err.Error(), "before") {
		t.Fatalf("want flag-order error mentioning flags go before project, got %v", err)
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage in flag-order error, got %v", err)
	}
}

func TestInitRejectsTwoProjects(t *testing.T) {
	t.Chdir(t.TempDir())
	_, _, err := runCLI(t, nil, "init", "a", "b")
	if err == nil {
		t.Fatal("expected init with two projects to fail, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error for two projects, got %v", err)
	}
}

func TestInitQuotesHostileDir(t *testing.T) {
	hostileDir := filepath.Join(t.TempDir(), "evil\tdir\ninject")
	out, _, err := runCLI(t, nil, "init", "--dir", hostileDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	wantDir := fmt.Sprintf("%q", hostileDir)
	if !strings.Contains(out, `"demo"`) {
		t.Fatalf("init output should quote project, got %q", out)
	}
	if !strings.Contains(out, wantDir) {
		t.Fatalf("init output should quote dir as %s, got %q", wantDir, out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("quoted init output should be one line, got %q", out)
	}
}
