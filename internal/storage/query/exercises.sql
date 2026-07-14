-- name: InsertExercise :one
INSERT INTO exercises (user_id, skill_id, content_id, options)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at;

-- name: SetExerciseMessageID :exec
UPDATE exercises SET message_id = $2 WHERE id = $1;

-- name: GetExercise :one
SELECT id, user_id, skill_id, content_id, options, created_at, answered_at, message_id
FROM exercises
WHERE id = $1;

-- name: MarkExerciseAnswered :one
-- Single-use answer guard: flips answered_at only if still open. A returned row
-- means this caller owns the answer; no row means it was already answered.
UPDATE exercises
SET answered_at = $2
WHERE id = $1 AND answered_at IS NULL
RETURNING id;
