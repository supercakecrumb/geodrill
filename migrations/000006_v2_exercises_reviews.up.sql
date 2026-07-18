-- geodrill v2 exercises/reviews (architecture contract §2.5, transitional).
-- Adds mode-aware, item-based columns to exercises/reviews alongside the
-- legacy skill_id/chosen_key/correct_key columns (dropped later in 000007,
-- once the old-quiz data migration backfills item_id on every row). Also
-- scopes content_items to a topic (nullable = shared across topics).

ALTER TABLE exercises
  ADD COLUMN item_id       uuid REFERENCES items(id),   -- nullable for now; backfilled in the old-quiz migration
  ADD COLUMN mode          smallint NOT NULL DEFAULT 0, -- quiz.Mode
  ADD COLUMN prompt        text,                        -- rendered prompt (char / word / sentence cached)
  ADD COLUMN correct_answer text,                       -- serialized key / sorted-set / canonical spelling
  ADD COLUMN is_media      boolean NOT NULL DEFAULT false, -- photo message (edit caption+markup) vs text
  ADD COLUMN practice      boolean NOT NULL DEFAULT false;
CREATE INDEX exercises_item_idx ON exercises(item_id);

ALTER TABLE reviews
  ADD COLUMN item_id        uuid REFERENCES items(id),  -- nullable; legacy skill_id kept until 000007
  ADD COLUMN mode           smallint NOT NULL DEFAULT 0, -- quiz.Mode
  ADD COLUMN chosen         text,                        -- generalized chosen_key/serialized-set/typed string; nullable alongside legacy chosen_key
  ADD COLUMN correct_answer text;                        -- generalized correct_key/serialized-set/canonical spelling; nullable alongside legacy correct_key
CREATE INDEX reviews_item_idx ON reviews(item_id);

ALTER TABLE content_items
  ADD COLUMN topic_id uuid REFERENCES topics(id) ON DELETE CASCADE; -- NULL = shared across topics
CREATE INDEX content_items_topic_idx ON content_items(topic_id);
