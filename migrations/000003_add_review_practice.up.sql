-- Adds a flag distinguishing /practice answers (counted in stats, never
-- scheduled) from scheduled answers (both counted and scheduled). See
-- internal/train/service.go schedule() / recordPractice().

ALTER TABLE reviews ADD COLUMN practice boolean NOT NULL DEFAULT false;
