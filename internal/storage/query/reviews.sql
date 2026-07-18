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
SELECT * FROM reviews
WHERE user_id = $1 AND reviewed_at >= $2
ORDER BY reviewed_at;

-- name: CountReviewsSince :one
SELECT count(*) FROM reviews
WHERE user_id = $1 AND reviewed_at >= $2;

-- name: PracticeStatsSince :one
-- Totals for a /practice session: practice-flagged answers since a start time.
SELECT count(*) AS total,
       count(*) FILTER (WHERE correct) AS correct
FROM reviews
WHERE user_id = $1 AND practice = true AND reviewed_at >= $2;

-- name: ListAttemptsSince :many
-- internal/study.Service.Stats: answer records for quiz.Confusion,
-- restricted to item-based attempts (item_id IS NOT NULL, so chosen/correct_answer
-- are always populated by the item-based write path — see internal/study's
-- finishAnswer).
SELECT correct_answer, chosen, correct, response_ms, reviewed_at
FROM reviews
WHERE user_id = $1 AND reviewed_at >= $2 AND item_id IS NOT NULL
ORDER BY reviewed_at;

-- name: ReviewStatsByTopic :many
-- internal/study.Service.Stats: per-topic accuracy since a time,
-- restricted to item-based attempts (item_id IS NOT NULL) — the /stats view.
SELECT t.id AS topic_id, t.name,
       count(*) AS total,
       count(*) FILTER (WHERE r.correct) AS correct
FROM reviews r
JOIN items i ON i.id = r.item_id
JOIN topics t ON t.id = i.topic_id
WHERE r.user_id = $1 AND r.reviewed_at >= $2 AND r.item_id IS NOT NULL
GROUP BY t.id, t.name
ORDER BY t.name;
