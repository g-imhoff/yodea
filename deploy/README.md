# yodead deployment notes

Samples: `Dockerfile.yodead` (runtime image) and `Caddyfile.sample`
(apex plus wildcard on-demand blocks). What follows documents the
container and Caddy wiring.

## DataDir layout

`yodead --data-dir <dir>` (or `YODEA_DATA_DIR`) keeps everything under one
directory (`store.Open` loads or creates it; `sites.ExtractDist` unpacks
deploys into it):

```
<data-dir>/
  db.json            # local metadata store: sites, per-viewer views, favorites
  db.json.bak        # previous good copy, rotated before each save; fallback when primary is missing, empty, or corrupt
  db.json.tmp        # transient, during atomic save-rename only
  yodead.lock        # single-writer flock file (FD held for the Store lifetime, PID inside)
  sites/
    <label>/         # one dir per preview label, static dist files
      index.html     # required; missing assets stay 404, extensionless
                     # routes fall back to index.html
    .stage-*/        # transient staging, one per deploy; crashed leftovers removed on startup
    .old-*/          # transient backup during atomic swap; crashed leftovers removed on startup
```

Details from the code: the directory is created `0755`; `db.json` is written
`0600` via atomic write-to-`db.json.tmp`-then-rename (`internal/store/store.go`);
the previous good primary is rotated to `db.json.bak` (only when it parses,
so a corrupt primary never destroys a good backup) and `Open` boots from the
backup when the primary is missing, empty, or corrupt (error only when both
are corrupt). Site files land `0644` and directories `0755` with no exec bits
(`internal/sites`). Deploys extract to a unique `sites/.stage-*` dir and swap
it live via `ReplaceSite`; the previous live dir moves aside to a unique
`sites/.old-*` backup between the two renames. `server.New` calls
`sites.CleanupLeftovers` on startup to remove crashed `.stage-*` and `.old-*`
leftovers (only dot-prefixed staging/backup names, never the live dest).
Only the local store uses `db.json`; with
`YODEA_STORE=supabase` metadata lives in PostgREST and the DataDir still
holds `sites/<label>/`.

## Container run command

Observed config: image `debian:stable-slim`,
entrypoint `/yodead`, restart policy `unless-stopped`, no published ports
(Caddy is the only public entry), container network shared with Caddy so the
name `yodead` resolves to port `8093`:

```sh
# Build the image (binary first, then image; base is locally cached).
CGO_ENABLED=0 go build -o deploy/yodead ./cmd/yodead
docker build -f deploy/Dockerfile.yodead -t yodead .

# Run it (adjust host paths and network).
docker run -d --name yodead \
  --network <caddy-network> \
  --restart unless-stopped \
  -v <host-data>:/data \
  yodead \
  --addr 0.0.0.0:8093 --data-dir /data --domain previews.example.com
```

A dev container instead bind-mounts a host-built binary read-only
(`<host-binary>:/yodead:ro`) and a host DataDir (`<host-data>:/data`) on the
same base image with the same flags plus `--dev`. `--dev` selects synthetic
per-viewer tokens and the local file store; never use it in production.
Production adds the Supabase env (`YODEA_STORE=supabase`, `SUPABASE_URL` plus
a server-side key); secrets come from env only, never flags.

## Caddy reload procedure

The Caddyfile is bind-mounted read-only into the caddy container at
`/etc/caddy/Caddyfile`. After editing the host file, validate first, then
reload:

```sh
docker exec <caddy-container> caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker exec <caddy-container> caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
```

Reload only on a passing validate. Deploys never need this: Caddy asks
yodead per hostname (below), so no proxy edits per preview.

## Certificate behavior

On-demand TLS: the global `on_demand_tls { ask ... }` stanza plus
`tls { on_demand }` on the `*.previews.example.com` block mean Caddy, on the first
TLS handshake for a preview hostname, calls
`GET http://yodead:8093/api/caddy-ask?domain=<host>` over the container
network. `yodead` answers `200` only when the host is
`<label>.previews.example.com` with an existing site for `<label>`
(`handleCaddyAsk` in `internal/server/server.go`); anything else gets `404`
and no certificate is issued. So each preview gets its own certificate on
first visit, and only live previews can get one.

## Single-writer rule for the local store

The local store is one JSON file guarded by an in-process mutex plus an
exclusive non-blocking flock on `<data-dir>/yodead.lock` (FD held for the
Store lifetime; `Server.Close` calls `Store.Close` to release it) with
atomic save-rename and backup rotation to `db.json.bak`. Run exactly one
`yodead` writer per DataDir: a second process opening the same dir gets a
single-writer refusal, and sequential in-process restarts must `Close`
before reopening. Crashed deploy leftovers (`sites/.stage-*`, `sites/.old-*`)
are removed on startup by `sites.CleanupLeftovers` (never the live dest).
The multi-writer path is `YODEA_STORE=supabase`, where PostgREST plus RLS own
concurrency (schema in `supabase/migrations/0001_yodea_mvp.sql`).
