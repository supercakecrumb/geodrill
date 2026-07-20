-- name: InsertExercise :one
-- internal/study.Service: insert a mode-aware exercise row — item_id/mode/
-- prompt/options/correct_answer/is_media/practice — in one shot. content_id
-- is optional (shared/topic-scoped content, or none).
INSERT INTO exercises (user_id, item_id, content_id, mode, prompt, options, correct_answer, is_media, practice)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, created_at;

-- name: SetExerciseMessageID :exec
UPDATE exercises SET message_id = $2 WHERE id = $1;

-- name: MarkExerciseAnswered :one
-- Single-use answer guard: flips answered_at only if still open. A returned row
-- means this caller owns the answer; no row means it was already answered.
UPDATE exercises
SET answered_at = $2
WHERE id = $1 AND answered_at IS NULL
RETURNING id;

-- name: SetExerciseItemFields :exec
-- Updates item/mode-aware metadata on an existing exercise row.
UPDATE exercises
SET item_id = $2, mode = $3, prompt = $4, correct_answer = $5, is_media = $6, practice = $7
WHERE id = $1;

-- name: GetExercisesByItem :many
SELECT id, user_id, content_id, item_id, mode, prompt, options,
       correct_answer, is_media, practice, created_at, answered_at, message_id
FROM exercises
WHERE item_id = $1
ORDER BY created_at DESC;

-- name: GetOpenExerciseByMode :one
-- Latest open exercise of a given mode for a user (architecture §5.4: free-text
-- answers arrive as a plain message, resolved via the caller's single open
-- mode=text exercise).
SELECT id, user_id, content_id, item_id, mode, prompt, options,
       correct_answer, is_media, practice, created_at, answered_at, message_id
FROM exercises
WHERE user_id = $1 AND mode = $2 AND answered_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: GetExerciseByID :one
-- internal/study.Service.Answer: fetch one exercise by id with the full
-- mode-aware column set.
SELECT * FROM exercises WHERE id = $1;

-- name: DeleteOpenExercisesByTopic :execrows
-- Delete only the OPEN (unanswered) exercises of every item under a topic —
-- the cities cutover's reset. Answered exercises stay as archive (reviews
-- reference only answered exercises, so this never trips the reviews->exercises
-- FK), and items themselves are untouched (exercises->items has no cascade).
DELETE FROM exercises e
USING items i
WHERE e.item_id = i.id AND i.topic_id = $1 AND e.answered_at IS NULL;
