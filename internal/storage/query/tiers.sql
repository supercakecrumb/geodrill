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
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = $1
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
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = $1
WHERE t.tier = $2
GROUP BY t.tier;
