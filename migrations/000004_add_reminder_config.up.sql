-- Per-user reminder configuration: the local hour the daily reminder fires,
-- plus a follow-up nudge (its own on/off and delay) sent when the user hasn't
-- engaged within the window. See internal/telegram/bot.go (remindLoop).

ALTER TABLE users
  ADD COLUMN reminder_hour int NOT NULL DEFAULT 9,
  ADD COLUMN follow_up_enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN follow_up_delay_min int NOT NULL DEFAULT 60;
