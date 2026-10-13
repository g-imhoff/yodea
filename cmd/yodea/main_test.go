package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain runs main instead of the tests when TestMainStreams re-executes
// the test binary.
func TestMain(m *testing.M) {
	if os.Getenv("YODEA_TEST_RUN_MAIN") == "1" {
		main()
	}
	os.Exit(m.Run())
}

// TestMainStreams checks that main wires the process streams: output to
// stdout, errors to stderr, and the exit status.
func TestMainStreams(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args       []string
		code       int
		wantStdout string // prefix
		wantStderr string
	}{
		{[]string{"help"}, 0, "yodea [--server URL] <command> [options]\n", ""},
		{[]string{"bogus"}, 1, "", "yodea: unknown command \"bogus\" (want login, init, push, list, or delete)\n"},
	}
	for _, tc := range cases {
		cmd := exec.Command(exe, tc.args...)
		cmd.Env = append(os.Environ(), "YODEA_TEST_RUN_MAIN=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatal(err)
			}
			code = exitErr.ExitCode()
		}
		if code != tc.code {
			t.Errorf("yodea %q exited %d, want %d", tc.args, code, tc.code)
		}
		if !strings.HasPrefix(stdout.String(), tc.wantStdout) || (tc.wantStdout == "") != (stdout.Len() == 0) {
			t.Errorf("yodea %q stdout = %q, want prefix %q", tc.args, stdout.String(), tc.wantStdout)
		}
		if stderr.String() != tc.wantStderr {
			t.Errorf("yodea %q stderr = %q, want %q", tc.args, stderr.String(), tc.wantStderr)
		}
	}
}
