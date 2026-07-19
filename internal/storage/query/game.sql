-- name: UpsertGameRun :one
-- Records the end of one game-zone run (design doc "Persistence"):
-- best_streak only ever grows (GREATEST against the existing row), runs
-- increments by one, and last_played_at is stamped to when the run ended.
-- No per-run log — reviews stays FSRS-only; run state (streak, open
-- answer, used content ids) lives in-memory per chat in the telegram layer.
INSERT INTO game_stats (user_id, game, best_streak, runs, last_played_at)
VALUES ($1, $2, $3, 1, $4)
ON CONFLICT (user_id, game) DO UPDATE SET
  best_streak = GREATEST(game_stats.best_streak, EXCLUDED.best_streak),
  runs = game_stats.runs + 1,
  last_played_at = EXCLUDED.last_played_at
RETURNING *;

-- name: GetGameStats :one
SELECT * FROM game_stats WHERE user_id = $1 AND game = $2;
