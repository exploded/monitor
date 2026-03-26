-- name: InsertAnomaly :exec
INSERT INTO anomalies (ts, type, client_ip, host, description, score) VALUES (?, ?, ?, ?, ?, ?);

-- name: RecentAnomalies :many
SELECT id, ts, type, client_ip, host, description, score, acknowledged, created_at
FROM anomalies ORDER BY id DESC LIMIT ?;

-- name: AcknowledgeAnomaly :exec
UPDATE anomalies SET acknowledged = 1 WHERE id = ?;

-- name: CountUnacknowledgedAnomalies :one
SELECT COUNT(*) FROM anomalies WHERE acknowledged = 0;

-- name: DeleteAnomaliesBefore :exec
DELETE FROM anomalies WHERE ts < ?;

-- name: IPRequestRateRecent :many
SELECT client_ip, COUNT(*) AS cnt
FROM requests WHERE ts >= sqlc.arg(since)
GROUP BY client_ip
HAVING COUNT(*) >= sqlc.arg(min_count)
ORDER BY COUNT(*) DESC;

-- name: IPErrorRate :many
SELECT client_ip,
    COUNT(*) AS total,
    SUM(CASE WHEN status >= 400 AND status < 500 THEN 1 ELSE 0 END) AS count_4xx
FROM requests WHERE ts >= ?
GROUP BY client_ip
HAVING COUNT(*) >= 10
ORDER BY total DESC;

-- name: HourlyErrorRates :many
SELECT strftime('%Y-%m-%d %H:00', ts) AS hour,
       COUNT(*) AS total,
       SUM(CASE WHEN status >= 500 THEN 1 ELSE 0 END) AS count_5xx
FROM requests WHERE ts >= ?
GROUP BY hour ORDER BY hour;
