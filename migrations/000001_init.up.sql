-- geodrill schema (architecture contract §4, pinned, PostgreSQL 18).
-- Uses native uuidv7() for primary keys (PostgreSQL 18+).

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  telegram_id bigint UNIQUE NOT NULL,
  username text,
  daily_new_cap int NOT NULL DEFAULT 5,
  reminders_enabled boolean NOT NULL DEFAULT true,
  timezone text NOT NULL DEFAULT 'UTC',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE decks (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  slug text UNIQUE NOT NULL,          -- 'romance'
  name text NOT NULL,
  exercise_type text NOT NULL DEFAULT 'language_id',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skills (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  deck_id uuid NOT NULL REFERENCES decks(id) ON DELETE CASCADE,
  key text NOT NULL,                  -- ISO-639-3
  label text NOT NULL,
  UNIQUE (deck_id, key)
);

CREATE TABLE user_decks (
  user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  deck_id uuid REFERENCES decks(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT true,
  PRIMARY KEY (user_id, deck_id)
);

CREATE TABLE user_skills (             -- engram.CardState per user+skill
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  skill_id uuid NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  due timestamptz NOT NULL,
  stability double precision NOT NULL DEFAULT 0,
  difficulty double precision NOT NULL DEFAULT 0,
  reps int NOT NULL DEFAULT 0,
  lapses int NOT NULL DEFAULT 0,
  state smallint NOT NULL DEFAULT 0,
  last_review timestamptz,
  PRIMARY KEY (user_id, skill_id)
);
CREATE INDEX user_skills_due_idx ON user_skills (user_id, due);

CREATE TABLE content_items (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  kind text NOT NULL DEFAULT 'sentence',  -- 'sentence' | 'image'
  key text NOT NULL,                       -- answer key (ISO code; later: country)
  payload text NOT NULL,                   -- sentence text (later: file ref)
  source text NOT NULL,                    -- CC-BY attribution, e.g. 'tatoeba#12345'
  char_length int NOT NULL DEFAULT 0,
  UNIQUE (kind, key, payload)
);
CREATE INDEX content_items_key_idx ON content_items (kind, key);

CREATE TABLE exercises (                  -- open questions; single-use answer guard
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  skill_id uuid NOT NULL REFERENCES skills(id),
  content_id uuid NOT NULL REFERENCES content_items(id),
  options jsonb NOT NULL,                 -- [{"key":..,"label":..}] as shown
  created_at timestamptz NOT NULL DEFAULT now(),
  answered_at timestamptz,                -- NULL = open
  message_id bigint
);

CREATE TABLE reviews (                    -- append-only; engram.Review + quiz.Attempt
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  skill_id uuid NOT NULL REFERENCES skills(id),
  exercise_id uuid REFERENCES exercises(id),
  content_id uuid REFERENCES content_items(id),
  chosen_key text NOT NULL,
  correct_key text NOT NULL,
  correct boolean NOT NULL,
  rating smallint NOT NULL,
  response_ms int,
  stability_before double precision NOT NULL,
  difficulty_before double precision NOT NULL,
  stability_after double precision NOT NULL,
  difficulty_after double precision NOT NULL,
  state_before smallint NOT NULL,
  scheduled_days int NOT NULL,
  elapsed_days int NOT NULL,
  reviewed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_user_time_idx ON reviews (user_id, reviewed_at);
