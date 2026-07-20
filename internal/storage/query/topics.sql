-- name: UpsertRootTopic :one
-- Upsert a root topic (parent_id IS NULL), keyed by the topics_root_slug
-- partial unique index.
INSERT INTO topics (slug, name, position, base_tier, quiz_kind, exercise_modes, is_quizzable, config)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (slug) WHERE parent_id IS NULL
DO UPDATE SET
  name = EXCLUDED.name,
  position = EXCLUDED.position,
  base_tier = EXCLUDED.base_tier,
  quiz_kind = EXCLUDED.quiz_kind,
  exercise_modes = EXCLUDED.exercise_modes,
  is_quizzable = EXCLUDED.is_quizzable,
  config = EXCLUDED.config
RETURNING *;

-- name: UpsertChildTopic :one
-- Upsert a child topic (parent_id NOT NULL), keyed by the topics_sibling_slug
-- partial unique index.
INSERT INTO topics (parent_id, slug, name, position, base_tier, quiz_kind, exercise_modes, is_quizzable, config)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (parent_id, slug) WHERE parent_id IS NOT NULL
DO UPDATE SET
  name = EXCLUDED.name,
  position = EXCLUDED.position,
  base_tier = EXCLUDED.base_tier,
  quiz_kind = EXCLUDED.quiz_kind,
  exercise_modes = EXCLUDED.exercise_modes,
  is_quizzable = EXCLUDED.is_quizzable,
  config = EXCLUDED.config
RETURNING *;

-- name: GetTopicByID :one
SELECT * FROM topics WHERE id = $1;

-- name: RenameTopic :exec
-- Rename a topic in place (slug + display name), preserving its id and all
-- item/exercise/review references — the cities cutover's one-time legacy
-- rename (cities/city-to-country -> cities/city-on-map) so engine.Seed
-- converges the renamed row rather than orphaning its ~4.5k items under a
-- second leaf keyed on the new slug.
UPDATE topics SET slug = $2, name = $3 WHERE id = $1;

-- name: ListRootTopics :many
SELECT * FROM topics WHERE parent_id IS NULL ORDER BY position, slug;

-- name: ListChildTopics :many
SELECT * FROM topics WHERE parent_id = $1 ORDER BY position, slug;

-- name: ListAllTopics :many
SELECT * FROM topics ORDER BY position, slug;

-- name: ReparentTopic :exec
-- Re-parenting is a single-row UPDATE (architecture §2.1) — the tree is tiny,
-- so the topic_paths recursive view stays cheap even after this.
UPDATE topics SET parent_id = $2 WHERE id = $1;

-- name: GetTopicPath :one
-- Path-walk helper: slash-joined slug path + depth from the recursive
-- topic_paths view, for one topic id.
SELECT id, path, depth FROM topic_paths WHERE id = $1;

-- name: GetTopicByPath :one
-- Path-walk helper: resolve a topic by its full slash-joined slug path (e.g.
-- "languages/special-characters"), via topic_paths.
SELECT t.*
FROM topics t
JOIN topic_paths tp ON tp.id = t.id
WHERE tp.path = $1;

-- name: ListTopicPaths :many
SELECT id, path, depth FROM topic_paths ORDER BY path;

-- ── user_topics (per-user topic opt-in/out, §2.10) ─────────────────────────

-- name: SetUserTopicEnabled :exec
INSERT INTO user_topics (user_id, topic_id, enabled)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, topic_id)
DO UPDATE SET enabled = EXCLUDED.enabled;

-- name: ListUserTopics :many
-- Every topic with the user's enabled flag (default-on when no row exists,
-- per architecture §2.10 / §9 open question 5).
SELECT t.id, t.parent_id, t.slug, t.name, t.position, t.base_tier, t.quiz_kind,
       t.exercise_modes, t.is_quizzable, t.config, t.created_at,
       COALESCE(ut.enabled, true) AS enabled
FROM topics t
LEFT JOIN user_topics ut ON ut.topic_id = t.id AND ut.user_id = $1
ORDER BY t.position, t.slug;

-- name: GetUserTopicEnabled :one
-- internal/study.Service, the /topics enable/disable toggle: a single
-- topic's enabled flag for a user (default-on when no user_topics row
-- exists), for rendering the toggle's current state without listing every
-- topic (ListUserTopics).
SELECT COALESCE(ut.enabled, true) AS enabled
FROM topics t
LEFT JOIN user_topics ut ON ut.topic_id = t.id AND ut.user_id = $1
WHERE t.id = $2;

-- name: GetSubtreeQuizzableTopicCounts :one
-- internal/study.TopicService, the /topics group-level "Turn group off/on"
-- toggle (a container's drilled-in view): aggregate enabled-vs-total counts
-- across every QUIZZABLE topic in topicID's subtree (itself + descendants,
-- via the topic_paths recursive view) for one user, in a single set-based
-- query — feeds the button's tri-state read (all on / all off / mixed)
-- without an N+1 per-topic walk.
WITH target AS (SELECT path FROM topic_paths WHERE topic_paths.id = $2)
SELECT
  count(*)::int AS total,
  count(*) FILTER (WHERE COALESCE(ut.enabled, true))::int AS enabled
FROM topics t
JOIN topic_paths tp ON tp.id = t.id
CROSS JOIN target
LEFT JOIN user_topics ut ON ut.topic_id = t.id AND ut.user_id = $1
WHERE t.is_quizzable
  AND (tp.path = target.path OR tp.path LIKE target.path || '/%');

-- name: SetSubtreeTopicsEnabled :exec
-- internal/study.TopicService.SetSubtreeEnabled: the group-level toggle's
-- write side — upserts user_topics.enabled for every QUIZZABLE topic in
-- topicID's subtree (itself + descendants, via topic_paths) in one
-- set-based statement, idempotent like SetUserTopicEnabled above.
WITH target AS (SELECT path FROM topic_paths WHERE topic_paths.id = $2)
INSERT INTO user_topics (user_id, topic_id, enabled)
SELECT $1, t.id, $3
FROM topics t
JOIN topic_paths tp ON tp.id = t.id
CROSS JOIN target
WHERE t.is_quizzable
  AND (tp.path = target.path OR tp.path LIKE target.path || '/%')
ON CONFLICT (user_id, topic_id)
DO UPDATE SET enabled = EXCLUDED.enabled;
