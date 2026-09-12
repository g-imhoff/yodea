# yodead deployment notes (mirrors the production server setup)

Samples: `Dockerfile.yodead` (runtime image) and `Caddyfile.sample`
(apex plus wildcard on-demand blocks). What follows documents the live
container and Caddy wiring as observed via read-only `docker inspect yodead`
and the Caddyfile's yodea blocks.

## DataDir layout

`yodead --data-dir <dir>` (or `YODEA_DATA_DIR`) keeps everything under one
directory (`store.Open` loads or creates it; `sites.ExtractDist` unpacks
deploys into it):

```
<data-dir>/
  db.json            # local metadata store: sites, per-viewer views, favorites
  db.json.tmp        # transient, during atomic save-rename only
  sites/
    <label>/         # one dir per preview label, static dist files
      index.html     # required; missing assets stay 404, extensionless
                     # routes fall back to index.html
```

Details from the code: the directory is created `0755`; `db.json` is written
`0600` via atomic write-to-`db.json.tmp`-then-rename (`internal/store/store.go`);
site files land `0644` and directories `0755` with no exec bits
(`internal/sites`). Only the local store uses `db.json`; with
`YODEA_STORE=supabase` metadata lives in PostgREST and the DataDir still
holds `sites/<label>/`.

## Container run command

Observed live config (`docker inspect yodead`): image `debian:stable-slim`,
entrypoint `/yodead`, restart policy `unless-stopped`, no published ports
(Caddy is the only public entry), container network shared with Caddy so the
name `yodead` resolves to port `8093`:

```sh
# Build the image (binary first, then image; base is locally cached).
CGO_ENABLED=0 go build -o deploy/yodead ./cmd/yodead
docker build -f deploy/Dockerfile.yodead -t yodead .

# Run it the way the container runs (adjust host paths and network).
docker run -d --name yodead \
  --network <caddy-network> \
  --restart unless-stopped \
  -v /var/lib/yodead/data:/data \
  yodead \
  --addr 0.0.0.0:8093 --data-dir /data --domain previews.example.com
```

The live test container instead bind-mounts a host-built binary read-only
(`<host-binary>:/yodead:ro`) and a host DataDir (`<host-data>:/data`) on the
same base image with the same flags plus `--dev`. `--dev` selects synthetic
per-viewer tokens and the local file store; never use it in production.
Production adds the Supabase env (`YODEA_STORE=supabase`, `SUPABASE_URL` plus
a server-side key); secrets come from env only, never flags.

## Caddy reload procedure

The Caddyfile is bind-mounted read-only into the caddy container at
`/etc/caddy/Caddyfile`. After editing the host file, validate first, then
reload (container name varies; it is `sentry-self-hosted-caddy-1`):

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

The local store is one JSON file guarded by a single process mutex with
atomic save-rename. Run exactly one `yodead` writer per DataDir: two
processes sharing one `db.json` will lose each other's updates. The
multi-writer path is `YODEA_STORE=supabase`, where PostgREST plus RLS own
concurrency (schema in `supabase/migrations/0001_yodea_mvp.sql`).
