-- Queries for cmd/ingest's -backfill-v2 mode (architecture §3.4/§3.5, task
-- W4.1): maps legacy skills/user_skills/exercises/reviews onto the v2
-- topics/items/user_items/introductions framework, in the same database.
-- All additive — no schema change, just new queries against existing
-- 000001-000006 tables.

-- name: ListAllUserSkills :many
-- Every user_skills row across all users — the source rows -backfill-v2
-- maps into user_items (there is no existing "list all" query; every other
-- user_skills reader is scoped to one user).
SELECT * FROM user_skills ORDER BY user_id, skill_id;

-- name: InsertUserItemIfAbsent :execrows
-- Backfill-only insert: unlike PutUserItem's ON CONFLICT DO UPDATE (the
-- live app's upsert), this is ON CONFLICT DO NOTHING so a re-run of
-- -backfill-v2 never clobbers a user_items row the live app (or a prior
-- backfill run) already created. Returns the number of rows actually
-- inserted (0 or 1) so the caller knows whether this pair is newly migrated.
INSERT INTO user_items (
  user_id, item_id, lifecycle, due, stability, difficulty, reps, lapses,
  state, last_review, introduced_at, known_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULL
)
ON CONFLICT (user_id, item_id) DO NOTHING;

-- name: CountIntroductionsForItem :one
-- Whether any introduction row (of any outcome) already exists for a
-- user+item — the "if none exists" guard before synthesizing one
-- (architecture §3.5).
SELECT count(*) FROM introductions WHERE user_id = $1 AND item_id = $2;

-- name: InsertSynthesizedIntroduction :exec
-- One synthesized first-exposure introduction row for a migrated user_item:
-- seq=1, outcome=0 (got_it), shown_at=answered_at=introduced_at (architecture
-- §3.5) — satisfies the "already introduced" invariant so migrated items are
-- never re-introduced.
INSERT INTO introductions (user_id, item_id, seq, outcome, shown_at, answered_at)
VALUES ($1, $2, 1, 0, $3, $3);

-- name: BackfillExercisesForSkill :execrows
-- Attach item_id/mode/correct_answer to every still-unmapped exercise row
-- for one legacy skill. correct_answer is the skill's own key: exercises.
-- skill_id is always the exercise's TARGET (correct) skill, so the correct
-- answer key never varies per exercise the way a review's chosen/correct
-- key can.
UPDATE exercises
SET item_id = $2, mode = 0, correct_answer = $3
WHERE skill_id = $1 AND item_id IS NULL;

-- name: BackfillReviewsForSkill :execrows
-- Attach item_id/mode/chosen/correct_answer to every still-unmapped review
-- row for one legacy skill. chosen/correct_answer are copied from each
-- review's own chosen_key/correct_key (a wrong answer's chosen_key differs
-- from correct_key), unlike exercises' fixed per-skill correct_answer.
UPDATE reviews
SET item_id = $2, mode = 0, chosen = chosen_key, correct_answer = correct_key
WHERE skill_id = $1 AND item_id IS NULL;
