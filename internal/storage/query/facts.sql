-- name: UpsertFactDef :one
INSERT INTO fact_defs (key, label, value_type, unit, cardinality, dataset)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (key) DO UPDATE SET
  label = EXCLUDED.label,
  value_type = EXCLUDED.value_type,
  unit = EXCLUDED.unit,
  cardinality = EXCLUDED.cardinality,
  dataset = EXCLUDED.dataset
RETURNING *;

-- name: GetFactDefByKey :one
SELECT * FROM fact_defs WHERE key = $1;

-- name: ListFactDefs :many
SELECT * FROM fact_defs ORDER BY key;

-- name: InsertCountryFact :one
-- Typed insert: the caller sets exactly one of val_text/val_num/val_bool (the
-- CHECK constraint enforces it); the other two are passed as NULL.
INSERT INTO country_facts (country_id, fact_def_id, val_text, val_num, val_bool, source, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: DeleteCountryFactsByDef :exec
-- Clears every fact value for one country+def (used to replace multi-valued
-- facts wholesale on reseed).
DELETE FROM country_facts WHERE country_id = $1 AND fact_def_id = $2;

-- name: ListCountryFactsByDefKey :many
-- Facts for one fact_def, looked up by its key (e.g. 'drives_on') — the
-- building block for arbitrary-filter joins (architecture §2.7).
SELECT cf.*
FROM country_facts cf
JOIN fact_defs fd ON fd.id = cf.fact_def_id
WHERE fd.key = $1
ORDER BY cf.country_id;

-- name: ListFactsForCountry :many
SELECT cf.id, cf.country_id, cf.fact_def_id, fd.key AS fact_key, cf.val_text,
       cf.val_num, cf.val_bool, cf.source, cf.observed_at, cf.created_at
FROM country_facts cf
JOIN fact_defs fd ON fd.id = cf.fact_def_id
WHERE cf.country_id = $1
ORDER BY fd.key;
