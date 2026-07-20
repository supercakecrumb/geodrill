-- name: GetUserItem :one
SELECT * FROM user_items WHERE user_id = $1 AND item_id = $2;

-- name: PutUserItem :exec
-- Upsert the lifecycle + FSRS card state for one user+item (engram.Lifecycle
-- and engram.CardState, architecture §2.3).
INSERT INTO user_items (
  user_id, item_id, lifecycle, due, stability, difficulty, reps, lapses,
  state, last_review, introduced_at, known_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
ON CONFLICT (user_id, item_id)
DO UPDATE SET
  lifecycle = EXCLUDED.lifecycle,
  due = EXCLUDED.due,
  stability = EXCLUDED.stability,
  difficulty = EXCLUDED.difficulty,
  reps = EXCLUDED.reps,
  lapses = EXCLUDED.lapses,
  state = EXCLUDED.state,
  last_review = EXCLUDED.last_review,
  introduced_at = EXCLUDED.introduced_at,
  known_at = EXCLUDED.known_at,
  updated_at = now();

-- name: DeleteUserItemsByTopic :execrows
-- Delete every user_items row for items under a topic — the cities cutover's
-- decided per-user progress reset, so every city becomes re-introducible
-- (biggest-first) via ListCandidateIntroItems's user_items-derived "new"
-- check. Items and their answered exercise/review archive are untouched.
DELETE FROM user_items ui
USING items i
WHERE ui.item_id = i.id AND i.topic_id = $1;

-- name: ListUserItemsByLifecycle :many
SELECT * FROM user_items WHERE user_id = $1 AND lifecycle = $2 ORDER BY updated_at;

-- name: ListDueUserItems :many
-- Due Introduced/Reviewing cards (engram.NextReview candidate set) joined
-- with their item for topic/key/label context. GeoGuessr-only filter: joins
-- the owning user and drops non-coverage items when users.gg_only is set
-- (the predicate is a no-op when it isn't) — no extra parameter, since
-- user_id ($1) already identifies the user whose flag governs the filter.
SELECT ui.user_id, ui.item_id, ui.lifecycle, ui.due, ui.stability, ui.difficulty,
       ui.reps, ui.lapses, ui.state, ui.last_review, ui.introduced_at, ui.known_at,
       ui.updated_at, i.topic_id, i.key, i.label
FROM user_items ui
JOIN items i ON i.id = ui.item_id
JOIN users u ON u.id = sqlc.arg(user_id)
WHERE ui.user_id = sqlc.arg(user_id) AND ui.lifecycle IN (1, 2) AND ui.due <= sqlc.arg(due)
  AND (NOT u.gg_only OR i.gg_relevant)
ORDER BY ui.due;

-- name: CountDueUserItems :one
-- internal/study.Service.DueCount / the reminder loop's due count:
-- Introduced/Reviewing cards due at or before now. GeoGuessr-only filtered
-- (see ListDueUserItems).
SELECT count(*) FROM user_items ui
JOIN items i ON i.id = ui.item_id
JOIN users u ON u.id = sqlc.arg(user_id)
WHERE ui.user_id = sqlc.arg(user_id) AND ui.lifecycle IN (1, 2) AND ui.due <= sqlc.arg(due)
  AND (NOT u.gg_only OR i.gg_relevant);

-- name: ListUserItemCardsInFSRS :many
-- internal/study.Service.Stats' DueForecast input: every Introduced/
-- Reviewing card for a user — replaces ListCardsForUser for the item-based review
-- path. Known/new rows are excluded: they carry a zeroed/absent due date
-- that would otherwise skew engram.DueForecast's "due today" bucket.
-- GeoGuessr-only filtered (see ListDueUserItems).
SELECT ui.* FROM user_items ui
JOIN items i ON i.id = ui.item_id
JOIN users u ON u.id = $1
WHERE ui.user_id = $1 AND ui.lifecycle IN (1, 2)
  AND (NOT u.gg_only OR i.gg_relevant);

-- name: CountIntroducedItems :one
-- internal/study.Service.Stats, "introduced" count: items that have
-- left lifecycle=new (Introduced, Reviewing, or Known). GeoGuessr-only
-- filtered (see ListDueUserItems).
SELECT count(*) FROM user_items ui
JOIN items i ON i.id = ui.item_id
JOIN users u ON u.id = $1
WHERE ui.user_id = $1 AND ui.lifecycle IN (1, 2, 3)
  AND (NOT u.gg_only OR i.gg_relevant);

-- name: CountKnownItems :one
-- internal/study.Service.Stats, "known" count: items marked known via
-- the "I know this" intro outcome. GeoGuessr-only filtered (see
-- ListDueUserItems).
SELECT count(*) FROM user_items ui
JOIN items i ON i.id = ui.item_id
JOIN users u ON u.id = $1
WHERE ui.user_id = $1 AND ui.lifecycle = 3
  AND (NOT u.gg_only OR i.gg_relevant);

-- name: ListCandidateIntroItems :many
-- Candidate items for the introduction queue: active, tier-unlocked
-- (parameterized allowed-tiers array), and either no user_items row yet or
-- still lifecycle=new. Ordered tier, then within-topic position, then topic
-- position — a topic round-robin (items.position is a stable per-topic rank,
-- assigned 0..n at seed time), so consecutive introductions rotate across
-- topics (t0.i0, t1.i0, t2.i0, t0.i1, …) instead of draining one whole topic
-- before the next. NextIntro re-runs this query and takes the first row every
-- call; because position is a rank fixed at seed time (not recomputed over the
-- shrinking candidate set), dropping an introduced row exposes the NEXT
-- topic's leading item, so the rotation holds statelessly across calls. i.id
-- is the final, total-order tiebreak (t.position is only unique among sibling
-- topics, so two leaves under different parents can share it). This is the
-- app-supplied priority order engram.NextIntroductions preserves.
-- GeoGuessr-only filtered (see ListDueUserItems): non-coverage items are
-- never introduced while the user's gg_only is set.
SELECT i.id AS item_id, i.topic_id, i.key, i.label, it.tier
FROM items i
JOIN item_tiers it ON it.item_id = i.id
JOIN topics t ON t.id = i.topic_id
JOIN users u ON u.id = sqlc.arg(user_id)
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = sqlc.arg(user_id)
WHERE i.active = true
  AND (ui.item_id IS NULL OR ui.lifecycle = 0)
  AND it.tier = ANY(sqlc.arg(tiers)::smallint[])
  AND (NOT u.gg_only OR i.gg_relevant)
ORDER BY it.tier, i.position, t.position, i.id;
