-- name: UpsertTierProgress :exec
INSERT INTO user_tier_progress (user_id, tier, total_items, introduced_items, good_shape_items, complete)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, tier) DO UPDATE SET
  total_items = EXCLUDED.total_items,
  introduced_items = EXCLUDED.introduced_items,
  good_shape_items = EXCLUDED.good_shape_items,
  complete = EXCLUDED.complete,
  updated_at = now();

-- name: ListTierProgressForUser :many
SELECT * FROM user_tier_progress WHERE user_id = $1 ORDER BY tier;

-- name: RecomputeTierProgress :many
-- On-the-fly source of truth for every tier of one user (architecture §4.2):
-- totals, introduced, and "good shape" counts via a single GROUP BY over
-- item_tiers <-> items <-> user_items. Good-shape = known (lifecycle=3) OR
-- graduated-and-durable (state=Review(2) AND stability>=21d, §4.1).
SELECT t.tier,
       count(*)::int AS total_items,
       count(*) FILTER (WHERE ui.lifecycle IS NOT NULL AND ui.lifecycle <> 0)::int AS introduced_items,
       count(*) FILTER (WHERE ui.lifecycle = 3
                           OR (ui.state = 2 AND ui.stability >= 21))::int AS good_shape_items
FROM item_tiers t
JOIN items i ON i.id = t.item_id
JOIN users u ON u.id = $1
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = $1
WHERE (NOT u.gg_only OR i.gg_relevant)
GROUP BY t.tier
ORDER BY t.tier;

-- name: RecomputeTierProgressForTier :one
-- Single-tier variant of RecomputeTierProgress, for the per-answer /
-- per-introduction transactional recompute (architecture §4.2/§5.5: "only the
-- item's tier needs recompute").
SELECT t.tier,
       count(*)::int AS total_items,
       count(*) FILTER (WHERE ui.lifecycle IS NOT NULL AND ui.lifecycle <> 0)::int AS introduced_items,
       count(*) FILTER (WHERE ui.lifecycle = 3
                           OR (ui.state = 2 AND ui.stability >= 21))::int AS good_shape_items
FROM item_tiers t
JOIN items i ON i.id = t.item_id
JOIN users u ON u.id = sqlc.arg(user_id)
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = sqlc.arg(user_id)
WHERE t.tier = sqlc.arg(tier)
  AND (NOT u.gg_only OR i.gg_relevant)
GROUP BY t.tier;

-- name: RecomputeTopicProgress :one
-- internal/study.TopicService, architecture §5.2 TopicRow: aggregate
-- progress across an ENTIRE topic subtree (the topic itself plus every
-- descendant, via the topic_paths recursive view) for one user — a
-- container topic like "languages" rolls up every quizzable topic beneath
-- it (e.g. special-characters, guess-the-language/*, common-words) into one
-- total/introduced/good-shape line. Always returns exactly one row (zeros
-- when the subtree has no items).
WITH target AS (SELECT path FROM topic_paths WHERE topic_paths.id = $2)
SELECT
  count(*)::int AS total_items,
  count(*) FILTER (WHERE ui.lifecycle IS NOT NULL AND ui.lifecycle <> 0)::int AS introduced_items,
  count(*) FILTER (WHERE ui.lifecycle = 3
                      OR (ui.state = 2 AND ui.stability >= 21))::int AS good_shape_items
FROM items i
JOIN topic_paths tp ON tp.id = i.topic_id
CROSS JOIN target
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = $1
WHERE tp.path = target.path OR tp.path LIKE target.path || '/%';

-- name: ListDistinctTiersUnderTopic :many
-- internal/study.TopicService: every effective tier used by an item
-- anywhere in a topic's subtree (itself + descendants) — the input to the
-- 🔒 AnyLocked/LockedTier badge (architecture §5.2), by comparing against
-- the user's currently-unlocked tier set.
WITH target AS (SELECT path FROM topic_paths WHERE topic_paths.id = $1)
SELECT DISTINCT it.tier
FROM items i
JOIN topic_paths tp ON tp.id = i.topic_id
JOIN item_tiers it ON it.item_id = i.id
CROSS JOIN target
WHERE tp.path = target.path OR tp.path LIKE target.path || '/%'
ORDER BY it.tier;

-- name: RecomputeTopicTierBreakdown :many
-- internal/study.TopicService, architecture §5.2 TierRow: per-tier
-- progress within ONE quizzable topic's OWN items (non-recursive — a
-- quizzable topic holds items directly, never a mix of items and child
-- topics), for one user.
SELECT it.tier,
       count(*)::int AS total_items,
       count(*) FILTER (WHERE ui.lifecycle IS NOT NULL AND ui.lifecycle <> 0)::int AS introduced_items,
       count(*) FILTER (WHERE ui.lifecycle = 3
                           OR (ui.state = 2 AND ui.stability >= 21))::int AS good_shape_items
FROM items i
JOIN item_tiers it ON it.item_id = i.id
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = $1
WHERE i.topic_id = $2
GROUP BY it.tier
ORDER BY it.tier;
