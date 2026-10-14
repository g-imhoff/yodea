package main

import (
	"context"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func devArgs(t *testing.T, addr, domain string, extra ...string) []string {
	return append([]string{"yodead", "--dev", "--addr", addr, "--data-dir", t.TempDir(), "--domain", domain}, extra...)
}

func mapEnv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func expectRefusal(t *testing.T, env map[string]string, args []string, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := run(ctx, args, mapEnv(env), io.Discard)
	if exitCode(err) == 0 {
		t.Fatalf("run %v env %v = %v, want a non-zero exit", args, env, err)
	}
	if !strings.Contains(err.Error(), "dev mode refused") || !strings.Contains(err.Error(), want) {
		t.Fatalf("run %v env %v = %q, want dev mode refused naming %q", args, env, err, want)
	}
}

type logWatch struct {
	mu        sync.Mutex
	buf       strings.Builder
	listening chan struct{}
}

func (l *logWatch) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	if l.listening != nil && strings.Contains(l.buf.String(), "yodead listening on") {
		close(l.listening)
		l.listening = nil
	}
	return len(p), nil
}

func (l *logWatch) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func expectStart(t *testing.T, env map[string]string, args []string) string {
	t.Helper()
	listening := make(chan struct{})
	logs := &logWatch{listening: listening}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, mapEnv(env), io.Discard) }()
	select {
	case <-listening:
	case err := <-done:
		t.Fatalf("run %v env %v exited before listening: %v\n%s", args, env, err, logs)
	case <-time.After(15 * time.Second):
		t.Fatalf("run %v env %v never listened:\n%s", args, env, logs)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run after SIGTERM = %v, want clean exit\n%s", err, logs)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("run did not return after SIGTERM\n%s", logs)
	}
	return logs.String()
}

func TestDevPublicRefusedWithoutEscapeHatch(t *testing.T) {
	expectRefusal(t, nil, devArgs(t, "127.0.0.1:0", "example.com"), "base domain")
	expectRefusal(t, nil, devArgs(t, "0.0.0.0:0", "previews.example.test"), "bind")
	expectRefusal(t, map[string]string{"YODEA_ALLOW_INSECURE_DEV_PUBLIC": "0"}, devArgs(t, "127.0.0.1:0", "example.com"), "base domain")
	expectRefusal(t, map[string]string{"YODEA_ALLOW_INSECURE_DEV_PUBLIC": "true"}, devArgs(t, "127.0.0.1:0", "example.com"), "base domain")
	expectRefusal(t, map[string]string{"YODEA_ALLOW_INSECURE_DEV_PUBLIC": "1"}, devArgs(t, "127.0.0.1:0", "example.com", "--allow-insecure-dev-public=false"), "base domain")
}

func TestDevPublicStartsWithEnvEscapeHatch(t *testing.T) {
	out := expectStart(t, map[string]string{"YODEA_ALLOW_INSECURE_DEV_PUBLIC": "1"}, devArgs(t, "127.0.0.1:0", "previews.example.com"))
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "forgeable") {
		t.Fatalf("startup log = %q, want the forgeable-token warning", out)
	}
	if !strings.Contains(out, "for previews.example.com") || !strings.Contains(out, "dev=true") || !strings.Contains(out, "store=local") {
		t.Fatalf("startup log = %q, want dev local store on previews.example.com", out)
	}
}

func TestDevPublicStartsWithFlagEscapeHatch(t *testing.T) {
	out := expectStart(t, nil, devArgs(t, "127.0.0.1:0", "example.com", "--allow-insecure-dev-public"))
	if !strings.Contains(out, "forgeable") {
		t.Fatalf("startup log = %q, want the forgeable-token warning", out)
	}
}

func TestDevLoopbackTestDomainStartsWithoutWarning(t *testing.T) {
	out := expectStart(t, nil, devArgs(t, "127.0.0.1:0", "previews.example.test"))
	if strings.Contains(out, "forgeable") {
		t.Fatalf("startup log = %q, want no escape-hatch warning", out)
	}
}
