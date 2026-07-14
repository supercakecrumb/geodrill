-- name: UpsertUser :one
INSERT INTO users (telegram_id, username)
VALUES ($1, $2)
ON CONFLICT (telegram_id)
DO UPDATE SET username = EXCLUDED.username
RETURNING *;

-- name: GetUserByTelegramID :one
SELECT * FROM users WHERE telegram_id = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: SetDailyCap :exec
UPDATE users SET daily_new_cap = $2 WHERE id = $1;

-- name: SetReminders :exec
UPDATE users SET reminders_enabled = $2 WHERE id = $1;

-- name: SetTimezone :exec
UPDATE users SET timezone = $2 WHERE id = $1;

-- name: UsersWithReminders :many
SELECT * FROM users WHERE reminders_enabled = true ORDER BY created_at;
