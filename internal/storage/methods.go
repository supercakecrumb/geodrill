package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// ── users ───────────────────────────────────────────────────────────────────

func userFrom(u db.User) User {
	return User{
		ID:               u.ID,
		TelegramID:       u.TelegramID,
		Username:         u.Username.String,
		DailyNewCap:      int(u.DailyNewCap),
		RemindersEnabled: u.RemindersEnabled,
		ReminderHour:     int(u.ReminderHour),
		FollowUpEnabled:  u.FollowUpEnabled,
		FollowUpDelayMin: int(u.FollowUpDelayMin),
		LabelStyle:       u.LabelStyle,
		Timezone:         u.Timezone,
		CreatedAt:        tsTime(u.CreatedAt),
		DailyIntroCap:    int(u.DailyIntroCap),
	}
}

// UpsertUser registers a Telegram user (or refreshes their username).
func (s *Store) UpsertUser(ctx context.Context, telegramID int64, username string) (User, error) {
	u, err := s.q.UpsertUser(ctx, db.UpsertUserParams{
		TelegramID: telegramID,
		Username:   pgText(username),
	})
	if err != nil {
		return User{}, err
	}
	return userFrom(u), nil
}

// GetUserByTelegramID looks up a user by Telegram id.
func (s *Store) GetUserByTelegramID(ctx context.Context, telegramID int64) (User, bool, error) {
	u, err := s.q.GetUserByTelegramID(ctx, telegramID)
	if IsNotFound(err) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return userFrom(u), true, nil
}

// GetUserByID looks up a user by primary key.
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	u, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	return userFrom(u), nil
}

// SetDailyCap sets the per-user daily new-skill cap.
func (s *Store) SetDailyCap(ctx context.Context, userID uuid.UUID, cap int) error {
	return s.q.SetDailyCap(ctx, db.SetDailyCapParams{ID: userID, DailyNewCap: int32(cap)})
}

// SetReminders toggles the daily reminder for a user.
func (s *Store) SetReminders(ctx context.Context, userID uuid.UUID, enabled bool) error {
	return s.q.SetReminders(ctx, db.SetRemindersParams{ID: userID, RemindersEnabled: enabled})
}

// SetLabelStyle sets the user's answer-button label style ("name", "code", or "plain").
func (s *Store) SetLabelStyle(ctx context.Context, userID uuid.UUID, style string) error {
	return s.q.SetLabelStyle(ctx, db.SetLabelStyleParams{ID: userID, LabelStyle: style})
}

// SetReminderHour sets the local hour (0–23) the daily reminder fires.
func (s *Store) SetReminderHour(ctx context.Context, userID uuid.UUID, hour int) error {
	return s.q.SetReminderHour(ctx, db.SetReminderHourParams{ID: userID, ReminderHour: int32(hour)})
}

// SetFollowUpEnabled toggles the follow-up nudge for a user.
func (s *Store) SetFollowUpEnabled(ctx context.Context, userID uuid.UUID, enabled bool) error {
	return s.q.SetFollowUpEnabled(ctx, db.SetFollowUpEnabledParams{ID: userID, FollowUpEnabled: enabled})
}

// SetFollowUpDelay sets the minutes after the first reminder before a follow-up.
func (s *Store) SetFollowUpDelay(ctx context.Context, userID uuid.UUID, minutes int) error {
	return s.q.SetFollowUpDelay(ctx, db.SetFollowUpDelayParams{ID: userID, FollowUpDelayMin: int32(minutes)})
}

// SetTimezone sets the user's IANA timezone.
func (s *Store) SetTimezone(ctx context.Context, userID uuid.UUID, tz string) error {
	return s.q.SetTimezone(ctx, db.SetTimezoneParams{ID: userID, Timezone: tz})
}

// UsersWithReminders lists users who opted into reminders.
func (s *Store) UsersWithReminders(ctx context.Context) ([]User, error) {
	rows, err := s.q.UsersWithReminders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]User, len(rows))
	for i, u := range rows {
		out[i] = userFrom(u)
	}
	return out, nil
}

// ── content ─────────────────────────────────────────────────────────────────

// InsertContent inserts one content item (idempotent on kind,key,payload).
func (s *Store) InsertContent(ctx context.Context, kind, key, payload, source string, charLen int) error {
	return s.q.InsertContent(ctx, db.InsertContentParams{
		Kind:       kind,
		Key:        key,
		Payload:    payload,
		Source:     source,
		CharLength: int32(charLen),
	})
}

// CountContentByKey counts content items for a key.
func (s *Store) CountContentByKey(ctx context.Context, kind, key string) (int, error) {
	n, err := s.q.CountContentByKey(ctx, db.CountContentByKeyParams{Kind: kind, Key: key})
	return int(n), err
}

// SampleContent returns a random sentence for a skill key, excluding the user's
// last-50 seen. found=false means the (excluded) pool is empty.
func (s *Store) SampleContent(ctx context.Context, userID uuid.UUID, key string) (Content, bool, error) {
	c, err := s.q.SampleContent(ctx, db.SampleContentParams{Key: key, UserID: userID})
	if IsNotFound(err) {
		return Content{}, false, nil
	}
	if err != nil {
		return Content{}, false, err
	}
	return Content{ID: c.ID, Kind: c.Kind, Key: c.Key, Payload: c.Payload, Source: c.Source, CharLength: int(c.CharLength)}, true, nil
}

// GetContentByID fetches one content item by primary key. found=false means
// the row no longer exists (e.g. content was deleted or re-ingested).
func (s *Store) GetContentByID(ctx context.Context, id uuid.UUID) (Content, bool, error) {
	c, err := s.q.GetContentByID(ctx, id)
	if IsNotFound(err) {
		return Content{}, false, nil
	}
	if err != nil {
		return Content{}, false, err
	}
	return Content{ID: c.ID, Kind: c.Kind, Key: c.Key, Payload: c.Payload, Source: c.Source, CharLength: int(c.CharLength)}, true, nil
}

// GetContentByKindKey looks up a content row by its exact (kind, key) pair —
// unlike SampleContent/SampleContentAny (hardcoded to kind='sentence',
// picked randomly among possibly-many rows), used by internal/study's
// bridge content row (a single, deterministically-keyed placeholder).
func (s *Store) GetContentByKindKey(ctx context.Context, kind, key string) (Content, bool, error) {
	c, err := s.q.GetContentByKindKey(ctx, db.GetContentByKindKeyParams{Kind: kind, Key: key})
	if IsNotFound(err) {
		return Content{}, false, nil
	}
	if err != nil {
		return Content{}, false, err
	}
	return Content{ID: c.ID, Kind: c.Kind, Key: c.Key, Payload: c.Payload, Source: c.Source, CharLength: int(c.CharLength)}, true, nil
}

// SampleContentAny returns a random sentence for a key with no exclusion.
func (s *Store) SampleContentAny(ctx context.Context, key string) (Content, bool, error) {
	c, err := s.q.SampleContentAny(ctx, key)
	if IsNotFound(err) {
		return Content{}, false, nil
	}
	if err != nil {
		return Content{}, false, err
	}
	return Content{ID: c.ID, Kind: c.Kind, Key: c.Key, Payload: c.Payload, Source: c.Source, CharLength: int(c.CharLength)}, true, nil
}

// ── exercises ───────────────────────────────────────────────────────────────

// SetExerciseMessageID records the Telegram message id for an exercise.
func (s *Store) SetExerciseMessageID(ctx context.Context, exerciseID uuid.UUID, messageID int64) error {
	return s.q.SetExerciseMessageID(ctx, db.SetExerciseMessageIDParams{
		ID:        exerciseID,
		MessageID: pgInt8(messageID),
	})
}

// MarkExerciseAnswered atomically flips answered_at only if still open. It
// returns true when this caller won the race (owns the answer); false means the
// exercise was already answered (a stale tap).
func (s *Store) MarkExerciseAnswered(ctx context.Context, exerciseID uuid.UUID, at time.Time) (bool, error) {
	_, err := s.q.MarkExerciseAnswered(ctx, db.MarkExerciseAnsweredParams{
		ID:         exerciseID,
		AnsweredAt: timeTs(at),
	})
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ── reviews ─────────────────────────────────────────────────────────────────

// ListReviewsSince returns reviews at or after since, oldest first.
func (s *Store) ListReviewsSince(ctx context.Context, userID uuid.UUID, since time.Time) ([]ReviewRecord, error) {
	rows, err := s.q.ListReviewsSince(ctx, db.ListReviewsSinceParams{UserID: userID, ReviewedAt: timeTs(since)})
	if err != nil {
		return nil, err
	}
	out := make([]ReviewRecord, len(rows))
	for i, r := range rows {
		out[i] = ReviewRecord{
			ItemID:           r.ItemID,
			Rating:           r.Rating,
			ReviewedAt:       tsTime(r.ReviewedAt),
			StabilityBefore:  r.StabilityBefore,
			DifficultyBefore: r.DifficultyBefore,
			StabilityAfter:   r.StabilityAfter,
			DifficultyAfter:  r.DifficultyAfter,
			StateBefore:      r.StateBefore,
			ScheduledDays:    int(r.ScheduledDays),
			ElapsedDays:      int(r.ElapsedDays),
			Correct:          r.Correct,
			Chosen:           r.Chosen.String,
			CorrectAnswer:    r.CorrectAnswer.String,
			Practice:         r.Practice,
		}
	}
	return out, nil
}

// CountReviewsSince counts reviews at or after since.
func (s *Store) CountReviewsSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	n, err := s.q.CountReviewsSince(ctx, db.CountReviewsSinceParams{UserID: userID, ReviewedAt: timeTs(since)})
	return int(n), err
}

// TopicStat is aggregate accuracy for one topic over a window.
type TopicStat struct {
	TopicID uuid.UUID
	Name    string
	Total   int
	Correct int
}

// ReviewStatsByTopic returns per-topic accuracy aggregates since a time,
// restricted to item-based attempts (item_id IS NOT NULL) — the /stats view.
func (s *Store) ReviewStatsByTopic(ctx context.Context, userID uuid.UUID, since time.Time) ([]TopicStat, error) {
	rows, err := s.q.ReviewStatsByTopic(ctx, db.ReviewStatsByTopicParams{UserID: userID, ReviewedAt: timeTs(since)})
	if err != nil {
		return nil, err
	}
	out := make([]TopicStat, len(rows))
	for i, r := range rows {
		out[i] = TopicStat{TopicID: r.TopicID, Name: r.Name, Total: int(r.Total), Correct: int(r.Correct)}
	}
	return out, nil
}

// Attempt is the minimal per-answer record for quiz.Confusion, computed
// over item-based attempts: CorrectAnswer/Chosen are the generalized,
// mode-serialized strings (architecture §2.5).
type Attempt struct {
	CorrectAnswer string
	Chosen        string
	Correct       bool
	ResponseMS    int
	AnsweredAt    time.Time
}

// ListAttemptsSince returns item-based answer records (item_id IS NOT NULL) for
// quiz.Confusion.
func (s *Store) ListAttemptsSince(ctx context.Context, userID uuid.UUID, since time.Time) ([]Attempt, error) {
	rows, err := s.q.ListAttemptsSince(ctx, db.ListAttemptsSinceParams{UserID: userID, ReviewedAt: timeTs(since)})
	if err != nil {
		return nil, err
	}
	out := make([]Attempt, len(rows))
	for i, r := range rows {
		out[i] = Attempt{
			CorrectAnswer: r.CorrectAnswer.String,
			Chosen:        r.Chosen.String,
			Correct:       r.Correct,
			ResponseMS:    int4Int(r.ResponseMs),
			AnsweredAt:    tsTime(r.ReviewedAt),
		}
	}
	return out, nil
}

// ── game stats ──────────────────────────────────────────────────────────────

func gameStatsFrom(g db.GameStat) GameStats {
	return GameStats{
		UserID:       g.UserID,
		Game:         g.Game,
		BestStreak:   int(g.BestStreak),
		Runs:         int(g.Runs),
		LastPlayedAt: tsTime(g.LastPlayedAt),
	}
}

// RecordGameRun upserts the end of one game-zone run (game-zone design doc
// "Persistence"): best_streak only ever grows (GREATEST against any
// existing row), runs increments by one, and last_played_at is stamped to
// at. Implements internal/game's StatsStore.
func (s *Store) RecordGameRun(ctx context.Context, userID uuid.UUID, gameKey string, streak int, at time.Time) (GameStats, error) {
	g, err := s.q.UpsertGameRun(ctx, db.UpsertGameRunParams{
		UserID:       userID,
		Game:         gameKey,
		BestStreak:   int32(streak),
		LastPlayedAt: timeTs(at),
	})
	if err != nil {
		return GameStats{}, err
	}
	return gameStatsFrom(g), nil
}

// GetGameStats returns userID's persisted aggregate for gameKey. found=false
// means the user has never played this game (no game_stats row yet).
func (s *Store) GetGameStats(ctx context.Context, userID uuid.UUID, gameKey string) (GameStats, bool, error) {
	g, err := s.q.GetGameStats(ctx, db.GetGameStatsParams{UserID: userID, Game: gameKey})
	if IsNotFound(err) {
		return GameStats{}, false, nil
	}
	if err != nil {
		return GameStats{}, false, err
	}
	return gameStatsFrom(g), true, nil
}
