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
-- avg_duration leaves out latency_exclude prefixes, as HourRows does;
-- AVG skips the NULLs.
-- name: RollupDailyStats :exec
INSERT INTO daily_stats (day, total, bots, unique_ips, errors, avg_duration, bytes)
SELECT
    date(ts),
    COUNT(*),
    SUM(CASE WHEN is_bot = 1 THEN 1 ELSE 0 END),
    COUNT(DISTINCT client_ip),
    SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),
    COALESCE(ROUND(AVG(CASE WHEN NOT EXISTS (
        SELECT 1 FROM json_each(CAST(sqlc.arg(latency_exclude) AS TEXT)) AS x
        WHERE substr(requests.uri, 1, length(x.value)) = x.value)
      THEN duration_ms END), 1), 0),
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

-- Ordered by ts, not id, so idx_requests_ts serves both the range and the
-- order and the scan stops at the limit. ORDER BY id DESC made the planner
-- sort the whole range in a temp B-tree first. The id tiebreak matches the
-- index, whose entries are (ts, rowid).
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
ORDER BY ts DESC, id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

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
ORDER BY ts DESC, id DESC
LIMIT 10000;

-- SearchRequests, slowest first. A separate query because sqlc's SQLite engine
-- does not rewrite parameters in ORDER BY: a sqlc.arg there is left in the SQL
-- as text. Keep the WHERE clause in step with SearchRequests. It also skips
-- LATENCY_EXCLUDE paths: long polls are slow by design and would fill the
-- first page. Newest-first still shows them.
-- name: SearchRequestsSlowest :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot
FROM requests
WHERE (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
  AND (sqlc.arg(ip_filter) = '' OR client_ip = sqlc.arg(ip_filter))
  AND (sqlc.arg(status_filter) = 0
       OR (sqlc.arg(status_filter) < 10 AND status / 100 = sqlc.arg(status_filter))
       OR (sqlc.arg(status_filter) >= 10 AND status = sqlc.arg(status_filter)))
  AND (sqlc.arg(ua_filter) = '' OR user_agent LIKE '%' || sqlc.arg(ua_filter) || '%')
  AND ts >= sqlc.arg(from_ts) AND ts <= sqlc.arg(to_ts)
  AND NOT EXISTS (
    SELECT 1 FROM json_each(CAST(sqlc.arg(latency_exclude) AS TEXT)) AS x
    WHERE substr(requests.uri, 1, length(x.value)) = x.value)
ORDER BY duration_ms DESC, id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- SearchRequestsExport, slowest first. See SearchRequestsSlowest.
-- name: SearchRequestsExportSlowest :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot
FROM requests
WHERE (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
  AND (sqlc.arg(ip_filter) = '' OR client_ip = sqlc.arg(ip_filter))
  AND (sqlc.arg(status_filter) = 0
       OR (sqlc.arg(status_filter) < 10 AND status / 100 = sqlc.arg(status_filter))
       OR (sqlc.arg(status_filter) >= 10 AND status = sqlc.arg(status_filter)))
  AND (sqlc.arg(ua_filter) = '' OR user_agent LIKE '%' || sqlc.arg(ua_filter) || '%')
  AND ts >= sqlc.arg(from_ts) AND ts <= sqlc.arg(to_ts)
  AND NOT EXISTS (
    SELECT 1 FROM json_each(CAST(sqlc.arg(latency_exclude) AS TEXT)) AS x
    WHERE substr(requests.uri, 1, length(x.value)) = x.value)
ORDER BY duration_ms DESC, id DESC
LIMIT 10000;

-- Raw rows for one hour of hourly_stats. excluded marks LATENCY_EXCLUDE
-- prefixes (see config.LatencyExcludeJSON): long polls such as skycam's
-- /api/demand hold every request open for a fixed time, so at low traffic they
-- would pin p95 and p99 to their timeout. They still count as traffic. substr
-- rather than LIKE so a prefix containing _ or % matches literally.
-- name: HourRows :many
SELECT status, size, duration_ms,
    CAST(EXISTS (
        SELECT 1 FROM json_each(CAST(sqlc.arg(latency_exclude) AS TEXT)) AS x
        WHERE substr(requests.uri, 1, length(x.value)) = x.value) AS INTEGER) AS excluded
FROM requests
WHERE ts >= sqlc.arg(from_ts) AND ts < sqlc.arg(to_ts);

-- name: UpsertHourlyStats :exec
INSERT INTO hourly_stats (hour, total, errors_5xx, bytes, lat_count, p50, p95, p99)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(hour) DO UPDATE SET
    total = excluded.total, errors_5xx = excluded.errors_5xx, bytes = excluded.bytes,
    lat_count = excluded.lat_count, p50 = excluded.p50, p95 = excluded.p95, p99 = excluded.p99;

-- name: HourlyStatsSince :many
SELECT hour, total, errors_5xx, bytes, lat_count, p50, p95, p99
FROM hourly_stats
WHERE hour >= sqlc.arg(from_hour)
ORDER BY hour;

-- name: DeleteHourlyStatsBefore :exec
DELETE FROM hourly_stats WHERE hour < sqlc.arg(cutoff_hour);
