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

-- name: SetExerciseItemFields :exec
-- v2: attach item/mode-aware metadata (architecture §2.5) to an exercise row.
-- item_id stays nullable until the old-quiz migration backfills it (§3.1).
UPDATE exercises
SET item_id = $2, mode = $3, prompt = $4, correct_answer = $5, is_media = $6, practice = $7
WHERE id = $1;

-- name: GetExercisesByItem :many
SELECT id, user_id, skill_id, content_id, item_id, mode, prompt, options,
       correct_answer, is_media, practice, created_at, answered_at, message_id
FROM exercises
WHERE item_id = $1
ORDER BY created_at DESC;

-- name: GetOpenExerciseByMode :one
-- Latest open exercise of a given mode for a user (architecture §5.4: free-text
-- answers arrive as a plain message, resolved via the caller's single open
-- mode=text exercise).
SELECT id, user_id, skill_id, content_id, item_id, mode, prompt, options,
       correct_answer, is_media, practice, created_at, answered_at, message_id
FROM exercises
WHERE user_id = $1 AND mode = $2 AND answered_at IS NULL
ORDER BY created_at DESC
LIMIT 1;
