# Monitor

Self-hosted server monitoring portal that tails Caddy JSON access logs, stores parsed entries in SQLite, and presents a live HTMX dashboard with traffic stats, bot detection, and IP/UA blocking.

## Tech Stack

- Go (net/http, html/template)
- HTMX for dynamic UI (polling + SSE)
- SQLite via modernc.org/sqlite (pure Go, no CGO)
- SQLC for type-safe query generation
- Caddy as reverse proxy with JSON structured logs

## Features

- **Real-time log ingestion** — tails Caddy JSON access logs, batch-inserts to SQLite
- **Live dashboard** — auto-refreshing traffic overview (30s polling), SSE live log tail
- **Bot detection** — configurable user agent pattern matching, seeded with common bots
- **IP blocklist** — manual or click-to-block from live log / top IPs table
- **Caddy integration** — pushes blocked IPs and UAs to Caddy admin API at runtime
- **Historical views** — hourly SVG bar chart, daily summary, search/filter
- **HTTP basic auth** — protects the portal
- **Log rotation handling** — detects inode change or truncation, reopens file
- **Automatic pruning** — deletes requests older than configurable retention period

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

Open http://localhost:8484 (default credentials: admin / changeme).

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
| `PORT` | `8484` | HTTP listen port |
| `DB_PATH` | `monitor.db` | SQLite database path |
| `LOG_PATH` | — | Path to Caddy JSON access log (required) |
| `CADDY_ADMIN_URL` | `http://localhost:2019` | Caddy admin API URL |
| `AUTH_USER` | `admin` | Basic auth username |
| `AUTH_PASS` | — | Basic auth password |
| `RETENTION_DAYS` | `90` | Delete requests older than this |

## Project Structure

```
cmd/server/main.go           — entry point, routes, graceful shutdown
internal/
  config/config.go            — .env loading
  database/database.go        — SQLite WAL open, schema, pruning
  watcher/
    watcher.go                — Caddy log tail, parse, batch ingest
    matcher.go                — bot pattern matching
  handlers/
    handler.go                — Handler struct, render, PageData
    templates.go              — clone-per-page template loading
    middleware.go              — basic auth, security headers, logging
    hub.go                    — SSE broadcast hub
    dashboard.go              — dashboard + traffic overview
    logs.go                   — SSE live log stream
    bots.go                   — bot pattern CRUD
    ips.go                    — IP blocklist CRUD
    history.go                — charts, daily summary, search
  caddy/caddy.go              — Caddy admin API client
db/
  schema.sql                  — tables, indexes, seed data
  queries/                    — SQLC query files
  sqlc/                       — generated Go code
web/
  templates/                  — html/template files
  static/                     — CSS, HTMX
```
