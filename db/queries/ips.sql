-- name: ListBlockedIPs :many
SELECT id, ip, reason, created_at FROM blocked_ips ORDER BY created_at DESC;

-- name: CreateBlockedIP :exec
INSERT OR IGNORE INTO blocked_ips (ip, reason) VALUES (?, ?);

-- name: DeleteBlockedIP :exec
DELETE FROM blocked_ips WHERE id = ?;

-- name: IsIPBlocked :one
SELECT COUNT(*) FROM blocked_ips WHERE ip = ?;

-- name: PruneAutoBlockedIPs :execresult
DELETE FROM blocked_ips
WHERE (reason LIKE 'auto-blocked:%' OR reason LIKE 'honeypot:%')
AND created_at < ?;
