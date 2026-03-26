-- name: ListHoneypots :many
SELECT id, path, description, enabled, hit_count, last_hit_at, created_at
FROM honeypots ORDER BY hit_count DESC, created_at;

-- name: ListEnabledHoneypots :many
SELECT id, path, description FROM honeypots WHERE enabled = 1;

-- name: CreateHoneypot :exec
INSERT OR IGNORE INTO honeypots (path, description) VALUES (?, ?);

-- name: DeleteHoneypot :exec
DELETE FROM honeypots WHERE id = ?;

-- name: ToggleHoneypot :exec
UPDATE honeypots SET enabled = CASE WHEN enabled = 0 THEN 1 ELSE 0 END WHERE id = ?;

-- name: IncrementHoneypotHit :exec
UPDATE honeypots SET hit_count = hit_count + 1, last_hit_at = CURRENT_TIMESTAMP WHERE id = ?;
