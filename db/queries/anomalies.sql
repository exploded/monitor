-- name: InsertAnomaly :exec
INSERT INTO anomalies (ts, type, client_ip, host, description, score) VALUES (?, ?, ?, ?, ?, ?);

-- Has this (type, client_ip, host) already fired recently? The detector runs
-- every 2 minutes, so without this a single persistent offender wrote a row
-- every tick for as long as it stayed over threshold.
-- name: RecentAnomalyExists :one
SELECT EXISTS (
    SELECT 1 FROM anomalies
    WHERE type = sqlc.arg(type) AND client_ip = sqlc.arg(client_ip)
      AND host = sqlc.arg(host) AND ts >= sqlc.arg(since)
);

-- name: RecentAnomalies :many
SELECT id, ts, type, client_ip, host, description, score, acknowledged, created_at
FROM anomalies ORDER BY id DESC LIMIT ?;

-- name: AcknowledgeAnomaly :exec
UPDATE anomalies SET acknowledged = 1 WHERE id = ?;

-- name: DeleteAnomaliesBeforeBatch :execresult
DELETE FROM anomalies WHERE id IN (
    SELECT a.id FROM anomalies a WHERE a.ts < sqlc.arg(cutoff) ORDER BY a.id LIMIT sqlc.arg(batch_size)
);
