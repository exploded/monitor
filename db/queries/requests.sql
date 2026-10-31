-- name: InsertRequest :exec
INSERT INTO requests (ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot, country)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CountRequestsSince :one
SELECT COUNT(*) FROM requests WHERE ts >= ?;

-- The dashboard's traffic panel aggregates these rows in Go (internal/traffic).
-- It replaced eight GROUP BY queries: the three grouped by client_ip let the
-- planner walk idx_requests_client_ip across the whole table to get grouping
-- order, about 0.8s each, where this range scan on idx_requests_ts is 0.1s.
-- name: TrafficRowsSince :many
SELECT ts, host, client_ip, status, user_agent, is_bot, country
FROM requests WHERE ts >= ?;

-- Batched so the prune never holds one long write transaction against a live
-- database. The pool is capped at a single connection, so an unbounded DELETE
-- blocks every dashboard read and every watcher insert for its full duration.
-- name: DeleteRequestsBeforeBatch :execresult
DELETE FROM requests WHERE id IN (
    SELECT r.id FROM requests r WHERE r.ts < sqlc.arg(cutoff) ORDER BY r.id LIMIT sqlc.arg(batch_size)
);
