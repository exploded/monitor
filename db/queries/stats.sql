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
    ROUND(AVG(duration_ms), 1) AS avg_duration_ms
FROM requests
WHERE ts >= ?
GROUP BY day ORDER BY day DESC;

-- name: SearchRequests :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot
FROM requests
WHERE (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
  AND (sqlc.arg(ip_filter) = '' OR client_ip = sqlc.arg(ip_filter))
  AND (sqlc.arg(status_filter) = 0 OR status = sqlc.arg(status_filter))
  AND (sqlc.arg(ua_filter) = '' OR user_agent LIKE '%' || sqlc.arg(ua_filter) || '%')
  AND ts >= sqlc.arg(from_ts) AND ts <= sqlc.arg(to_ts)
ORDER BY id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);
