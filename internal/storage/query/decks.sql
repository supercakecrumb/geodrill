-- name: ListDecks :many
SELECT * FROM decks ORDER BY created_at, slug;

-- name: GetDeckBySlug :one
SELECT * FROM decks WHERE slug = $1;

-- name: UpsertDeck :one
INSERT INTO decks (slug, name)
VALUES ($1, $2)
ON CONFLICT (slug)
DO UPDATE SET name = EXCLUDED.name
RETURNING *;

-- name: UpsertSkill :one
INSERT INTO skills (deck_id, key, label)
VALUES ($1, $2, $3)
ON CONFLICT (deck_id, key)
DO UPDATE SET label = EXCLUDED.label
RETURNING *;

-- name: ListSkillsByDeck :many
SELECT * FROM skills WHERE deck_id = $1 ORDER BY key;

-- name: SetUserDeckEnabled :exec
INSERT INTO user_decks (user_id, deck_id, enabled)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, deck_id)
DO UPDATE SET enabled = EXCLUDED.enabled;

-- name: ListUserDecks :many
SELECT d.id, d.slug, d.name, d.exercise_type, d.created_at,
       COALESCE(ud.enabled, false) AS enabled
FROM decks d
LEFT JOIN user_decks ud ON ud.deck_id = d.id AND ud.user_id = $1
ORDER BY d.created_at, d.slug;

-- name: CountEnabledDecks :one
SELECT count(*) FROM user_decks WHERE user_id = $1 AND enabled = true;

-- name: ListEnabledSkillCards :many
SELECT s.id AS skill_id, s.deck_id, s.key, s.label,
       us.due, us.stability, us.difficulty, us.reps, us.lapses, us.state, us.last_review
FROM skills s
JOIN decks d ON d.id = s.deck_id
JOIN user_decks ud ON ud.deck_id = d.id AND ud.user_id = $1 AND ud.enabled = true
LEFT JOIN user_skills us ON us.skill_id = s.id AND us.user_id = $1
ORDER BY s.deck_id, s.key;

-- name: ListEnabledSkills :many
SELECT s.id AS skill_id, s.deck_id, s.key, s.label
FROM skills s
JOIN decks d ON d.id = s.deck_id
JOIN user_decks ud ON ud.deck_id = d.id AND ud.user_id = $1 AND ud.enabled = true
ORDER BY s.deck_id, s.key;
