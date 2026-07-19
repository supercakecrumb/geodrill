-- name: InsertReview :exec
-- Append a review carrying the item-based, mode-aware column set
-- (architecture §2.5).
INSERT INTO reviews (
  user_id, item_id, exercise_id, content_id, mode, chosen, correct_answer,
  correct, rating, response_ms,
  stability_before, difficulty_before, stability_after, difficulty_after,
  state_before, scheduled_days, elapsed_days, practice, reviewed_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7,
  $8, $9, $10,
  $11, $12, $13, $14,
  $15, $16, $17, $18, $19
);

-- name: GetReviewsByItem :many
SELECT * FROM reviews WHERE item_id = $1 ORDER BY reviewed_at;

-- name: ListReviewsSince :many
-- GeoGuessr-only filter: LEFT JOIN items so legacy item-less rows are kept
-- when gg_only is off, and dropped only under gg_only (a NULL gg_relevant
-- fails the predicate) — the same no-extra-param, user_id-drives-the-flag
-- pattern the study/tier queries use.
SELECT r.* FROM reviews r
LEFT JOIN items i ON i.id = r.item_id
JOIN users u ON u.id = sqlc.arg(user_id)
WHERE r.user_id = sqlc.arg(user_id) AND r.reviewed_at >= sqlc.arg(reviewed_at)
  AND (NOT u.gg_only OR i.gg_relevant)
ORDER BY r.reviewed_at;

-- name: CountReviewsSince :one
-- GeoGuessr-only filtered (see ListReviewsSince).
SELECT count(*) FROM reviews r
LEFT JOIN items i ON i.id = r.item_id
JOIN users u ON u.id = sqlc.arg(user_id)
WHERE r.user_id = sqlc.arg(user_id) AND r.reviewed_at >= sqlc.arg(reviewed_at)
  AND (NOT u.gg_only OR i.gg_relevant);

-- name: ListAttemptsSince :many
-- internal/study.Service.Stats: answer records for quiz.Confusion,
-- restricted to item-based attempts (item_id IS NOT NULL, so chosen/correct_answer
-- are always populated by the item-based write path — see internal/study's
-- finishAnswer). GeoGuessr-only filtered (see ListReviewsSince).
SELECT r.correct_answer, r.chosen, r.correct, r.response_ms, r.reviewed_at
FROM reviews r
JOIN items i ON i.id = r.item_id
JOIN users u ON u.id = sqlc.arg(user_id)
WHERE r.user_id = sqlc.arg(user_id) AND r.reviewed_at >= sqlc.arg(reviewed_at) AND r.item_id IS NOT NULL
  AND (NOT u.gg_only OR i.gg_relevant)
ORDER BY r.reviewed_at;

-- name: ReviewStatsByTopic :many
-- internal/study.Service.Stats: per-topic accuracy since a time,
-- restricted to item-based attempts (item_id IS NOT NULL) — the /stats view.
-- GeoGuessr-only filtered (see ListReviewsSince).
SELECT t.id AS topic_id, t.name,
       count(*) AS total,
       count(*) FILTER (WHERE r.correct) AS correct
FROM reviews r
JOIN items i ON i.id = r.item_id
JOIN topics t ON t.id = i.topic_id
JOIN users u ON u.id = sqlc.arg(user_id)
WHERE r.user_id = sqlc.arg(user_id) AND r.reviewed_at >= sqlc.arg(reviewed_at) AND r.item_id IS NOT NULL
  AND (NOT u.gg_only OR i.gg_relevant)
GROUP BY t.id, t.name
ORDER BY t.name;
