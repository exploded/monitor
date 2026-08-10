# Monitor

Self-hosted server monitoring portal that tails Caddy JSON access logs, stores parsed entries in SQLite, and presents an HTMX dashboard with traffic stats, bot detection, uptime checks and Discord alerting.

Blocking is handled by Cloudflare, not by this app. Monitor used to push IP and user-agent blocks to Caddy's admin API; that has been removed, along with the manual blocklist, auto-block rules and honeypots. Bot detection remains, for labelling and stats.

## Tech Stack

- Go (net/http, html/template)
- HTMX for dynamic UI (polling)
- SQLite via modernc.org/sqlite (pure Go, no CGO)
- SQLC for type-safe query generation
- Caddy as reverse proxy with JSON structured logs

## Features

- **Real-time log ingestion** — tails Caddy JSON access logs, batch-inserts to SQLite
- **Dashboard** — auto-refreshing traffic overview, per-host sparklines, IP threat scores
- **Bot detection** — configurable user agent pattern matching, seeded with common bots
- **Uptime checks** — HTTP probes per target, with alerts on outage and on recovery
- **App log ingestion** — sibling apps ship WARN+ here via `pkg/logship`
- **Discord alerts** — 5xx spikes, traffic surges, per-app errors, downtime, anomalies
- **Historical views** — hourly SVG bar chart, daily summary, search/filter, CSV export
- **HTTP basic auth** — protects the portal
- **Log rotation handling** — detects inode change or truncation, reopens file
- **Automatic pruning** — batched deletes on per-table retention horizons, with daily rollups preserving long-run history

## Local Development

### Prerequisites

- Go 1.26+
- [sqlc](https://sqlc.dev/) (`go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`)

### Setup

```bash
cp .env.example .env
# Edit .env — set LOG_PATH to a Caddy JSON log file or testdata/sample-access.log

sqlc generate
go build -o monitor.exe ./cmd/server/
./monitor.exe
```

Open http://localhost:8989 (default credentials: admin / changeme).

On Windows, use `build.bat` which runs sqlc generate, builds, and starts the server.

### Testing with sample data

The `testdata/sample-access.log` file contains sample Caddy JSON entries. The watcher seeks to end on startup, so append new lines to see them ingested:

```bash
echo '{"level":"info","ts":1711234599.0,"logger":"http.log.access","msg":"handled request","request":{"remote_ip":"1.2.3.4","remote_port":"1234","client_ip":"1.2.3.4","proto":"HTTP/2.0","method":"GET","host":"example.com","uri":"/test","headers":{"User-Agent":["Mozilla/5.0"]}},"duration":0.001,"size":100,"status":200,"resp_headers":{}}' >> testdata/sample-access.log
```

## Debian/Linode Deployment

### 1. Install Caddy

```bash
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install caddy
```

### 2. Configure Caddy with JSON logging

Add JSON logging to your Caddyfile for each virtual host:

```
example.com {
    root * /var/www/example
    file_server

    log {
        output file /var/log/caddy/access.log {
            roll_size 100mb
            roll_keep 5
        }
        format json
    }
}
```

Reload Caddy: `sudo systemctl reload caddy`

### 3. Run server setup

```bash
curl -fsSL https://raw.githubusercontent.com/exploded/monitor/master/scripts/server-setup.sh | sudo bash
```

This creates:
- `deploy` user with SSH key for GitHub Actions
- `/var/www/monitor/` directory owned by `www-data`
- `.env` file (edit the `AUTH_PASS`!)
- systemd service (`monitor.service`)
- deploy script (`/usr/local/bin/deploy-monitor`)
- sudoers rules for passwordless deploy

### 4. Edit the .env file

```bash
sudo nano /var/www/monitor/.env
```

Set `AUTH_PASS` to a strong password and verify `LOG_PATH` points to your Caddy access log.

### 5. Add GitHub Actions secrets

Follow the instructions printed by the setup script. Add these secrets to your GitHub repository:

| Secret | Value |
|--------|-------|
| `DEPLOY_HOST` | Your server's public IP |
| `DEPLOY_USER` | `deploy` |
| `DEPLOY_SSH_KEY` | The private key printed by setup |
| `DEPLOY_PORT` | SSH port (optional, default 22) |

### 6. Deploy

Push to `master` to trigger the GitHub Actions workflow. It will:

1. Run `go test`
2. Build a static Linux binary (`CGO_ENABLED=0`)
3. SCP the binary + web assets to the server
4. Run the deploy script (stop, swap binary, start, verify)

### Manual deploy

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o monitor ./cmd/server/
scp monitor web/ db/schema.sql deploy@your-server:/tmp/monitor-deploy/
ssh deploy@your-server 'sudo /usr/local/bin/deploy-monitor /tmp/monitor-deploy'
```

## Configuration

All configuration via environment variables (or `.env` file):

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8989` | HTTP listen port |
| `DB_PATH` | `monitor.db` | SQLite database path |
| `LOG_PATH` | — | Path to Caddy JSON access log (required) |
| `AUTH_USER` | `admin` | Basic auth username |
| `AUTH_PASS` | — | Basic auth password |
| `LOG_API_KEY` | — | Key sibling apps send as `X-API-Key` to `POST /api/logs`. Unset means the endpoint returns 503 |
| `DISCORD_WEBHOOK_URL` | — | **The only alert transport.** Unset means no alert is delivered anywhere, while the alert log and "last fired" column still populate — so the UI looks healthy. Set this |
| `RETENTION_DAYS` | `30` | Raw `requests` horizon |
| `UPTIME_RETENTION_DAYS` | `14` | Raw `uptime_checks` horizon |
| `APP_LOG_RETENTION_DAYS` | `90` | `app_logs` at ERROR |
| `APP_LOG_NOISE_RETENTION_DAYS` | `14` | `app_logs` below ERROR |
| `ANOMALY_RETENTION_DAYS` | `90` | `anomalies` horizon |
| `ALERT_LOG_RETENTION_DAYS` | `180` | `alert_log` horizon |
| `GEOIP_DB_PATH` | `/var/lib/GeoIP/GeoLite2-City.mmdb` | MaxMind GeoLite2-City database |
| `IGNORE_HOSTS` | — | Comma-separated hosts to skip |
| `IGNORE_USER_AGENTS` | — | Extra UA substrings to skip. Monitor's own probes are always skipped; never add `Go-http-client` |

## Alerting

Alerts go to a Discord webhook, and nowhere else. **Until `DISCORD_WEBHOOK_URL` is set,
nothing is delivered** — every alert still writes an `alert_log` row and updates the
rule's "last fired" timestamp, so `/alerts/dashboard` looks like a working system while
no notification has ever left the box. The engine logs a WARN on each such fire.

To set it up: Discord → Server Settings → Integrations → Webhooks → New Webhook → copy
the URL, then add `DISCORD_WEBHOOK_URL=...` to `/var/www/monitor/.env` and restart.

What fires:

| Alert | Trigger | Default |
|-------|---------|---------|
| `app_error` | Any ERROR shipped by an app, **per app** | 1 error, 30 min cooldown per app |
| `downtime` | An uptime target failing 2 consecutive probes | 15 min cooldown per target |
| `5xx_spike` | 5xx responses in the Caddy log over a window | 5 in 5 min |
| `traffic_surge` | Total requests over a window | 500 in 5 min |
| `rate_spike`, `new_scanner`, `5xx_anomaly` | Anomaly detector | 60 min cooldown |

Every outage also produces a green recovery notification when the target comes back.
Cooldowns are per subject, not per rule, so two services failing together produce two
alerts rather than one — and a noisy app cannot mask a quiet one's first error.

Sibling apps ship their logs here with `pkg/logship`, gated on `MONITOR_URL` and
`MONITOR_API_KEY`; they send WARN and above, and only ERROR raises an alert.

## Data Retention

Raw events expire quickly because nothing in the UI reads them for long — every
range selector caps at 7 days, and the only 30-day readers are the daily summary
and the host list. Long-run history lives in two rollup tables instead.

| Table | Raw | Kept as |
|---|---|---|
| `requests` | 30 days | `daily_stats`, ~13 months |
| `uptime_checks` | 14 days | `uptime_daily`, ~13 months |
| `app_logs` | 90d ERROR / 14d below | — |
| `anomalies` | 90 days | — |
| `alert_log` | 180 days | — |

The rollup runs immediately before each prune, so a day is always aggregated
before its raw rows can be deleted. The first run backfills every day present.
`/search` and `/export/search` are floored to the raw horizon — an earlier
`?from=` would otherwise scan the whole table to return a silently truncated
result.

Pruning deletes in batches of 5,000 with a short pause between them. The pool is
capped at a single connection, so an unbounded `DELETE` would block every
dashboard read and every watcher insert for its duration.

### Reclaiming disk space

Deletes free pages for reuse but **do not shrink the file** — after a one-off
retention reduction the file stays its old size with the freed space on the free
list. Reclaim it once, then leave it alone; at a fixed horizon the file
plateaus by itself and a scheduled `VACUUM` buys nothing.

```bash
sqlite3 /var/www/monitor/monitor.db "VACUUM INTO '/var/www/monitor/monitor-compact.db';"
sudo systemctl stop monitor
sudo -u www-data mv /var/www/monitor/monitor-compact.db /var/www/monitor/monitor.db
sudo systemctl start monitor
```

`VACUUM INTO` runs under a read transaction, so it does not take the exclusive
lock a plain `VACUUM` does and does not need 2× space in the same file. Downtime
is just the swap.

### GeoIP database

`GeoLite2-City.mmdb` lives at `/var/lib/GeoIP/` — outside the app directory,
which the deploy script `chown -R`s and overwrites. It is **not** shipped by
`scripts/deploy-monitor`; place it by hand. It is memory-mapped, so it costs no
heap and adds no startup time, but it also means **you must not overwrite it in
place** — download alongside and `mv` (an atomic rename), then restart:

```bash
sudo mv ~/GeoLite2-City.mmdb.new /var/lib/GeoIP/GeoLite2-City.mmdb
sudo systemctl restart monitor
```

Only the country code is read; MaxMind publishes updates twice weekly and there
is no automatic refresh. Without the file, monitor degrades gracefully — the
country panel shows its empty state and everything else works.

## Project Structure

```
cmd/server/main.go           — entry point, routes, graceful shutdown
internal/
  config/config.go            — .env loading
  database/
    database.go               — SQLite WAL open, schema, migrations, pruning
    rollup.go                 — daily aggregate refresh and backfill
  watcher/
    watcher.go                — Caddy log tail, parse, batch ingest
    matcher.go                — bot pattern matching
  handlers/
    handler.go                — Handler struct, render, PageData
    templates.go              — clone-per-page template loading
    middleware.go             — basic auth, security headers, logging
    dashboard.go              — dashboard + traffic overview
    bots.go                   — bot pattern CRUD
    uptime.go                 — uptime target CRUD and detail
    alerts.go                 — alert rule CRUD
    applogs.go                — POST /api/logs ingest, app error panel
    history.go                — charts, daily summary, search
  alerts/                     — alert engine + Discord transport
  uptime/uptime.go            — HTTP probes, failure debounce, recovery
  anomaly/detector.go         — rate spikes, new scanners, 5xx anomalies
  reputation/reputation.go    — per-IP threat score
db/
  schema.sql                  — tables, indexes, seed data
  queries/                    — SQLC query files
  sqlc/                       — generated Go code
web/
  templates/                  — html/template files
  static/                     — CSS, HTMX
```
