package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/g-imhoff/yodea/internal/server"
)

// loadConfig builds the server config from args (program name first) and
// the environment, flag > env > default. Flag errors and -h usage go to
// output.
func loadConfig(args []string, getenv func(string) string, output io.Writer) (server.Config, error) {
	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	var cfg server.Config
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(output)
	// flag.CommandLine's usage text. The FlagSet default matches it except
	// for an empty program name, where it prints "Usage:", not "Usage of :".
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage of %s:\n", fs.Name())
		fs.PrintDefaults()
	}
	fs.BoolVar(&cfg.DevNoAuth, "dev", getenv("YODEA_DEV") == "1", "DevNoAuth mode: synthetic per-viewer tokens, no Supabase (never in production)")
	fs.StringVar(&cfg.Addr, "addr", env("YODEA_ADDR", ""), "listen address")
	fs.StringVar(&cfg.DataDir, "data-dir", env("YODEA_DATA_DIR", ""), "metadata plus sites directory")
	fs.StringVar(&cfg.BaseDomain, "domain", env("YODEA_BASE_DOMAIN", ""), "central host, e.g. previews.example.com")
	fs.StringVar(&cfg.StoreBackend, "store", env("YODEA_STORE", "local"), "metadata backend: local or supabase")
	if err := fs.Parse(args[1:]); err != nil {
		return server.Config{}, flagError{err}
	}

	if cfg.Addr == "" {
		cfg.Addr = listenAddr(getenv("PORT"))
	}
	if cfg.DataDir == "" || cfg.BaseDomain == "" {
		return server.Config{}, errors.New("--data-dir and --domain (or YODEA_DATA_DIR/YODEA_BASE_DOMAIN) are required")
	}
	cfg.SupabaseURL = getenv("SUPABASE_URL")
	cfg.AnonKey = getenv("SUPABASE_ANON_KEY")
	cfg.SupabaseKey = getenv("SUPABASE_SERVICE_KEY")
	cfg.SecureCookies = secureCookies(getenv("YODEA_SECURE_COOKIES"), cfg.DevNoAuth)
	return cfg, nil
}

// flagError is a command-line parse error. The flag package has already
// printed it along with the usage.
type flagError struct{ err error }

func (e flagError) Error() string { return e.err.Error() }
func (e flagError) Unwrap() error { return e.err }

// listenAddr is the listen address when neither --addr nor YODEA_ADDR is
// set: PORT as is when it holds a colon, else PORT on loopback, else
// 127.0.0.1:8093.
func listenAddr(port string) string {
	switch {
	case port == "":
		return "127.0.0.1:8093"
	case strings.Contains(port, ":"):
		return port
	default:
		return "127.0.0.1:" + port
	}
}

// secureCookies reads YODEA_SECURE_COOKIES: 1/true/yes forces Secure
// cookies, 0/false/no disables them, and anything else keeps the default,
// which is !dev.
func secureCookies(v string, dev bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return !dev
}
