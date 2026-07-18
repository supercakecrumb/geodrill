-- Reverse of 000006_v2_exercises_reviews.up.sql.

DROP INDEX IF EXISTS content_items_topic_idx;
ALTER TABLE content_items DROP COLUMN IF EXISTS topic_id;

DROP INDEX IF EXISTS reviews_item_idx;
ALTER TABLE reviews
  DROP COLUMN IF EXISTS correct_answer,
  DROP COLUMN IF EXISTS chosen,
  DROP COLUMN IF EXISTS mode,
  DROP COLUMN IF EXISTS item_id;

DROP INDEX IF EXISTS exercises_item_idx;
ALTER TABLE exercises
  DROP COLUMN IF EXISTS practice,
  DROP COLUMN IF EXISTS is_media,
  DROP COLUMN IF EXISTS correct_answer,
  DROP COLUMN IF EXISTS prompt,
  DROP COLUMN IF EXISTS mode,
  DROP COLUMN IF EXISTS item_id;
