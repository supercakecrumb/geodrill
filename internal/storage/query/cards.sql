-- name: GetCard :one
SELECT due, stability, difficulty, reps, lapses, state, last_review
FROM user_skills
WHERE user_id = $1 AND skill_id = $2;

-- name: PutCard :exec
INSERT INTO user_skills (user_id, skill_id, due, stability, difficulty, reps, lapses, state, last_review)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (user_id, skill_id)
DO UPDATE SET
  due = EXCLUDED.due,
  stability = EXCLUDED.stability,
  difficulty = EXCLUDED.difficulty,
  reps = EXCLUDED.reps,
  lapses = EXCLUDED.lapses,
  state = EXCLUDED.state,
  last_review = EXCLUDED.last_review;

-- name: ListCardsForUser :many
SELECT due, stability, difficulty, reps, lapses, state, last_review
FROM user_skills
WHERE user_id = $1;

-- name: CountDueSkills :one
SELECT count(*) FROM user_skills
WHERE user_id = $1 AND due <= $2;
