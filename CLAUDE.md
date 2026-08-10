# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Self-hosted server monitoring portal that tails Caddy JSON access logs in real-time, stores entries in SQLite, and presents an HTMX dashboard with traffic stats, bot detection, uptime checks and anomaly detection. Also ingests application logs from other services via POST /api/logs, and raises Discord alerts.

**Blocking is Cloudflare's job, not this app's.** Monitor used to push IP and user-agent blocks to Caddy's admin API, with a manual blocklist, auto-block rules and honeypots on top. All of it has been removed — the Caddy sync went first and the rest lingered for a while, recording block decisions that no longer had any effect. Bot *detection* stays: it labels requests and feeds the traffic stats. Do not reintroduce blocking here.

**GitHub:** `https://github.com/exploded/monitor`
**Production:** Linode (Debian), deployed via GitHub Actions on push to `main`.

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

Pure Go HTTP server (no framework), SQLite (modernc.org/sqlite, pure Go/no CGO), HTMX frontend. There is no SSE and no websocket — every live-looking panel is HTMX polling.

### Package Layout

- **`cmd/server/main.go`** — Entry point: config loading, DB init, route registration, watcher/alert/uptime/anomaly startup, hourly maintenance, graceful shutdown.
- **`internal/config`** — Loads `.env` file. Config struct holds all env vars (PORT, DB_PATH, LOG_PATH, AUTH_USER, AUTH_PASS, the retention horizons, LOG_API_KEY, DISCORD_WEBHOOK_URL).
- **`internal/database`** — SQLite setup (WAL mode, MaxOpenConns=1), applies `db/schema.sql` on startup, runs the once-migration ledger, hourly rollup and batched pruning.
- **`internal/watcher`** — Tails Caddy JSON log file, parses entries, detects file rotation (inode change/truncation), batch-writes to DB (100 entries or 2s). Contains `BotMatcher` for case-insensitive UA pattern matching.
- **`internal/handlers`** — HTTP handlers, middleware (basic auth, security headers, request logging), template rendering (clone-per-page pattern with FuncMap helpers).
- **`internal/alerts`** — Alert engine: 30s polled rules plus event-driven `Notify`, per-subject and per-app cooldowns, Discord webhook transport.
- **`internal/uptime`** — HTTP probes of configured targets, with consecutive-failure debounce and recovery events.
- **`internal/anomaly`** — Rate-spike, new-scanner and 5xx anomaly detection over the request table.
- **`internal/reputation`** — Threat score (0-100) per IP from 4xx ratio, bot share and request velocity.
- **`pkg/logship`** — Reusable slog.Handler that batch-ships logs to monitor's POST /api/logs. Used by other projects to send their logs here.
- **`db/queries/`** — SQLC query files. **`db/sqlc/`** — Generated Go code (do not edit).

### Key Routes

Auth-protected (basic auth): `/`, `/bots`, `/security`, `/history`, `/search`, `/uptime`, `/alerts`, `/app-logs`, `/partials/*`
No auth: `/health`, `/static/*`
API key auth (X-API-Key header): `POST /api/logs`

### HTMX Patterns

- Polling, not streaming: traffic overview every 60s; alerts, app logs and anomalies every 30s
- Form submissions with `hx-post` + `hx-target` for bot pattern, alert rule and uptime target CRUD

### Template System

Clone-per-page pattern: base layout (`web/templates/layouts/base.html`) is cloned per page. Fragment templates (`_*.html` in `pages/`) are auto-included. Template FuncMap provides: `formatTime`, `formatDateTime`, `formatDate`, `statusClass`, `truncate`, `humanSize`, `safeHTML`, and math functions.

## SQLC

Config in `sqlc.yaml`. Engine: SQLite. Queries in `db/queries/*.sql`, schema in `db/schema.sql`, generated code in `db/sqlc/`. Always run `sqlc generate` after modifying queries or schema.

- **Keep `db/queries/*.sql` ASCII-only.** sqlc rewrites `sqlc.arg()` into positional parameters using byte offsets, so one multi-byte rune (an em-dash in a comment is the easy mistake) shifts every later edit and silently corrupts a *different* query. The error points at the innocent query, not the comment. `TestQueryFilesAreASCII` guards this. `db/schema.sql` is exempt.
- Deleting a query file does not delete its generated `db/sqlc/*.sql.go` — remove that by hand or the package won't compile.
- Order of operations: edit queries and schema together, run `sqlc generate` once, then fix Go.

## Deployment

Push to `main` triggers GitHub Actions: test → build static Linux binary → SCP to server → stop service → run deploy script → restart.

- Binary: `/var/www/monitor/monitor`
- Service: systemd unit `monitor` (runs as `www-data`)
- Server setup: `curl -fsSL https://raw.githubusercontent.com/exploded/monitor/main/scripts/server-setup.sh | sudo bash`
- Deploy script at `/usr/local/bin/deploy-monitor` stops service, `rm -f` binary (avoids "text file busy"), copies new one, restarts.

## Schema changes

`db/schema.sql` is re-executed on every boot, and everything in it is `CREATE TABLE IF NOT EXISTS` / `INSERT OR IGNORE`. Two consequences:

- **Removing a `CREATE TABLE` from schema.sql does not drop it in production** — the table and its data survive as an orphan. Add an entry to `onceMigrations` in `internal/database/database.go` to actually drop it. The ledger is `schema_migrations`; `isAlreadyApplied` tolerates already-gone columns and indexes, so `DROP ... IF EXISTS` is safe on a fresh DB too.
- **Changing a seed row does not update an existing one.** `INSERT OR IGNORE` skips rows already present, so altering a seeded alert rule's threshold needs a migration as well. Adding a *new* seed row does land on existing databases.

## Alerting

Discord webhook is the only transport. **If `DISCORD_WEBHOOK_URL` is unset, no alert reaches anyone** — the alert log and the "last fired" column still populate, so the UI looks perfectly healthy while nothing is delivered. The engine logs a WARN on every such fire.

- Polled every 30s: `5xx_spike`, `traffic_surge` (count over a window vs threshold), and `app_error`.
- Event-driven via `Notify`: `downtime` from the uptime checker, and the three anomaly types.
- `app_error` is per app: any ERROR from an app alerts on the next tick, with a per-app cooldown so one chatty app cannot spam you or mask another app's first error.
- Cooldowns are keyed per subject (`downtime:<target-id>`, or the app name), not per rule, so two services failing in the same window produce two alerts.
- Downtime needs `failuresBeforeAlert` consecutive failures (2), and every outage gets a matching green recovery notification.
- Alert state (cooldowns, failure streaks) is in memory — a restart can re-alert on an ongoing incident. Deliberate: the alternative is a restart swallowing a real alert.

## Important Notes

- The watcher seeks to EOF on startup — it only processes new log lines, not historical data.
- Bot patterns are pre-seeded in `db/schema.sql` (~22 common bots). They label traffic; they do not block.
- Maintenance runs at startup and hourly: rollup, then prune (never the other way round, or a day can be deleted from raw before it has been aggregated).
- The rollup's catch-up scan is a full table scan and only runs on the startup pass; hourly passes recompute today and yesterday only.
- Rollup date predicates must stay sargable (`ts >= day AND ts < day+1`, never `date(ts) = day`) — the function form defeats `idx_requests_ts` and turns an hourly job into a full scan. Measured at 1.1s vs 140ms on 300k rows.
- `uptime_checks`/`uptime_daily` have foreign keys to `uptime_targets` with no `ON DELETE CASCADE`, so deleting a target means deleting its children first, in a transaction.
