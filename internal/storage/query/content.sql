-- name: InsertContent :exec
INSERT INTO content_items (kind, key, payload, source, char_length)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (kind, key, payload) DO NOTHING;

-- name: CountContentByKey :one
SELECT count(*) FROM content_items WHERE kind = $1 AND key = $2;

-- name: SampleContent :one
-- Random sentence for a skill key, excluding the user's last-50 seen content
-- for that key (recently-seen exclusion, contract §4).
SELECT ci.id, ci.kind, ci.key, ci.payload, ci.source, ci.char_length
FROM content_items ci
WHERE ci.kind = 'sentence' AND ci.key = $1
  AND ci.id NOT IN (
    SELECT r.content_id
    FROM reviews r
    WHERE r.user_id = $2 AND r.correct_key = $1 AND r.content_id IS NOT NULL
    ORDER BY r.reviewed_at DESC
    LIMIT 50
  )
ORDER BY random()
LIMIT 1;

-- name: GetContentByID :one
SELECT ci.id, ci.kind, ci.key, ci.payload, ci.source, ci.char_length
FROM content_items ci
WHERE ci.id = $1;

-- name: SampleContentAny :one
-- Fallback sampler with no exclusion (used when the pool is smaller than the
-- exclusion window).
SELECT ci.id, ci.kind, ci.key, ci.payload, ci.source, ci.char_length
FROM content_items ci
WHERE ci.kind = 'sentence' AND ci.key = $1
ORDER BY random()
LIMIT 1;
