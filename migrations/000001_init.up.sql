-- geodrill schema (architecture contract, pinned, PostgreSQL 18).
-- Uses native uuidv7() for primary keys (PostgreSQL 18+).
--
-- Single init migration: users/settings, the topic/item framework
-- (topics, countries, items, user_items, introductions, content_items,
-- media_files, fact_defs, country_facts, user_topics, user_tier_progress),
-- the game zone's aggregate stats (game_stats), and the item/mode-based
-- exercises/reviews tables. Order matters for FK resolution: users first;
-- topics/countries before items; items before
-- user_items/introductions/exercises/reviews; content_items before
-- media_files; exercises before reviews; users before game_stats.

-- Users and their settings.
CREATE TABLE users (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  telegram_id          bigint UNIQUE NOT NULL,
  username             text,
  daily_new_cap        int NOT NULL DEFAULT 5,
  daily_intro_cap      int NOT NULL DEFAULT 10,
  reminders_enabled    boolean NOT NULL DEFAULT true,
  timezone             text NOT NULL DEFAULT 'UTC',
  label_style          text NOT NULL DEFAULT 'name',
  reminder_hour        int NOT NULL DEFAULT 9,
  follow_up_enabled    boolean NOT NULL DEFAULT true,
  follow_up_delay_min  int NOT NULL DEFAULT 60,
  gg_only              boolean NOT NULL DEFAULT true,   -- GeoGuessr-only mode: hide non-coverage countries/languages everywhere
  created_at           timestamptz NOT NULL DEFAULT now()
);

-- Topic tree (parent_id + slug; topic_paths view below for subtree reads).
CREATE TABLE topics (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  parent_id      uuid REFERENCES topics(id) ON DELETE CASCADE,      -- NULL = root
  slug           text NOT NULL,                                     -- unique among siblings
  name           text NOT NULL,
  position       int  NOT NULL DEFAULT 0,                           -- sibling ordering in the browser
  base_tier      smallint NOT NULL DEFAULT 0,                       -- default tier for this topic's items
  quiz_kind      text NOT NULL DEFAULT 'container',                 -- 'container'|'language_id'|'char_language'|'road_side'|'word_language'|...
  exercise_modes text[] NOT NULL DEFAULT '{single}',                -- allowed engram modes
  is_quizzable   boolean NOT NULL DEFAULT true,                     -- false = pure container node
  config         jsonb NOT NULL DEFAULT '{}',                       -- per-topic knobs (prompt template, media flag, distractor group ref)
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX topics_sibling_slug ON topics(parent_id, slug) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX topics_root_slug    ON topics(slug)            WHERE parent_id IS NULL;
CREATE INDEX topics_parent_pos          ON topics(parent_id, position);

-- Countries — first-class.
CREATE TABLE countries (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  iso_a2            text UNIQUE,                 -- 'FR','GB'; NULL only for subdivisions without one
  iso_a3            text UNIQUE,                 -- 'FRA'
  numeric_code      text,                        -- '250'
  name              text NOT NULL,
  official_name     text,
  flag_emoji        text,                        -- regional-indicator pair or subdivision tag sequence
  parent_country_id uuid REFERENCES countries(id),   -- England/Scotland/Wales -> GB
  is_subdivision    boolean NOT NULL DEFAULT false,
  un_member         boolean NOT NULL DEFAULT false,
  gg_coverage       boolean NOT NULL DEFAULT false,   -- has official Street View
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX countries_flags_idx ON countries(un_member, gg_coverage);

-- Items — generic quizzable entity.
CREATE TABLE items (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  topic_id    uuid NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  key         text NOT NULL,                    -- stable answer key within the topic (e.g. 'rus', 'set:nor,dan', 'FR')
  label       text NOT NULL,
  tier        smallint,                         -- NULL => inherit topics.base_tier (per-item override)
  payload     jsonb NOT NULL DEFAULT '{}',      -- topic-specific: {"char":"..."} | {"languages":[...]} | {"side":"L"} | {"word":"..."}
  country_id  uuid REFERENCES countries(id),    -- optional link (road-side, flags, profiles)
  position    int  NOT NULL DEFAULT 0,
  active      boolean NOT NULL DEFAULT true,
  gg_relevant boolean NOT NULL DEFAULT true,     -- precomputed at ingest: is this item about a GeoGuessr-covered country/language? (gated per-user by users.gg_only)
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (topic_id, key)
);
CREATE INDEX items_topic_idx        ON items(topic_id);
CREATE INDEX items_country_idx      ON items(country_id);
CREATE INDEX items_effective_tier   ON items(topic_id, COALESCE(tier, 0)); -- effective tier resolved via item_tiers view
CREATE INDEX items_gg_relevant_idx  ON items(gg_relevant);                 -- every study/stats/tier query filters on it under gg_only

-- Per-user per-item lifecycle + FSRS card.
CREATE TABLE user_items (
  user_id       uuid NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
  item_id       uuid NOT NULL REFERENCES items(id)  ON DELETE CASCADE,
  lifecycle     smallint NOT NULL DEFAULT 0,        -- engram.Lifecycle: 0 new,1 introduced,2 reviewing,3 known
  -- engram.CardState (meaningful iff lifecycle IN (1,2)); zeroed otherwise
  due           timestamptz,
  stability     double precision NOT NULL DEFAULT 0,
  difficulty    double precision NOT NULL DEFAULT 0,
  reps          int  NOT NULL DEFAULT 0,
  lapses        int  NOT NULL DEFAULT 0,
  state         smallint NOT NULL DEFAULT 0,        -- FSRS memory state
  last_review   timestamptz,
  introduced_at timestamptz,                        -- when it first left 'new'
  known_at      timestamptz,                        -- when marked known (audit + reversibility)
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, item_id)
);
CREATE INDEX user_items_due_idx       ON user_items(user_id, due) WHERE lifecycle IN (1,2);
CREATE INDEX user_items_lifecycle_idx ON user_items(user_id, lifecycle);

-- Introductions — re-viewable, three-button outcome (append-only event log).
CREATE TABLE introductions (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id     uuid NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
  item_id     uuid NOT NULL REFERENCES items(id)  ON DELETE CASCADE,
  seq         int  NOT NULL DEFAULT 1,             -- 1 = first exposure; >1 = re-view
  outcome     smallint,                            -- NULL = shown, not yet answered; 0 got_it,1 known,2 test_me
  shown_at    timestamptz NOT NULL DEFAULT now(),
  answered_at timestamptz,
  message_id  bigint
);
CREATE INDEX introductions_user_item_idx ON introductions(user_id, item_id);
CREATE INDEX introductions_user_day_idx  ON introductions(user_id, answered_at);

-- Typed, normalized country facts — zero-DDL dataset absorption.
CREATE TABLE fact_defs (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  key         text UNIQUE NOT NULL,        -- 'drives_on','main_religion','region','gdp_per_capita','lgbt_legal','tld'
  label       text NOT NULL,
  value_type  text NOT NULL,               -- 'text'|'number'|'bool'   (enums stored as text)
  unit        text,                        -- 'USD','%',NULL
  cardinality text NOT NULL DEFAULT 'single', -- 'single'|'multi' (languages spoken = multi)
  dataset     text,                        -- provenance grouping: 'baseline','cia_factbook','plonkit',...
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE country_facts (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  country_id  uuid NOT NULL REFERENCES countries(id) ON DELETE CASCADE,
  fact_def_id uuid NOT NULL REFERENCES fact_defs(id) ON DELETE CASCADE,
  val_text    text,
  val_num     double precision,
  val_bool    boolean,
  source      text,                        -- attribution / provenance
  observed_at date,                        -- time-series support (e.g. GDP by year); NULL = current
  created_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (num_nonnulls(val_text, val_num, val_bool) = 1)  -- exactly one typed slot populated
);
CREATE INDEX cf_country_def_idx ON country_facts(country_id, fact_def_id);
CREATE INDEX cf_def_text_idx    ON country_facts(fact_def_id, val_text);
CREATE INDEX cf_def_bool_idx    ON country_facts(fact_def_id, val_bool);
CREATE INDEX cf_def_num_idx     ON country_facts(fact_def_id, val_num);

-- Content items (sentences and, later, image refs) with attribution, scoped
-- to an optional topic.
CREATE TABLE content_items (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  kind        text NOT NULL DEFAULT 'sentence',  -- 'sentence' | 'image'
  key         text NOT NULL,                     -- answer key (ISO code; later: country)
  payload     text NOT NULL,                     -- sentence text (later: file ref)
  source      text NOT NULL,                     -- CC-BY attribution, e.g. 'tatoeba#12345'
  char_length int NOT NULL DEFAULT 0,
  topic_id    uuid REFERENCES topics(id) ON DELETE CASCADE, -- NULL = shared across topics
  UNIQUE (kind, key, payload)
);
CREATE INDEX content_items_key_idx   ON content_items (kind, key);
CREATE INDEX content_items_topic_idx ON content_items (topic_id);

-- Media (photos: local path + telegram file_id cache).
CREATE TABLE media_files (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  content_id       uuid REFERENCES content_items(id) ON DELETE CASCADE,
  local_path       text NOT NULL UNIQUE,
  sha256           text,
  telegram_file_id text,                 -- cached after first upload; reused to skip re-upload
  width int, height int, bytes int,
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- Optional per-user topic opt-in/out.
CREATE TABLE user_topics (
  user_id  uuid NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
  topic_id uuid NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  enabled  boolean NOT NULL DEFAULT true,
  PRIMARY KEY (user_id, topic_id)
);

-- Game zone aggregate stats (game-zone design doc "Persistence"): one row
-- per user per game key (e.g. 'language_roulette'), best streak ever
-- reached, total runs played, and when they last played. No per-run log —
-- reviews stays FSRS-only; run state (streak, open answer, used content
-- ids) lives in-memory per chat in the telegram layer instead.
CREATE TABLE game_stats (
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  game           text NOT NULL,
  best_streak    int  NOT NULL DEFAULT 0,
  runs           int  NOT NULL DEFAULT 0,
  last_played_at timestamptz,
  PRIMARY KEY (user_id, game)
);

-- Tiers + global gating — per-user completion cache (recomputed
-- transactionally on each answer/introduction; on-the-fly query is truth).
CREATE TABLE user_tier_progress (
  user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tier             smallint NOT NULL,
  total_items      int NOT NULL,
  introduced_items int NOT NULL,        -- lifecycle != new
  good_shape_items int NOT NULL,        -- meets the FSRS-shape threshold
  complete         boolean NOT NULL DEFAULT false,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, tier)
);

-- Effective tier per item: COALESCE(items.tier, topics.base_tier).
CREATE VIEW item_tiers AS
SELECT i.id AS item_id, i.topic_id, COALESCE(i.tier, t.base_tier)::smallint AS tier
FROM items i
JOIN topics t ON t.id = i.topic_id;

-- Slash-joined slug path + depth for every topic, via a recursive CTE over
-- parent_id (breadcrumbs / subtree reads; the tree is tiny so this is cheap).
CREATE VIEW topic_paths AS
WITH RECURSIVE tp AS (
  SELECT id, slug::text AS path, 0 AS depth
  FROM topics
  WHERE parent_id IS NULL
  UNION ALL
  SELECT c.id, tp.path || '/' || c.slug, tp.depth + 1
  FROM topics c
  JOIN tp ON c.parent_id = tp.id
)
SELECT id, path, depth FROM tp;

-- Exercises — open questions; single-use answer guard. item_id identifies
-- the quizzed item; mode selects the answer UI (engram/quiz.Mode).
CREATE TABLE exercises (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id        uuid NOT NULL REFERENCES items(id),
  content_id     uuid REFERENCES content_items(id),
  mode           smallint NOT NULL DEFAULT 0,     -- quiz.Mode
  prompt         text,                            -- rendered prompt (char / word / sentence cached)
  options        jsonb NOT NULL,                  -- [{"key":..,"label":..}] as shown
  correct_answer text,                            -- serialized key / sorted-set / canonical spelling
  is_media       boolean NOT NULL DEFAULT false,  -- photo message (edit caption+markup) vs text
  practice       boolean NOT NULL DEFAULT false,
  created_at     timestamptz NOT NULL DEFAULT now(),
  answered_at    timestamptz,                     -- NULL = open
  message_id     bigint
);
CREATE INDEX exercises_item_idx ON exercises(item_id);

-- Reviews — append-only; engram.Review + quiz.Attempt.
CREATE TABLE reviews (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id           uuid NOT NULL REFERENCES items(id),
  exercise_id       uuid REFERENCES exercises(id),
  content_id        uuid REFERENCES content_items(id),
  mode              smallint NOT NULL DEFAULT 0,  -- quiz.Mode
  chosen            text,                         -- generalized chosen answer (serialized key / sorted-set / typed string)
  correct_answer    text,                         -- generalized correct answer (serialized key / sorted-set / canonical spelling)
  correct           boolean NOT NULL,
  rating            smallint NOT NULL,
  response_ms       int,
  stability_before  double precision NOT NULL,
  difficulty_before double precision NOT NULL,
  stability_after   double precision NOT NULL,
  difficulty_after  double precision NOT NULL,
  state_before      smallint NOT NULL,
  scheduled_days    int NOT NULL,
  elapsed_days      int NOT NULL,
  practice          boolean NOT NULL DEFAULT false,
  reviewed_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_user_time_idx ON reviews (user_id, reviewed_at);
CREATE INDEX reviews_item_idx      ON reviews(item_id);
