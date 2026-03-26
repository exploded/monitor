-- name: HourlyRequestCounts :many
SELECT strftime('%Y-%m-%d %H:00', ts) AS hour, COUNT(*) AS cnt
FROM requests
WHERE ts >= ?
GROUP BY hour ORDER BY hour;

-- name: HourlyRequestCountsByHost :many
SELECT strftime('%Y-%m-%d %H:00', ts) AS hour, COUNT(*) AS cnt
FROM requests
WHERE ts >= sqlc.arg(since) AND (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
GROUP BY hour ORDER BY hour;

-- name: DailySummary :many
SELECT
    date(ts) AS day,
    COUNT(*) AS total,
    SUM(CASE WHEN is_bot = 1 THEN 1 ELSE 0 END) AS bots,
    COUNT(DISTINCT client_ip) AS unique_ips,
    SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END) AS errors,
    ROUND(AVG(duration_ms), 1) AS avg_duration_ms,
    SUM(size) AS total_bytes
FROM requests
WHERE ts >= ?
GROUP BY day ORDER BY day DESC;

-- name: SearchRequests :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot
FROM requests
WHERE (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
  AND (sqlc.arg(ip_filter) = '' OR client_ip = sqlc.arg(ip_filter))
  AND (sqlc.arg(status_filter) = 0
       OR (sqlc.arg(status_filter) < 10 AND status / 100 = sqlc.arg(status_filter))
       OR (sqlc.arg(status_filter) >= 10 AND status = sqlc.arg(status_filter)))
  AND (sqlc.arg(ua_filter) = '' OR user_agent LIKE '%' || sqlc.arg(ua_filter) || '%')
  AND ts >= sqlc.arg(from_ts) AND ts <= sqlc.arg(to_ts)
ORDER BY id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: TopCountriesSince :many
SELECT country, COUNT(*) AS cnt
FROM requests WHERE ts >= ? AND country != ''
GROUP BY country ORDER BY cnt DESC LIMIT ?;

-- name: HourlyDurations :many
SELECT strftime('%Y-%m-%d %H:00', ts) AS hour, duration_ms
FROM requests
WHERE ts >= sqlc.arg(since) AND (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
ORDER BY hour, duration_ms;

-- name: MinutelyRequestCountsByHost :many
SELECT host, strftime('%Y-%m-%d %H:%M', ts) AS minute, COUNT(*) AS cnt
FROM requests
WHERE ts >= ?
GROUP BY host, minute
ORDER BY host, minute;

-- name: HourlyBandwidth :many
SELECT strftime('%Y-%m-%d %H:00', ts) AS hour, SUM(size) AS total_bytes, COUNT(*) AS cnt
FROM requests
WHERE ts >= sqlc.arg(since) AND (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
GROUP BY hour ORDER BY hour;

-- name: DailyBandwidthSummary :many
SELECT date(ts) AS day, SUM(size) AS total_bytes, COUNT(*) AS cnt
FROM requests WHERE ts >= ?
GROUP BY day ORDER BY day DESC;

-- name: BandwidthByHostSince :many
SELECT host, SUM(size) AS total_bytes, COUNT(*) AS cnt
FROM requests WHERE ts >= ?
GROUP BY host ORDER BY total_bytes DESC;

-- name: IPReputationData :many
SELECT
    client_ip,
    COUNT(*) AS total,
    SUM(CASE WHEN status >= 400 AND status < 500 THEN 1 ELSE 0 END) AS count_4xx,
    SUM(CASE WHEN is_bot = 1 THEN 1 ELSE 0 END) AS bot_count,
    MIN(ts) AS first_seen,
    MAX(ts) AS last_seen
FROM requests
WHERE ts >= ?
GROUP BY client_ip
HAVING total >= 3
ORDER BY total DESC
LIMIT ?;

-- name: SearchRequestsExport :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot
FROM requests
WHERE (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
  AND (sqlc.arg(ip_filter) = '' OR client_ip = sqlc.arg(ip_filter))
  AND (sqlc.arg(status_filter) = 0
       OR (sqlc.arg(status_filter) < 10 AND status / 100 = sqlc.arg(status_filter))
       OR (sqlc.arg(status_filter) >= 10 AND status = sqlc.arg(status_filter)))
  AND (sqlc.arg(ua_filter) = '' OR user_agent LIKE '%' || sqlc.arg(ua_filter) || '%')
  AND ts >= sqlc.arg(from_ts) AND ts <= sqlc.arg(to_ts)
ORDER BY id DESC
LIMIT 10000;
