# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Self-hosted server monitoring portal that tails Caddy JSON access logs in real-time, stores entries in SQLite, and presents a live HTMX dashboard with traffic stats, bot detection, and IP/UA blocking via Caddy's admin API. Also ingests application logs from other services via POST /api/logs.

**GitHub:** `https://github.com/exploded/monitor`
**Production:** Linode (Debian), deployed via GitHub Actions on push to `master`.

## Build & Dev Commands

```bash
# Generate SQLC code (required before building if queries changed)
sqlc generate

# Build for local dev (Windows)
go build -o monitor.exe ./cmd/server/

# Or use build.bat which runs sqlc generate + build + loads .env + starts server
build.bat

# Build for production (Linux static binary — required for Linode)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o monitor ./cmd/server/

# Run tests
go test -v ./...
```

Requires a `.env` file (copy `.env.example`). Key vars: `LOG_PATH` (Caddy log), `AUTH_PASS`, `LOG_API_KEY`.

## Architecture

Pure Go HTTP server (no framework), SQLite (modernc.org/sqlite, pure Go/no CGO), HTMX frontend with SSE for live streaming.

### Package Layout

- **`cmd/server/main.go`** — Entry point: config loading, DB init, route registration, watcher startup, graceful shutdown.
- **`internal/config`** — Loads `.env` file. Config struct holds all env vars (PORT, DB_PATH, LOG_PATH, CADDY_ADMIN_URL, AUTH_USER, AUTH_PASS, RETENTION_DAYS, LOG_API_KEY).
- **`internal/database`** — SQLite setup (WAL mode, MaxOpenConns=1), applies `db/schema.sql` on startup, hourly pruning of old records.
- **`internal/watcher`** — Tails Caddy JSON log file, parses entries, detects file rotation (inode change/truncation), batch-writes to DB (100 entries or 500ms). Contains `BotMatcher` for case-insensitive UA pattern matching.
- **`internal/handlers`** — HTTP handlers, middleware (basic auth, security headers, request logging), SSE hub (fan-out broadcast), template rendering (clone-per-page pattern with FuncMap helpers).
- **`internal/caddy`** — Caddy admin API client. Syncs blocked IPs to `routes/0` (remote_ip matcher) and blocked UAs to `routes/1` (header_regexp matcher) via PATCH/DELETE.
- **`pkg/logship`** — Reusable slog.Handler that batch-ships logs to monitor's POST /api/logs. Used by other projects to send their logs here.
- **`db/queries/`** — SQLC query files. **`db/sqlc/`** — Generated Go code (do not edit).

### Key Routes

Auth-protected (basic auth): `/`, `/stream`, `/bots`, `/ips`, `/history`, `/search`, `/partials/*`
No auth: `/health`, `/static/*`
API key auth (X-API-Key header): `POST /api/logs`

### HTMX Patterns

- SSE live log streaming via `/stream` endpoint and `hx-ext="sse"`
- 30-second polling on traffic overview via `hx-trigger="every 30s"`
- Form submissions with `hx-post` + `hx-target` for bot/IP CRUD

### Template System

Clone-per-page pattern: base layout (`web/templates/layouts/base.html`) is cloned per page. Fragment templates (`_*.html` in `pages/`) are auto-included. Template FuncMap provides: `formatTime`, `formatDateTime`, `formatDate`, `statusClass`, `truncate`, `humanSize`, `safeHTML`, and math functions.

## SQLC

Config in `sqlc.yaml`. Engine: SQLite. Queries in `db/queries/*.sql`, schema in `db/schema.sql`, generated code in `db/sqlc/`. Always run `sqlc generate` after modifying queries or schema.

## Deployment

Push to `master` triggers GitHub Actions: test → build static Linux binary → SCP to server → stop service → run deploy script → restart.

- Binary: `/var/www/monitor/monitor`
- Service: systemd unit `monitor` (runs as `www-data`)
- Server setup: `curl -fsSL https://raw.githubusercontent.com/exploded/monitor/master/scripts/server-setup.sh | sudo bash`
- Deploy script at `/usr/local/bin/deploy-monitor` stops service, `rm -f` binary (avoids "text file busy"), copies new one, restarts.

## Important Notes

- The watcher seeks to EOF on startup — it only processes new log lines, not historical data.
- `StatusWriter` in middleware implements `http.Flusher` for SSE to work through the middleware chain.
- Caddy admin API routes are positional: route 0 = IP blocks, route 1 = UA blocks. If the block list is empty, the route is DELETEd rather than PATCHed.
- Bot patterns are pre-seeded in `db/schema.sql` (~22 common bots with block flags).
- Data pruning runs hourly, deleting requests and app_logs older than RETENTION_DAYS (default 90).
