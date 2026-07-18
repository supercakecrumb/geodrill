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
