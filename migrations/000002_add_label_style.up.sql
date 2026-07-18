-- Adds a per-user preference for how answer-button labels are rendered
-- (flag+name, flag+code, or plain name — see internal/telegram/flags.go).

ALTER TABLE users ADD COLUMN label_style text NOT NULL DEFAULT 'name';
