-- name: ListUptimeTargets :many
SELECT id, name, url, interval_seconds, expected_status, enabled, created_at
FROM uptime_targets ORDER BY name;

-- name: CreateUptimeTarget :exec
INSERT INTO uptime_targets (name, url, interval_seconds, expected_status) VALUES (?, ?, ?, ?);

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
SELECT id, target_id, ts, status, response_time_ms, error, created_at
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

-- name: DeleteUptimeChecksBefore :exec
DELETE FROM uptime_checks WHERE ts < ?;
