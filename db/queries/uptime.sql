-- name: GetUptimeTarget :one
SELECT id, name, url, interval_seconds, expected_status, enabled, created_at
FROM uptime_targets WHERE id = ?;

-- name: ListUptimeTargets :many
SELECT id, name, url, interval_seconds, expected_status, enabled, created_at
FROM uptime_targets ORDER BY name;

-- name: CreateUptimeTarget :exec
INSERT INTO uptime_targets (name, url, interval_seconds, expected_status) VALUES (?, ?, ?, ?);

-- name: UpdateUptimeTarget :exec
-- Edits a target in place so its uptime_checks and uptime_daily history survive.
-- Deleting and recreating would orphan both.
UPDATE uptime_targets
SET name = ?, url = ?, interval_seconds = ?, expected_status = ?
WHERE id = ?;

-- name: DeleteUptimeTarget :exec
DELETE FROM uptime_targets WHERE id = ?;

-- name: ToggleUptimeTarget :exec
UPDATE uptime_targets SET enabled = CASE WHEN enabled = 0 THEN 1 ELSE 0 END WHERE id = ?;

-- name: ListEnabledUptimeTargets :many
SELECT id, name, url, interval_seconds, expected_status
FROM uptime_targets WHERE enabled = 1;

-- name: InsertUptimeCheck :exec
INSERT INTO uptime_checks (target_id, ts, status, response_time_ms, error) VALUES (?, ?, ?, ?, ?);

-- name: RecentUptimeChecks :many
SELECT id, target_id, ts, status, response_time_ms, error
FROM uptime_checks WHERE target_id = ? ORDER BY id DESC LIMIT ?;

-- name: UptimePercentage :one
SELECT
    COUNT(*) AS total,
    SUM(CASE WHEN status = ? THEN 1 ELSE 0 END) AS up_count
FROM uptime_checks
WHERE target_id = ? AND ts >= ?;

-- name: AvgResponseTime :one
SELECT COALESCE(ROUND(AVG(response_time_ms), 1), 0) AS avg_ms
FROM uptime_checks
WHERE target_id = ? AND ts >= ? AND error = '';

-- name: UptimeChecksSince :many
SELECT id, target_id, ts, status, response_time_ms, error
FROM uptime_checks
WHERE target_id = sqlc.arg(target_id) AND ts >= sqlc.arg(since)
ORDER BY ts;

-- name: RollupUptimeDaily :exec
INSERT INTO uptime_daily (target_id, day, checks, up_count, avg_response_ms, max_response_ms)
SELECT
    target_id,
    date(ts),
    COUNT(*),
    SUM(CASE WHEN error = '' THEN 1 ELSE 0 END),
    COALESCE(ROUND(AVG(CASE WHEN error = '' THEN response_time_ms END), 1), 0),
    COALESCE(MAX(CASE WHEN error = '' THEN response_time_ms END), 0)
FROM uptime_checks
-- Half-open range, for the same reason as RollupDailyStats.
WHERE ts >= CAST(sqlc.arg(day) AS TEXT)
  AND ts < date(CAST(sqlc.arg(day) AS TEXT), '+1 day')
GROUP BY target_id, date(ts)
ON CONFLICT(target_id, day) DO UPDATE SET
    checks = excluded.checks, up_count = excluded.up_count,
    avg_response_ms = excluded.avg_response_ms, max_response_ms = excluded.max_response_ms;

-- name: DaysMissingFromUptimeDaily :many
SELECT DISTINCT date(ts) AS day
FROM uptime_checks
WHERE date(ts) NOT IN (SELECT day FROM uptime_daily)
ORDER BY day;

-- name: UptimeDailySince :many
SELECT target_id, day, checks, up_count, avg_response_ms, max_response_ms
FROM uptime_daily
WHERE target_id = sqlc.arg(target_id) AND day >= sqlc.arg(from_day)
ORDER BY day;

-- name: DeleteUptimeDailyBefore :exec
DELETE FROM uptime_daily WHERE day < sqlc.arg(cutoff_day);

-- name: DeleteUptimeChecksForTarget :exec
DELETE FROM uptime_checks WHERE target_id = ?;

-- name: DeleteUptimeDailyForTarget :exec
DELETE FROM uptime_daily WHERE target_id = ?;

-- name: DeleteUptimeChecksBeforeBatch :execresult
DELETE FROM uptime_checks WHERE id IN (
    SELECT u.id FROM uptime_checks u WHERE u.ts < sqlc.arg(cutoff) ORDER BY u.id LIMIT sqlc.arg(batch_size)
);
