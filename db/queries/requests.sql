-- name: InsertRequest :exec
INSERT INTO requests (ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot, country, city, referer)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CountRequests :one
SELECT COUNT(*) FROM requests;

-- name: CountRequestsSince :one
SELECT COUNT(*) FROM requests WHERE ts >= ?;

-- name: CountBotRequestsSince :one
SELECT COUNT(*) FROM requests WHERE ts >= ? AND is_bot = 1;

-- name: CountUniqueIPsSince :one
SELECT COUNT(DISTINCT client_ip) FROM requests WHERE ts >= ?;

-- name: RecentRequests :many
SELECT id, ts, host, client_ip, method, uri, status, size, user_agent, duration_ms, is_bot, referer
FROM requests ORDER BY id DESC LIMIT ?;

-- name: TopReferrersSince :many
SELECT referer, COUNT(*) AS cnt
FROM requests WHERE ts >= ? AND referer != ''
GROUP BY referer ORDER BY cnt DESC LIMIT ?;

-- name: TopIPsSince :many
SELECT client_ip, COUNT(*) AS cnt
FROM requests WHERE ts >= ?
GROUP BY client_ip ORDER BY cnt DESC LIMIT ?;

-- name: TopUserAgentsSince :many
SELECT user_agent, COUNT(*) AS cnt, MAX(is_bot) AS is_bot
FROM requests WHERE ts >= ?
GROUP BY user_agent ORDER BY cnt DESC LIMIT ?;

-- name: RequestsByHostSince :many
SELECT host, COUNT(*) AS cnt
FROM requests WHERE ts >= ?
GROUP BY host ORDER BY cnt DESC;

-- name: StatusCodesSince :many
SELECT status, COUNT(*) AS cnt
FROM requests WHERE ts >= ?
GROUP BY status ORDER BY status;

-- name: DeleteRequestsBefore :exec
DELETE FROM requests WHERE ts < ?;
