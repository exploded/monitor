-- name: ListAutoblockRules :many
SELECT id, pattern, description, enabled, hit_count, last_hit_at, created_at
FROM autoblock_rules ORDER BY hit_count DESC, created_at;

-- name: ListEnabledAutoblockRules :many
SELECT id, pattern, description FROM autoblock_rules WHERE enabled = 1;

-- name: CreateAutoblockRule :exec
INSERT OR IGNORE INTO autoblock_rules (pattern, description) VALUES (?, ?);

-- name: DeleteAutoblockRule :exec
DELETE FROM autoblock_rules WHERE id = ?;

-- name: ToggleAutoblockRule :exec
UPDATE autoblock_rules SET enabled = CASE WHEN enabled = 0 THEN 1 ELSE 0 END WHERE id = ?;

-- name: IncrementAutoblockHit :exec
UPDATE autoblock_rules SET hit_count = hit_count + 1, last_hit_at = CURRENT_TIMESTAMP WHERE id = ?;
