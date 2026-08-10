CREATE TABLE IF NOT EXISTS requests (
    id          INTEGER PRIMARY KEY,
    ts          DATETIME NOT NULL,
    host        TEXT NOT NULL,
    client_ip   TEXT NOT NULL,
    method      TEXT NOT NULL,
    uri         TEXT NOT NULL,
    status      INTEGER NOT NULL,
    size        INTEGER NOT NULL,
    user_agent  TEXT NOT NULL,
    duration_ms REAL NOT NULL,
    is_bot      INTEGER NOT NULL DEFAULT 0,
    country     TEXT NOT NULL DEFAULT '',
    referer     TEXT NOT NULL DEFAULT ''
);

-- Deliberately only three indexes. Dropped previously:
--   idx_requests_host    — strict prefix of idx_requests_host_ts
--   idx_requests_status  — a handful of distinct values, and unusable for the
--                          status/100 class filter in SearchRequests
--   idx_requests_country — low cardinality; TopCountriesSince filters ts first
-- Together they cost ~26 MB while serving no query. See migrations in
-- database.go, which drop them on existing databases.
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS idx_requests_client_ip ON requests(client_ip);
CREATE INDEX IF NOT EXISTS idx_requests_host_ts ON requests(host, ts);

-- Bot patterns drive detection only (the is_bot label on requests, and the bot
-- share of traffic stats). Blocking is Cloudflare's job — monitor pushed IP and
-- UA blocks to Caddy's admin API until that was retired.
CREATE TABLE IF NOT EXISTS bot_patterns (
    id          INTEGER PRIMARY KEY,
    pattern     TEXT NOT NULL UNIQUE,
    label       TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seed common bot patterns
INSERT OR IGNORE INTO bot_patterns (pattern, label) VALUES
    ('Googlebot', 'Google'),
    ('bingbot', 'Bing'),
    ('Baiduspider', 'Baidu'),
    ('YandexBot', 'Yandex'),
    ('DuckDuckBot', 'DuckDuckGo'),
    ('Slurp', 'Yahoo'),
    ('facebookexternalhit', 'Facebook'),
    ('Twitterbot', 'Twitter'),
    ('Applebot', 'Apple'),
    ('AhrefsBot', 'Ahrefs'),
    ('SemrushBot', 'Semrush'),
    ('MJ12bot', 'Majestic'),
    ('DotBot', 'Moz'),
    ('PetalBot', 'Huawei'),
    ('Bytespider', 'ByteDance'),
    ('GPTBot', 'OpenAI'),
    ('ClaudeBot', 'Anthropic'),
    ('CCBot', 'Common Crawl'),
    ('Amazonbot', 'Amazon'),
    ('SERankingBot', 'SE Ranking'),
    ('CensysInspect', 'Censys'),
    ('Wget', 'Wget');

CREATE TABLE IF NOT EXISTS app_logs (
    id          INTEGER PRIMARY KEY,
    ts          DATETIME NOT NULL,
    app         TEXT NOT NULL,
    level       TEXT NOT NULL,
    message     TEXT NOT NULL,
    attrs       TEXT NOT NULL DEFAULT '{}',
    source      TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_app_logs_ts ON app_logs(ts);
CREATE INDEX IF NOT EXISTS idx_app_logs_app ON app_logs(app);
CREATE INDEX IF NOT EXISTS idx_app_logs_level ON app_logs(level);

-- Alert rules and log
CREATE TABLE IF NOT EXISTS alert_rules (
    id               INTEGER PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    type             TEXT NOT NULL,
    enabled          INTEGER NOT NULL DEFAULT 1,
    threshold        INTEGER NOT NULL DEFAULT 0,
    window_minutes   INTEGER NOT NULL DEFAULT 5,
    cooldown_minutes INTEGER NOT NULL DEFAULT 15,
    last_fired_at    DATETIME,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS alert_log (
    id         INTEGER PRIMARY KEY,
    rule_id    INTEGER NOT NULL REFERENCES alert_rules(id),
    type       TEXT NOT NULL,
    message    TEXT NOT NULL,
    details    TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_alert_log_created ON alert_log(created_at);

-- The last three match the types the anomaly detector emits. Without them,
-- Notify found no rule and silently no-opped while the anomaly row was still
-- written — detections existed in the UI but never alerted.
INSERT OR IGNORE INTO alert_rules (name, type, threshold, window_minutes, cooldown_minutes) VALUES
    ('5xx Spike', '5xx_spike', 5, 5, 15),
    ('Traffic Surge', 'traffic_surge', 500, 5, 30),
    ('App Error', 'app_error', 1, 5, 30),
    ('Downtime', 'downtime', 1, 1, 15),
    ('Rate Spike', 'rate_spike', 1, 5, 60),
    ('New Scanner', 'new_scanner', 1, 10, 60),
    ('5xx Anomaly', '5xx_anomaly', 1, 60, 60);

-- Uptime monitoring
CREATE TABLE IF NOT EXISTS uptime_targets (
    id               INTEGER PRIMARY KEY,
    name             TEXT NOT NULL,
    url              TEXT NOT NULL UNIQUE,
    interval_seconds INTEGER NOT NULL DEFAULT 60,
    expected_status  INTEGER NOT NULL DEFAULT 200,
    enabled          INTEGER NOT NULL DEFAULT 1,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- No created_at: it duplicated ts on every row (~20 bytes each) and was read by
-- nothing. This is the highest-volume table in the database.
CREATE TABLE IF NOT EXISTS uptime_checks (
    id               INTEGER PRIMARY KEY,
    target_id        INTEGER NOT NULL REFERENCES uptime_targets(id),
    ts               DATETIME NOT NULL,
    status           INTEGER NOT NULL,
    response_time_ms REAL NOT NULL,
    error            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_uptime_checks_target_ts ON uptime_checks(target_id, ts);

-- Daily rollup of uptime_checks, so raw checks can expire at 14 days while
-- long-run availability history survives. ~10 rows/day against ~5,900 raw.
CREATE TABLE IF NOT EXISTS uptime_daily (
    target_id      INTEGER NOT NULL REFERENCES uptime_targets(id),
    day            TEXT NOT NULL,
    checks         INTEGER NOT NULL,
    up_count       INTEGER NOT NULL,
    avg_response_ms REAL NOT NULL,
    max_response_ms REAL NOT NULL,
    PRIMARY KEY (target_id, day)
);

-- Daily rollup of requests. Serves DailySummary directly (a six-aggregate 30-day
-- scan that ran on every /history load) and keeps history past the 30-day raw
-- horizon. unique_ips is stored per-day because COUNT(DISTINCT) does not
-- re-aggregate: weekly/monthly distinct-IP totals cannot be derived from it.
CREATE TABLE IF NOT EXISTS daily_stats (
    day          TEXT PRIMARY KEY,
    total        INTEGER NOT NULL,
    bots         INTEGER NOT NULL,
    unique_ips   INTEGER NOT NULL,
    errors       INTEGER NOT NULL,
    avg_duration REAL NOT NULL,
    bytes        INTEGER NOT NULL
);

-- Applied-migration ledger. Without this the timestamp normalisation below ran
-- on every boot, full-scanning the largest table each time.
CREATE TABLE IF NOT EXISTS schema_migrations (
    name       TEXT PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Anomaly detection
CREATE TABLE IF NOT EXISTS anomalies (
    id           INTEGER PRIMARY KEY,
    ts           DATETIME NOT NULL,
    type         TEXT NOT NULL,
    client_ip    TEXT NOT NULL DEFAULT '',
    host         TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL,
    score        REAL NOT NULL DEFAULT 0,
    acknowledged INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_anomalies_ts ON anomalies(ts);
