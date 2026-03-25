-- name: ListBotPatterns :many
SELECT id, pattern, label, block, created_at FROM bot_patterns ORDER BY label;

-- name: CreateBotPattern :exec
INSERT INTO bot_patterns (pattern, label, block) VALUES (?, ?, ?);

-- name: DeleteBotPattern :exec
DELETE FROM bot_patterns WHERE id = ?;

-- name: ToggleBotBlock :exec
UPDATE bot_patterns SET block = CASE WHEN block = 0 THEN 1 ELSE 0 END WHERE id = ?;

-- name: GetBotPattern :one
SELECT id, pattern, label, block, created_at FROM bot_patterns WHERE id = ?;
