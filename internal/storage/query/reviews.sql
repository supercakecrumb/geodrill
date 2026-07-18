-- name: InsertReview :exec
INSERT INTO reviews (
  user_id, skill_id, exercise_id, content_id,
  chosen_key, correct_key, correct, rating, response_ms,
  stability_before, difficulty_before, stability_after, difficulty_after,
  state_before, scheduled_days, elapsed_days, reviewed_at, practice
) VALUES (
  $1, $2, $3, $4,
  $5, $6, $7, $8, $9,
  $10, $11, $12, $13,
  $14, $15, $16, $17, $18
);

-- name: InsertReviewV2 :exec
-- v2: append a review carrying both the legacy skill_id/chosen_key/correct_key
-- (still NOT NULL until 000007 drops them) and the generalized item_id/mode/
-- chosen/correct_answer columns (architecture §2.5, transitional).
INSERT INTO reviews (
  user_id, skill_id, exercise_id, content_id,
  chosen_key, correct_key, correct, rating, response_ms,
  stability_before, difficulty_before, stability_after, difficulty_after,
  state_before, scheduled_days, elapsed_days, reviewed_at, practice,
  item_id, mode, chosen, correct_answer
) VALUES (
  $1, $2, $3, $4,
  $5, $6, $7, $8, $9,
  $10, $11, $12, $13,
  $14, $15, $16, $17, $18,
  $19, $20, $21, $22
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
SELECT skill_id, correct_key, chosen_key, correct, response_ms, reviewed_at
FROM reviews
WHERE user_id = $1 AND reviewed_at >= $2
ORDER BY reviewed_at;

-- name: ReviewStatsByDeck :many
SELECT d.slug, d.name,
       count(*) AS total,
       count(*) FILTER (WHERE r.correct) AS correct
FROM reviews r
JOIN skills s ON s.id = r.skill_id
JOIN decks d ON d.id = s.deck_id
WHERE r.user_id = $1 AND r.reviewed_at >= $2
GROUP BY d.slug, d.name
ORDER BY d.slug;

-- name: ReviewStatsByTopic :many
-- v2 (internal/study.Service.Stats): per-topic accuracy since a time,
-- restricted to v2 attempts (item_id IS NOT NULL) — replaces ReviewStatsByDeck
-- for the /stats view once every write path is item-based (architecture §2.5).
SELECT t.id AS topic_id, t.name,
       count(*) AS total,
       count(*) FILTER (WHERE r.correct) AS correct
FROM reviews r
JOIN items i ON i.id = r.item_id
JOIN topics t ON t.id = i.topic_id
WHERE r.user_id = $1 AND r.reviewed_at >= $2 AND r.item_id IS NOT NULL
GROUP BY t.id, t.name
ORDER BY t.name;

-- name: ListAttemptsSinceV2 :many
-- v2 (internal/study.Service.Stats): answer records for quiz.Confusion,
-- restricted to v2 attempts (item_id IS NOT NULL, so chosen/correct_answer
-- are always populated by the v2 write path — see internal/study's
-- finishAnswer).
SELECT correct_answer, chosen, correct, response_ms, reviewed_at
FROM reviews
WHERE user_id = $1 AND reviewed_at >= $2 AND item_id IS NOT NULL
ORDER BY reviewed_at;
