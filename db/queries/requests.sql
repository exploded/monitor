-- name: InsertRequest :exec
INSERT INTO requests (ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot, country, referer)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CountRequestsSince :one
SELECT COUNT(*) FROM requests WHERE ts >= ?;

-- name: CountBotRequestsSince :one
SELECT COUNT(*) FROM requests WHERE ts >= ? AND is_bot = 1;

-- name: CountUniqueIPsSince :one
SELECT COUNT(DISTINCT client_ip) FROM requests WHERE ts >= ?;

-- name: RecentRequestsSince :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot, referer
FROM requests WHERE ts >= ? ORDER BY id DESC LIMIT ?;

-- name: ReferrersByHost :many
SELECT referer, COUNT(*) AS cnt
FROM requests
WHERE ts >= sqlc.arg(since)
  AND referer != ''
  AND (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
GROUP BY referer ORDER BY cnt DESC LIMIT sqlc.arg(lim);

-- name: ReferrerRequestsByHost :many
SELECT id, ts, host, client_ip, method, uri, status, user_agent, referer
FROM requests
WHERE ts >= sqlc.arg(since)
  AND referer != ''
  AND (sqlc.arg(host_filter) = '' OR host = sqlc.arg(host_filter))
ORDER BY id DESC LIMIT sqlc.arg(lim);

-- The dashboard's traffic panel aggregates these rows in Go (internal/traffic).
-- It replaced eight GROUP BY queries: the three grouped by client_ip let the
-- planner walk idx_requests_client_ip across the whole table to get grouping
-- order, about 0.8s each, where this range scan on idx_requests_ts is 0.1s.
-- name: TrafficRowsSince :many
SELECT ts, host, client_ip, status, user_agent, is_bot, country, referer
FROM requests WHERE ts >= ?;

-- name: DistinctHosts :many
SELECT DISTINCT host FROM requests WHERE ts >= ? ORDER BY host;

-- Batched so the prune never holds one long write transaction against a live
-- database. The pool is capped at a single connection, so an unbounded DELETE
-- blocks every dashboard read and every watcher insert for its full duration.
-- name: DeleteRequestsBeforeBatch :execresult
DELETE FROM requests WHERE id IN (
    SELECT r.id FROM requests r WHERE r.ts < sqlc.arg(cutoff) ORDER BY r.id LIMIT sqlc.arg(batch_size)
);
