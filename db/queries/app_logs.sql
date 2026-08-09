-- name: InsertAppLog :exec
INSERT INTO app_logs (ts, app, level, message, attrs, source)
VALUES (?, ?, ?, ?, ?, ?);

-- name: RecentAppErrors :many
SELECT id, ts, app, level, message, attrs, source, created_at
FROM app_logs
WHERE level IN ('ERROR', 'WARN')
ORDER BY ts DESC, id DESC
LIMIT ?;

-- name: CountAppErrorsSince :one
SELECT COUNT(*) FROM app_logs WHERE level = 'ERROR' AND ts >= ?;

-- name: CountAppLogsSince :one
SELECT COUNT(*) FROM app_logs WHERE ts >= ?;

-- name: GetAppLog :one
SELECT id, ts, app, level, message, attrs, source, created_at
FROM app_logs
WHERE id = ?;

-- Level-aware retention: ERROR is rare and worth keeping, but the table is
-- overwhelmingly WARN/INFO noise, so those get a much shorter horizon.
-- name: DeleteAppLogsBeforeBatch :execresult
DELETE FROM app_logs WHERE id IN (
    SELECT al.id FROM app_logs al
    WHERE (al.level = 'ERROR' AND al.ts < sqlc.arg(error_cutoff))
       OR (al.level <> 'ERROR' AND al.ts < sqlc.arg(other_cutoff))
    ORDER BY al.id LIMIT sqlc.arg(batch_size)
);
