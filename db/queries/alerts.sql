-- name: ListAlertRules :many
SELECT id, name, type, enabled, threshold, window_minutes, cooldown_minutes, last_fired_at, created_at
FROM alert_rules ORDER BY type, name;

-- name: CreateAlertRule :exec
INSERT INTO alert_rules (name, type, threshold, window_minutes, cooldown_minutes)
VALUES (?, ?, ?, ?, ?);

-- name: ToggleAlertRule :exec
UPDATE alert_rules SET enabled = CASE WHEN enabled = 0 THEN 1 ELSE 0 END WHERE id = ?;

-- name: DeleteAlertRule :exec
DELETE FROM alert_rules WHERE id = ?;

-- name: ListEnabledAlertRules :many
SELECT id, name, type, enabled, threshold, window_minutes, cooldown_minutes, last_fired_at
FROM alert_rules WHERE enabled = 1;

-- name: UpdateAlertRuleFired :exec
UPDATE alert_rules SET last_fired_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: InsertAlertLog :exec
INSERT INTO alert_log (rule_id, type, message, details) VALUES (?, ?, ?, ?);

-- name: RecentAlertLogs :many
SELECT id, rule_id, type, message, details, created_at
FROM alert_log ORDER BY id DESC LIMIT ?;

-- name: Count5xxSince :one
SELECT COUNT(*) FROM requests WHERE status >= 500 AND ts >= ?;

-- name: CountRequestsInWindow :one
SELECT COUNT(*) FROM requests WHERE ts >= ?;

-- name: AppErrorSummarySince :many
-- One row per app with errors in the window, carrying the most recent message as
-- a sample. `message` is a bare column against MAX(ts): SQLite documents that as
-- taking the value from the row holding the max, which is exactly the sample we
-- want. Alerting is per app, so no ORDER BY or LIMIT: a chatty app must not be
-- able to push the first error of a quieter app off the list.
SELECT app, COUNT(*) AS cnt, message AS sample, MAX(ts) AS last_ts
FROM app_logs WHERE level = 'ERROR' AND ts >= ?
GROUP BY app;

-- name: DeleteAlertLogsBeforeBatch :execresult
DELETE FROM alert_log WHERE id IN (
    SELECT al.id FROM alert_log al WHERE al.created_at < sqlc.arg(cutoff) ORDER BY al.id LIMIT sqlc.arg(batch_size)
);
