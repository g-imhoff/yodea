# yodea

Tiny private preview host: Go server `yodead` plus Go CLI `yodea`.
Supabase invite-only auth, per-preview subdomains (`<user>-<project>.previews.example.com`).

## Install the CLI

Requires Go 1.27 or newer:

```sh
go install github.com/g-imhoff/yodea/cmd/yodea@latest
```

> Publishing note: this resolves against the public `github.com/g-imhoff/yodea`
> repo, so push the current tree there (a version tag like `v0.1.0` gives
> users a stable release). Until the published tree contains `cmd/yodea`,
> build from a checkout: `go build -o yodea ./cmd/yodea`.

## Quick start

```sh
# Log in (password comes from $YODEA_PASSWORD or a hidden prompt)
yodea login --email you@example.com

# Scaffold a fresh Vite React TS app, or link an existing folder as-is.
# Flags go before the project name.
yodea init --dir ./my-app my-app
yodea init --dir ./existing --link existing-name

# Deploy (needs a static dist/ with index.html; React TS only)
yodea push --dir ./my-app

yodea list
yodea delete my-app
```

`--server URL` overrides the production default `https://previews.example.com`.
Deploys upload `dist/` as a gzipped tarball (30MB cap, no dotfiles,
no symlinks, `index.html` required).

## Server

`yodead` serves the dashboard, login, session API, and preview subdomains.
It runs in Docker behind Caddy, which terminates TLS and provisions one
certificate per preview after asking yodead (`GET /api/caddy-ask`) whether
the hostname is a live preview. No proxy edits per deploy.

```sh
yodead --dev --addr 127.0.0.1:8093 --data-dir ./data --domain previews.example.test
```

`--dev` uses synthetic per-viewer tokens and the local file store; never use
it in production. Production uses `YODEA_STORE=supabase` with `SUPABASE_URL`
plus a server-side key, schema in `supabase/migrations/0001_yodea_mvp.sql`.

## Layout

- `cmd/yodea` — the CLI (`login init push list delete`)
- `cmd/yodead` — the server
- `internal/server` — HTTP backend (dashboard, previews, views, favorites)
- `internal/client` — CLI backend (session, deploy packing, React TS checks)
- `internal/sites` — archive validation and preview labels
- `internal/store` — local file store plus Supabase PostgREST store
- `internal/auth` — Supabase JWT verification plus dev tokens
- `webui` — React Vite TS dashboard (built to `internal/server/web/dist`)
- `supabase/migrations` — metadata schema plus RLS policies
