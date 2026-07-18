-- Reverse of 000004_add_reminder_config.up.sql.

ALTER TABLE users
  DROP COLUMN reminder_hour,
  DROP COLUMN follow_up_enabled,
  DROP COLUMN follow_up_delay_min;
