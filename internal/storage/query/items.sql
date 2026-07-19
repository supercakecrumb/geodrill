-- name: UpsertItem :one
INSERT INTO items (topic_id, key, label, tier, payload, country_id, position, active, gg_relevant)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (topic_id, key)
DO UPDATE SET
  label = EXCLUDED.label,
  tier = EXCLUDED.tier,
  payload = EXCLUDED.payload,
  country_id = EXCLUDED.country_id,
  position = EXCLUDED.position,
  active = EXCLUDED.active,
  gg_relevant = EXCLUDED.gg_relevant
RETURNING *;

-- name: UpdateItemsRelevanceByCountry :exec
-- GeoGuessr-coverage relevance pass (cmd/ingest): every country-linked item's
-- gg_relevant mirrors its country's gg_coverage. Language items (country_id
-- IS NULL) are left to the language pass (SetItemRelevance).
UPDATE items i
SET gg_relevant = c.gg_coverage
FROM countries c
WHERE i.country_id = c.id;

-- name: SetItemRelevance :exec
-- Sets one item's gg_relevant explicitly — the language relevance pass
-- (cmd/ingest) uses it for country_id IS NULL language items.
UPDATE items SET gg_relevant = $2 WHERE id = $1;

-- name: ListLanguageItems :many
-- Items with no country link (language topics: special characters, common
-- words, guess-the-language) — the input to the language relevance pass.
SELECT * FROM items WHERE country_id IS NULL ORDER BY topic_id, key;

-- name: ListCoveredLanguageFactValues :many
-- Distinct languages_spoken fact values across every GeoGuessr-covered
-- country — the covered-language set the language relevance pass matches
-- language items against (normalized via seeds/language_coverage.yaml).
SELECT DISTINCT cf.val_text
FROM country_facts cf
JOIN fact_defs fd ON fd.id = cf.fact_def_id
JOIN countries c  ON c.id = cf.country_id
WHERE fd.key = 'languages_spoken'
  AND c.gg_coverage = true
  AND cf.val_text IS NOT NULL;

-- name: ListAllLanguageFactValues :many
-- Distinct languages_spoken fact values across ALL countries (covered or
-- not) — the language relevance pass uses this to tell a genuinely
-- non-covered language (present in the fact data, but only in non-coverage
-- countries → hide) apart from one absent from the fact data entirely
-- (coverage undeterminable → conservatively keep + flag).
SELECT DISTINCT cf.val_text
FROM country_facts cf
JOIN fact_defs fd ON fd.id = cf.fact_def_id
WHERE fd.key = 'languages_spoken'
  AND cf.val_text IS NOT NULL;

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
       i.position, i.active, i.gg_relevant, i.created_at, it.tier AS effective_tier
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
