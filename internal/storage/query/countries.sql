-- name: UpsertCountry :one
-- Keyed on iso_a2. NOTE: rows with iso_a2 IS NULL (subdivisions without an
-- alpha-2 code) never conflict and always insert a new row — acceptable for
-- now; subdivision seeding dedups by a caller-side lookup (GetCountryByISO on
-- iso_a3, or a future dedicated key) before calling this.
INSERT INTO countries (
  iso_a2, iso_a3, numeric_code, name, official_name, flag_emoji,
  parent_country_id, is_subdivision, un_member, gg_coverage
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (iso_a2) DO UPDATE SET
  iso_a3 = EXCLUDED.iso_a3,
  numeric_code = EXCLUDED.numeric_code,
  name = EXCLUDED.name,
  official_name = EXCLUDED.official_name,
  flag_emoji = EXCLUDED.flag_emoji,
  parent_country_id = EXCLUDED.parent_country_id,
  is_subdivision = EXCLUDED.is_subdivision,
  un_member = EXCLUDED.un_member,
  gg_coverage = EXCLUDED.gg_coverage
RETURNING *;

-- name: GetCountryByISO :one
SELECT * FROM countries WHERE iso_a2 = $1;

-- name: GetCountryByISOA3 :one
SELECT * FROM countries WHERE iso_a3 = $1;

-- name: GetCountryByID :one
SELECT * FROM countries WHERE id = $1;

-- name: ListCountries :many
SELECT * FROM countries ORDER BY name;

-- name: ListCountriesByFlags :many
-- Filter by the first-class un_member/gg_coverage booleans (e.g. the
-- road-side audit's "every gg_coverage country" check).
SELECT * FROM countries WHERE un_member = $1 AND gg_coverage = $2 ORDER BY name;
