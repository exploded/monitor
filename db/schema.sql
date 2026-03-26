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
    is_bot      INTEGER NOT NULL DEFAULT 0
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
