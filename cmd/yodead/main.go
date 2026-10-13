// Command yodead is the yodea backend: central dashboard plus login,
// session API, site deploys, preview subdomains, views, and favorites.
//
// Configuration precedence is flag > env > default: every --flag defaults
// from its YODEA_* env var and falls back to a built-in default.
//
//	YODEA_DATA_DIR       metadata plus sites dir (required)
//	YODEA_BASE_DOMAIN    central host, e.g. previews.example.com (required)
//	SUPABASE_URL         Supabase project URL (required unless --dev)
//	SUPABASE_ANON_KEY    public anon key for password login (auth mode)
//	SUPABASE_SERVICE_KEY server-side key, only for YODEA_STORE=supabase
//	YODEA_STORE          "local" (default file store) or "supabase"
//	YODEA_DEV / --dev    DevNoAuth: per-viewer synthetic tokens, no network
//	YODEA_SECURE_COOKIES "1" forces Secure cookies, "0" disables; default is !dev
//	PORT / YODEA_ADDR   listen address (default 127.0.0.1:8093)
//
// Never ship secrets: keys come from env only. YODEA_DEV must never be set
// in production: it accepts synthetic tokens.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/g-imhoff/yodea/internal/server"
)

func main() {
	err := run(context.Background(), os.Args, os.Getenv, os.Stderr)
	code := exitCode(err)
	if code == 1 {
		log.Printf("yodead: %v", err)
	}
	os.Exit(code)
}

// run starts yodead with args (program name first) and serves until ctx is
// done or SIGINT/SIGTERM arrives. Flag errors and -h usage go to stderr.
// Log lines, the server's included, go to the standard logger.
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	cfg, err := loadConfig(args, getenv, stderr)
	if err != nil {
		return err
	}
	s, err := server.New(cfg)
	if err != nil {
		return err
	}

	// Graceful shutdown: SIGINT/SIGTERM drains in-flight deploys plus
	// db.json writes (10s) before Close stops the JWKS refresh loop.
	// Without this the default SIGTERM kill abandons in-flight writes
	// and Close never runs.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	runErr := make(chan error, 1)
	go func() { runErr <- s.Run() }()

	select {
	case <-ctx.Done():
		stop()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(shutCtx); err != nil {
			log.Printf("yodead: shutdown: %v", err)
		}
		if err := <-runErr; err != nil {
			return err // without Close, as yodead always has on this path
		}
		s.Close()
		return nil
	case err := <-runErr:
		// ListenAndServe exited on its own (bind failure, ...): Run
		// already normalized a Shutdown stop to nil, so any error here
		// is real.
		s.Close()
		return err
	}
}

// exitCode is the exit status for an error from run: 0 for success and -h,
// 2 for other flag errors (as with flag.ExitOnError), else 1. Only the
// errors for 1 still need printing: the flag package printed the others.
func exitCode(err error) int {
	var fe flagError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &fe):
		return 2
	default:
		return 1
	}
}
