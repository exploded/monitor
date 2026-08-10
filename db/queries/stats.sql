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

-- Reads the rollup, not raw rows. Previously this recomputed six aggregates
-- (including a COUNT DISTINCT) over a 30-day partition of `requests` on every
-- /history load and every /partials/daily poll.
-- name: DailySummary :many
SELECT day, total, bots, unique_ips, errors, avg_duration AS avg_duration_ms, bytes AS total_bytes
FROM daily_stats
WHERE day >= sqlc.arg(from_day)
ORDER BY day DESC;

-- Recomputes one day from raw. Called for today and yesterday on each rollup
-- tick (yesterday so the final late-arriving rows are folded in), and for every
-- uncovered day during backfill. UPSERT so re-running is idempotent.
-- name: RollupDailyStats :exec
INSERT INTO daily_stats (day, total, bots, unique_ips, errors, avg_duration, bytes)
SELECT
    date(ts),
    COUNT(*),
    SUM(CASE WHEN is_bot = 1 THEN 1 ELSE 0 END),
    COUNT(DISTINCT client_ip),
    SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),
    COALESCE(ROUND(AVG(duration_ms), 1), 0),
    COALESCE(SUM(size), 0)
FROM requests
-- Half-open range rather than date(ts) = ?. Wrapping the column in a function
-- makes the predicate non-sargable, so idx_requests_ts is unusable and every
-- hourly rollup full-scans requests. ts is normalised text, so it compares
-- lexically against a bare YYYY-MM-DD. GROUP BY stays so an empty day inserts
-- no row at all, rather than a row of zeroes.
WHERE ts >= CAST(sqlc.arg(day) AS TEXT)
  AND ts < date(CAST(sqlc.arg(day) AS TEXT), '+1 day')
GROUP BY date(ts)
ON CONFLICT(day) DO UPDATE SET
    total = excluded.total, bots = excluded.bots, unique_ips = excluded.unique_ips,
    errors = excluded.errors, avg_duration = excluded.avg_duration, bytes = excluded.bytes;

-- Days present in raw `requests` but not yet rolled up. Drives the backfill and
-- catches any day missed while the process was down.
-- Deliberately unbounded: it must find every day raw still holds, including days
-- older than the retention horizon that Prune is about to delete. It is a full
-- scan, so Rollup only runs it at startup. See the comment there.
-- name: DaysMissingFromDailyStats :many
SELECT DISTINCT date(ts) AS day
FROM requests
WHERE date(ts) NOT IN (SELECT day FROM daily_stats)
ORDER BY day;

-- name: DeleteDailyStatsBefore :exec
DELETE FROM daily_stats WHERE day < sqlc.arg(cutoff_day);

-- name: SearchRequests :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot, referer
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
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot, referer
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
