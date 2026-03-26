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
    city        TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS idx_requests_host ON requests(host);
CREATE INDEX IF NOT EXISTS idx_requests_client_ip ON requests(client_ip);
CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(status);
CREATE INDEX IF NOT EXISTS idx_requests_host_ts ON requests(host, ts);

CREATE TABLE IF NOT EXISTS bot_patterns (
    id          INTEGER PRIMARY KEY,
    pattern     TEXT NOT NULL UNIQUE,
    label       TEXT NOT NULL,
    block       INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS blocked_ips (
    id          INTEGER PRIMARY KEY,
    ip          TEXT NOT NULL UNIQUE,
    reason      TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seed common bot patterns
INSERT OR IGNORE INTO bot_patterns (pattern, label, block) VALUES
    ('Googlebot', 'Google', 0),
    ('bingbot', 'Bing', 0),
    ('Baiduspider', 'Baidu', 0),
    ('YandexBot', 'Yandex', 0),
    ('DuckDuckBot', 'DuckDuckGo', 0),
    ('Slurp', 'Yahoo', 0),
    ('facebookexternalhit', 'Facebook', 0),
    ('Twitterbot', 'Twitter', 0),
    ('Applebot', 'Apple', 0),
    ('AhrefsBot', 'Ahrefs', 1),
    ('SemrushBot', 'Semrush', 1),
    ('MJ12bot', 'Majestic', 1),
    ('DotBot', 'Moz', 1),
    ('PetalBot', 'Huawei', 1),
    ('Bytespider', 'ByteDance', 1),
    ('GPTBot', 'OpenAI', 1),
    ('ClaudeBot', 'Anthropic', 1),
    ('CCBot', 'Common Crawl', 1),
    ('Amazonbot', 'Amazon', 1),
    ('SERankingBot', 'SE Ranking', 1),
    ('CensysInspect', 'Censys', 1),
    ('Wget', 'Wget', 0);

CREATE TABLE IF NOT EXISTS autoblock_rules (
    id          INTEGER PRIMARY KEY,
    pattern     TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    hit_count   INTEGER NOT NULL DEFAULT 0,
    last_hit_at DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seed common auto-block path patterns (vulnerability probes)
INSERT OR IGNORE INTO autoblock_rules (pattern, description) VALUES
    ('/wp-login', 'WordPress login page'),
    ('/wp-admin', 'WordPress admin panel'),
    ('/wp-content/', 'WordPress content directory'),
    ('/wp-includes/', 'WordPress includes directory'),
    ('/xmlrpc.php', 'WordPress XML-RPC'),
    ('/wp-cron', 'WordPress cron'),
    ('phpmyadmin', 'phpMyAdmin access attempt'),
    ('/.env', 'Environment file exposure'),
    ('/.git/', 'Git directory exposure'),
    ('/cgi-bin/', 'CGI script probing'),
    ('/.aws/', 'AWS credentials exposure'),
    ('/.ssh/', 'SSH key exposure'),
    ('/config.php', 'PHP config file access'),
    ('/setup.php', 'PHP setup page'),
    ('/install.php', 'PHP install page'),
    ('/eval-stdin.php', 'PHP eval exploit'),
    ('/wp-json/', 'WordPress REST API'),
    ('/telescope/requests', 'Laravel Telescope debug tool');

-- Honeypot paths (fake endpoints that auto-block visiting IPs)
CREATE TABLE IF NOT EXISTS honeypots (
    id          INTEGER PRIMARY KEY,
    path        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    hit_count   INTEGER NOT NULL DEFAULT 0,
    last_hit_at DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO honeypots (path, description) VALUES
    ('/admin/login', 'Fake admin login'),
    ('/api/debug', 'Fake debug endpoint'),
    ('/backup.sql', 'Database backup probe'),
    ('/server-status', 'Apache server-status probe'),
    ('/actuator', 'Spring Boot actuator probe'),
    ('/.DS_Store', 'macOS metadata probe'),
    ('/composer.json', 'PHP Composer config probe'),
    ('/package.json', 'Node.js package probe');

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
    name             TEXT NOT NULL,
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

INSERT OR IGNORE INTO alert_rules (name, type, threshold, window_minutes, cooldown_minutes) VALUES
    ('5xx Spike', '5xx_spike', 5, 5, 15),
    ('Auto-Block', 'auto_block', 1, 1, 5),
    ('Traffic Surge', 'traffic_surge', 500, 5, 30),
    ('App Error', 'app_error', 3, 5, 15);

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

CREATE TABLE IF NOT EXISTS uptime_checks (
    id               INTEGER PRIMARY KEY,
    target_id        INTEGER NOT NULL REFERENCES uptime_targets(id),
    ts               DATETIME NOT NULL,
    status           INTEGER NOT NULL,
    response_time_ms REAL NOT NULL,
    error            TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_uptime_checks_target_ts ON uptime_checks(target_id, ts);

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
