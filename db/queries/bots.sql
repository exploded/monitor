-- name: ListBotPatterns :many
SELECT id, pattern, label, created_at FROM bot_patterns ORDER BY label;

-- name: CreateBotPattern :exec
INSERT INTO bot_patterns (pattern, label) VALUES (?, ?);

-- name: DeleteBotPattern :exec
DELETE FROM bot_patterns WHERE id = ?;
