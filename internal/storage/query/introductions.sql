-- name: InsertIntroduction :one
INSERT INTO introductions (user_id, item_id, seq, outcome, shown_at, answered_at, message_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: NextIntroSeq :one
-- seq for the next introduction row of this user+item (1 = first exposure,
-- >1 = re-view, architecture §2.4).
SELECT COALESCE(MAX(seq), 0) + 1 FROM introductions WHERE user_id = $1 AND item_id = $2;

-- name: AnswerIntroduction :one
UPDATE introductions SET outcome = $2, answered_at = $3 WHERE id = $1 RETURNING *;

-- name: SetIntroductionMessageID :exec
UPDATE introductions SET message_id = $2 WHERE id = $1;

-- name: GetLatestOpenIntroductionForItem :one
SELECT * FROM introductions
WHERE user_id = $1 AND item_id = $2 AND answered_at IS NULL
ORDER BY shown_at DESC
LIMIT 1;

-- name: GetLatestOpenIntroduction :one
-- Latest open (shown, not yet answered) introduction for a user, regardless
-- of item — used to resolve a bare callback/reply to the card still on screen.
SELECT * FROM introductions
WHERE user_id = $1 AND answered_at IS NULL
ORDER BY shown_at DESC
LIMIT 1;

-- name: CountIntroductionsToday :one
-- "Introduced today" for the daily budget (architecture §2.4): distinct items
-- with a first-exposure (seq=1), a genuinely-introduced outcome, inside the
-- caller-supplied local-day [from, to) bounds. Excludes outcome=1
-- (engram.IntroKnown, "I know this"): that outcome never actually introduces
-- the item, so it must not spend the daily intro budget.
SELECT count(DISTINCT item_id) FROM introductions
WHERE user_id = $1 AND seq = 1 AND outcome IS NOT NULL AND outcome != 1
  AND answered_at >= $2 AND answered_at < $3;

-- name: GetIntroductionByID :one
-- internal/study.Service.AnswerIntro: resolve the item an introduction
-- callback refers to.
SELECT * FROM introductions WHERE id = $1;

-- name: AnswerIntroductionOnce :one
-- Single-use answer guard for introductions (mirrors MarkExerciseAnswered):
-- flips outcome/answered_at only if still open. A returned row means this
-- caller owns the answer; no row means it was already answered (a stale
-- second tap on the same intro card, architecture §5.1/§5.5).
UPDATE introductions
SET outcome = $2, answered_at = $3
WHERE id = $1 AND answered_at IS NULL
RETURNING *;
