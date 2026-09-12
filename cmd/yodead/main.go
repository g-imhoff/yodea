// Command yodead is the yodea backend: central dashboard plus login,
// session API, site deploys, preview subdomains, views, and favorites.
//
// Configuration is env-first with flag overrides:
//
//	YODEA_DATA_DIR       metadata plus sites dir (required)
//	YODEA_BASE_DOMAIN    central host, e.g. previews.example.com (required)
//	SUPABASE_URL         Supabase project URL (required unless --dev)
//	SUPABASE_ANON_KEY    public anon key for password login (auth mode)
//	SUPABASE_SERVICE_KEY server-side key, only for YODEA_STORE=supabase
//	YODEA_STORE          "local" (default file store) or "supabase"
//	YODEA_DEV / --dev    DevNoAuth: per-viewer synthetic tokens, no network
//	PORT / YODEA_ADDR   listen address (default 127.0.0.1:8093)
//
// Never ship secrets: keys come from env only. YODEA_DEV must never be set
// in production: it accepts synthetic tokens.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/g-imhoff/yodea/internal/server"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	dev := flag.Bool("dev", os.Getenv("YODEA_DEV") == "1", "DevNoAuth mode: synthetic per-viewer tokens, no Supabase (never in production)")
	addr := flag.String("addr", env("YODEA_ADDR", ""), "listen address")
	dataDir := flag.String("data-dir", env("YODEA_DATA_DIR", ""), "metadata plus sites directory")
	domain := flag.String("domain", env("YODEA_BASE_DOMAIN", ""), "central host, e.g. previews.example.com")
	storeBackend := flag.String("store", env("YODEA_STORE", "local"), "metadata backend: local or supabase")
	flag.Parse()

	if *addr == "" {
		*addr = "127.0.0.1:8093"
		if p := os.Getenv("PORT"); p != "" {
			if strings.Contains(p, ":") {
				*addr = p
			} else {
				*addr = "127.0.0.1:" + p
			}
		}
	}
	if *dataDir == "" || *domain == "" {
		log.Fatal("yodead: --data-dir and --domain (or YODEA_DATA_DIR/YODEA_BASE_DOMAIN) are required")
	}
	s, err := server.New(server.Config{
		Addr:          *addr,
		DataDir:       *dataDir,
		BaseDomain:    *domain,
		SupabaseURL:   os.Getenv("SUPABASE_URL"),
		AnonKey:       os.Getenv("SUPABASE_ANON_KEY"),
		SupabaseKey:   os.Getenv("SUPABASE_SERVICE_KEY"),
		StoreBackend:  *storeBackend,
		DevNoAuth:     *dev,
		SecureCookies: !*dev, // dev serves plain http on loopback
	})
	if err != nil {
		log.Fatalf("yodead: %v", err)
	}

	// Graceful shutdown: SIGINT/SIGTERM drains in-flight deploys plus
	// db.json writes (10s) before Close stops the JWKS refresh loop.
	// Without this the default SIGTERM kill abandons in-flight writes
	// and Close never runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
			log.Fatalf("yodead: %v", err)
		}
		s.Close()
	case err := <-runErr:
		// ListenAndServe exited on its own (bind failure, ...): Run
		// already normalized a Shutdown stop to nil, so any error here
		// is real.
		s.Close()
		if err != nil {
			log.Fatalf("yodead: %v", err)
		}
	}
}
