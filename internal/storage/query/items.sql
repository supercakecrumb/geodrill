-- name: UpsertItem :one
INSERT INTO items (topic_id, key, label, tier, payload, country_id, position, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (topic_id, key)
DO UPDATE SET
  label = EXCLUDED.label,
  tier = EXCLUDED.tier,
  payload = EXCLUDED.payload,
  country_id = EXCLUDED.country_id,
  position = EXCLUDED.position,
  active = EXCLUDED.active
RETURNING *;

-- name: GetItemByID :one
SELECT * FROM items WHERE id = $1;

-- name: ListItemsByTopic :many
SELECT * FROM items WHERE topic_id = $1 ORDER BY position, key;

-- name: ListActiveItemsByTopic :many
SELECT * FROM items WHERE topic_id = $1 AND active = true ORDER BY position, key;

-- name: ListItemsWithTierByTopic :many
-- Items for a topic with their effective tier (COALESCE(items.tier,
-- topics.base_tier)) resolved via the item_tiers view.
SELECT i.id, i.topic_id, i.key, i.label, i.tier, i.payload, i.country_id,
       i.position, i.active, i.created_at, it.tier AS effective_tier
FROM items i
JOIN item_tiers it ON it.item_id = i.id
WHERE i.topic_id = $1
ORDER BY i.position, i.key;

-- name: GetItemEffectiveTier :one
SELECT tier FROM item_tiers WHERE item_id = $1;

-- name: ListAllItemKeyLabels :many
-- Global key->label lookup for confusion display. Best-effort: item keys
-- are only unique WITHIN a topic (items' UNIQUE (topic_id, key) constraint),
-- so a key shared by two topics resolves to whichever row this query
-- returns last for it — an acceptable approximation for a "which
-- language/character/word" hint.
SELECT key, label FROM items;
