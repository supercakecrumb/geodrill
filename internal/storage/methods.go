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

// ── decks & skills ──────────────────────────────────────────────────────────

// ListDecks returns every deck.
func (s *Store) ListDecks(ctx context.Context) ([]Deck, error) {
	rows, err := s.q.ListDecks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Deck, len(rows))
	for i, d := range rows {
		out[i] = Deck{ID: d.ID, Slug: d.Slug, Name: d.Name, ExerciseType: d.ExerciseType}
	}
	return out, nil
}

// GetDeckBySlug looks up a deck by slug.
func (s *Store) GetDeckBySlug(ctx context.Context, slug string) (Deck, bool, error) {
	d, err := s.q.GetDeckBySlug(ctx, slug)
	if IsNotFound(err) {
		return Deck{}, false, nil
	}
	if err != nil {
		return Deck{}, false, err
	}
	return Deck{ID: d.ID, Slug: d.Slug, Name: d.Name, ExerciseType: d.ExerciseType}, true, nil
}

// UpsertDeck inserts or updates a deck by slug (ingest seeding).
func (s *Store) UpsertDeck(ctx context.Context, slug, name string) (Deck, error) {
	d, err := s.q.UpsertDeck(ctx, db.UpsertDeckParams{Slug: slug, Name: name})
	if err != nil {
		return Deck{}, err
	}
	return Deck{ID: d.ID, Slug: d.Slug, Name: d.Name, ExerciseType: d.ExerciseType}, nil
}

// UpsertSkill inserts or updates a skill by (deck, key) (ingest seeding).
func (s *Store) UpsertSkill(ctx context.Context, deckID uuid.UUID, key, label string) (Skill, error) {
	sk, err := s.q.UpsertSkill(ctx, db.UpsertSkillParams{DeckID: deckID, Key: key, Label: label})
	if err != nil {
		return Skill{}, err
	}
	return Skill{ID: sk.ID, DeckID: sk.DeckID, Key: sk.Key, Label: sk.Label}, nil
}

// ListSkillsByDeck returns a deck's skills.
func (s *Store) ListSkillsByDeck(ctx context.Context, deckID uuid.UUID) ([]Skill, error) {
	rows, err := s.q.ListSkillsByDeck(ctx, deckID)
	if err != nil {
		return nil, err
	}
	out := make([]Skill, len(rows))
	for i, sk := range rows {
		out[i] = Skill{ID: sk.ID, DeckID: sk.DeckID, Key: sk.Key, Label: sk.Label}
	}
	return out, nil
}

// GetSkillByID looks up a single skill by id.
func (s *Store) GetSkillByID(ctx context.Context, skillID uuid.UUID) (Skill, bool, error) {
	sk, err := s.q.GetSkillByID(ctx, skillID)
	if IsNotFound(err) {
		return Skill{}, false, nil
	}
	if err != nil {
		return Skill{}, false, err
	}
	return Skill{ID: sk.ID, DeckID: sk.DeckID, Key: sk.Key, Label: sk.Label}, true, nil
}

// ListAllSkills returns every skill across all decks (label lookups).
func (s *Store) ListAllSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.q.ListAllSkills(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Skill, len(rows))
	for i, sk := range rows {
		out[i] = Skill{ID: sk.ID, DeckID: sk.DeckID, Key: sk.Key, Label: sk.Label}
	}
	return out, nil
}

// ListUserDecks returns all decks with the user's enabled flag.
func (s *Store) ListUserDecks(ctx context.Context, userID uuid.UUID) ([]UserDeck, error) {
	rows, err := s.q.ListUserDecks(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]UserDeck, len(rows))
	for i, r := range rows {
		out[i] = UserDeck{
			Deck:    Deck{ID: r.ID, Slug: r.Slug, Name: r.Name, ExerciseType: r.ExerciseType},
			Enabled: r.Enabled,
		}
	}
	return out, nil
}

// SetUserDeckEnabled toggles a deck for a user.
func (s *Store) SetUserDeckEnabled(ctx context.Context, userID, deckID uuid.UUID, enabled bool) error {
	return s.q.SetUserDeckEnabled(ctx, db.SetUserDeckEnabledParams{UserID: userID, DeckID: deckID, Enabled: enabled})
}

// CountEnabledDecks counts a user's enabled decks.
func (s *Store) CountEnabledDecks(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := s.q.CountEnabledDecks(ctx, userID)
	return int(n), err
}

// ListEnabledSkills returns skills across a user's enabled decks.
func (s *Store) ListEnabledSkills(ctx context.Context, userID uuid.UUID) ([]Skill, error) {
	rows, err := s.q.ListEnabledSkills(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Skill, len(rows))
	for i, r := range rows {
		out[i] = Skill{ID: r.SkillID, DeckID: r.DeckID, Key: r.Key, Label: r.Label}
	}
	return out, nil
}

// ListEnabledSkillCards returns enabled skills with their optional card state.
func (s *Store) ListEnabledSkillCards(ctx context.Context, userID uuid.UUID) ([]SkillCard, error) {
	rows, err := s.q.ListEnabledSkillCards(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]SkillCard, len(rows))
	for i, r := range rows {
		sc := SkillCard{
			Skill:   Skill{ID: r.SkillID, DeckID: r.DeckID, Key: r.Key, Label: r.Label},
			HasCard: r.Due.Valid,
		}
		if sc.HasCard {
			sc.Card = CardFields{
				Due:        tsTime(r.Due),
				Stability:  r.Stability.Float64,
				Difficulty: r.Difficulty.Float64,
				Reps:       int(r.Reps.Int32),
				Lapses:     int(r.Lapses.Int32),
				State:      r.State.Int16,
				LastReview: tsTime(r.LastReview),
			}
		}
		out[i] = sc
	}
	return out, nil
}

// ── cards (user_skills) ─────────────────────────────────────────────────────

// GetCard returns the FSRS state for user+skill. found=false = never scheduled.
func (s *Store) GetCard(ctx context.Context, userID, skillID uuid.UUID) (CardFields, bool, error) {
	r, err := s.q.GetCard(ctx, db.GetCardParams{UserID: userID, SkillID: skillID})
	if IsNotFound(err) {
		return CardFields{}, false, nil
	}
	if err != nil {
		return CardFields{}, false, err
	}
	return CardFields{
		Due:        tsTime(r.Due),
		Stability:  r.Stability,
		Difficulty: r.Difficulty,
		Reps:       int(r.Reps),
		Lapses:     int(r.Lapses),
		State:      r.State,
		LastReview: tsTime(r.LastReview),
	}, true, nil
}

// PutCard upserts the FSRS state for user+skill.
func (s *Store) PutCard(ctx context.Context, userID, skillID uuid.UUID, c CardFields) error {
	return s.q.PutCard(ctx, db.PutCardParams{
		UserID:     userID,
		SkillID:    skillID,
		Due:        timeTs(c.Due),
		Stability:  c.Stability,
		Difficulty: c.Difficulty,
		Reps:       int32(c.Reps),
		Lapses:     int32(c.Lapses),
		State:      c.State,
		LastReview: timeTs(c.LastReview),
	})
}

// ListCardsForUser returns every card for a user (for DueForecast).
func (s *Store) ListCardsForUser(ctx context.Context, userID uuid.UUID) ([]CardFields, error) {
	rows, err := s.q.ListCardsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]CardFields, len(rows))
	for i, r := range rows {
		out[i] = CardFields{
			Due:        tsTime(r.Due),
			Stability:  r.Stability,
			Difficulty: r.Difficulty,
			Reps:       int(r.Reps),
			Lapses:     int(r.Lapses),
			State:      r.State,
			LastReview: tsTime(r.LastReview),
		}
	}
	return out, nil
}

// CountDueSkills counts a user's cards due at or before now.
func (s *Store) CountDueSkills(ctx context.Context, userID uuid.UUID, now time.Time) (int, error) {
	n, err := s.q.CountDueSkills(ctx, db.CountDueSkillsParams{UserID: userID, Due: timeTs(now)})
	return int(n), err
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

// InsertExercise creates an open exercise and returns its id.
func (s *Store) InsertExercise(ctx context.Context, userID, skillID, contentID uuid.UUID, optionsJSON []byte) (uuid.UUID, error) {
	r, err := s.q.InsertExercise(ctx, db.InsertExerciseParams{
		UserID:    userID,
		SkillID:   skillID,
		ContentID: contentID,
		Options:   optionsJSON,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return r.ID, nil
}

// SetExerciseMessageID records the Telegram message id for an exercise.
func (s *Store) SetExerciseMessageID(ctx context.Context, exerciseID uuid.UUID, messageID int64) error {
	return s.q.SetExerciseMessageID(ctx, db.SetExerciseMessageIDParams{
		ID:        exerciseID,
		MessageID: pgInt8(messageID),
	})
}

// GetExercise fetches an exercise (open or answered).
func (s *Store) GetExercise(ctx context.Context, exerciseID uuid.UUID) (Exercise, bool, error) {
	e, err := s.q.GetExercise(ctx, exerciseID)
	if IsNotFound(err) {
		return Exercise{}, false, nil
	}
	if err != nil {
		return Exercise{}, false, err
	}
	ex := Exercise{
		ID:         e.ID,
		UserID:     e.UserID,
		SkillID:    e.SkillID,
		ContentID:  e.ContentID,
		Options:    e.Options,
		CreatedAt:  tsTime(e.CreatedAt),
		AnsweredAt: tsTime(e.AnsweredAt),
		Answered:   e.AnsweredAt.Valid,
		MessageID:  e.MessageID.Int64,
		HasMessage: e.MessageID.Valid,
	}
	return ex, true, nil
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

// InsertReview appends a full review row (engram.Review + quiz.Attempt).
func (s *Store) InsertReview(ctx context.Context, r ReviewInsert) error {
	return s.q.InsertReview(ctx, db.InsertReviewParams{
		UserID:           r.UserID,
		SkillID:          r.SkillID,
		ExerciseID:       r.ExerciseID,
		ContentID:        r.ContentID,
		ChosenKey:        r.ChosenKey,
		CorrectKey:       r.CorrectKey,
		Correct:          r.Correct,
		Rating:           r.Rating,
		ResponseMs:       ptrInt4(r.ResponseMS),
		StabilityBefore:  r.StabilityBefore,
		DifficultyBefore: r.DifficultyBefore,
		StabilityAfter:   r.StabilityAfter,
		DifficultyAfter:  r.DifficultyAfter,
		StateBefore:      r.StateBefore,
		ScheduledDays:    int32(r.ScheduledDays),
		ElapsedDays:      int32(r.ElapsedDays),
		ReviewedAt:       timeTs(r.ReviewedAt),
		Practice:         r.Practice,
	})
}

// ListReviewsSince returns reviews at or after since, oldest first.
func (s *Store) ListReviewsSince(ctx context.Context, userID uuid.UUID, since time.Time) ([]ReviewRecord, error) {
	rows, err := s.q.ListReviewsSince(ctx, db.ListReviewsSinceParams{UserID: userID, ReviewedAt: timeTs(since)})
	if err != nil {
		return nil, err
	}
	out := make([]ReviewRecord, len(rows))
	for i, r := range rows {
		out[i] = ReviewRecord{
			SkillID:          r.SkillID,
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
			ChosenKey:        r.ChosenKey,
			CorrectKey:       r.CorrectKey,
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

// PracticeStatsSince returns the number of practice-flagged answers (total and
// correct) for a user since a start time — the tally for one /practice session.
func (s *Store) PracticeStatsSince(ctx context.Context, userID uuid.UUID, since time.Time) (total, correct int, err error) {
	row, err := s.q.PracticeStatsSince(ctx, db.PracticeStatsSinceParams{UserID: userID, ReviewedAt: timeTs(since)})
	if err != nil {
		return 0, 0, err
	}
	return int(row.Total), int(row.Correct), nil
}

// ListAttemptsSince returns answer records for quiz.Confusion.
func (s *Store) ListAttemptsSince(ctx context.Context, userID uuid.UUID, since time.Time) ([]Attempt, error) {
	rows, err := s.q.ListAttemptsSince(ctx, db.ListAttemptsSinceParams{UserID: userID, ReviewedAt: timeTs(since)})
	if err != nil {
		return nil, err
	}
	out := make([]Attempt, len(rows))
	for i, r := range rows {
		out[i] = Attempt{
			SkillID:    r.SkillID,
			CorrectKey: r.CorrectKey,
			ChosenKey:  r.ChosenKey,
			Correct:    r.Correct,
			ResponseMS: int4Int(r.ResponseMs),
			AnsweredAt: tsTime(r.ReviewedAt),
		}
	}
	return out, nil
}

// ReviewStatsByDeck returns per-deck accuracy aggregates since a time.
func (s *Store) ReviewStatsByDeck(ctx context.Context, userID uuid.UUID, since time.Time) ([]DeckStat, error) {
	rows, err := s.q.ReviewStatsByDeck(ctx, db.ReviewStatsByDeckParams{UserID: userID, ReviewedAt: timeTs(since)})
	if err != nil {
		return nil, err
	}
	out := make([]DeckStat, len(rows))
	for i, r := range rows {
		out[i] = DeckStat{Slug: r.Slug, Name: r.Name, Total: int(r.Total), Correct: int(r.Correct)}
	}
	return out, nil
}
