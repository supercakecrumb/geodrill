-- Reverse of 000005_v2_core.up.sql. Views first, then tables in dependency
-- order (children before parents), then the users column.

DROP VIEW IF EXISTS topic_paths;
DROP VIEW IF EXISTS item_tiers;

DROP TABLE IF EXISTS user_tier_progress;
DROP TABLE IF EXISTS user_topics;
DROP TABLE IF EXISTS media_files;
DROP TABLE IF EXISTS country_facts;
DROP TABLE IF EXISTS introductions;
DROP TABLE IF EXISTS user_items;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS fact_defs;
DROP TABLE IF EXISTS countries;
DROP TABLE IF EXISTS topics;

ALTER TABLE users DROP COLUMN IF EXISTS daily_intro_cap;
