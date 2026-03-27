-- name: InsertAppLog :exec
INSERT INTO app_logs (ts, app, level, message, attrs, source)
VALUES (?, ?, ?, ?, ?, ?);

-- name: RecentAppErrors :many
SELECT id, ts, app, level, message, attrs, source, created_at
FROM app_logs
WHERE level IN ('ERROR', 'WARN')
ORDER BY ts DESC, id DESC
LIMIT ?;

-- name: RecentAppLogs :many
SELECT id, ts, app, level, message, attrs, source, created_at
FROM app_logs
ORDER BY id DESC
LIMIT ?;

-- name: CountAppErrorsSince :one
SELECT COUNT(*) FROM app_logs WHERE level = 'ERROR' AND ts >= ?;

-- name: CountAppLogsSince :one
SELECT COUNT(*) FROM app_logs WHERE ts >= ?;

-- name: AppList :many
SELECT DISTINCT app FROM app_logs ORDER BY app;

-- name: GetAppLog :one
SELECT id, ts, app, level, message, attrs, source, created_at
FROM app_logs
WHERE id = ?;

-- name: DeleteAppLogsBefore :exec
DELETE FROM app_logs WHERE ts < ?;
