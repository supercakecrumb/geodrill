package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// ExerciseV2 is a mode-aware v2 exercise row (architecture §2.5): the full
// item_id/mode/prompt/options/correct_answer/is_media/practice column set,
// alongside the single-use answered_at guard. Distinct from the legacy
// Exercise (which predates these columns) so existing legacy call sites are
// unaffected by this additive type.
type ExerciseV2 struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	ItemID        *uuid.UUID
	ContentID     *uuid.UUID
	Mode          int16
	Prompt        string
	Options       []byte // jsonb
	CorrectAnswer string
	IsMedia       bool
	Practice      bool
	CreatedAt     time.Time
	AnsweredAt    time.Time // zero if still open
	Answered      bool
	MessageID     int64
	HasMessage    bool
}

func exerciseV2From(e db.Exercise) ExerciseV2 {
	var contentID *uuid.UUID
	if e.ContentID != uuid.Nil {
		id := e.ContentID
		contentID = &id
	}
	messageID, hasMessage := int8ToMessageID(e.MessageID)
	return ExerciseV2{
		ID:            e.ID,
		UserID:        e.UserID,
		ItemID:        e.ItemID,
		ContentID:     contentID,
		Mode:          e.Mode,
		Prompt:        e.Prompt.String,
		Options:       e.Options,
		CorrectAnswer: e.CorrectAnswer.String,
		IsMedia:       e.IsMedia,
		Practice:      e.Practice,
		CreatedAt:     tsTime(e.CreatedAt),
		AnsweredAt:    tsTime(e.AnsweredAt),
		Answered:      e.AnsweredAt.Valid,
		MessageID:     messageID,
		HasMessage:    hasMessage,
	}
}

// exerciseV2FromOpenRow mirrors exerciseV2From for GetOpenExerciseByModeRow —
// a structurally-identical but distinct sqlc row type (its query selects an
// explicit column list rather than exercises.*, so sqlc does not reuse
// db.Exercise for it).
func exerciseV2FromOpenRow(e db.GetOpenExerciseByModeRow) ExerciseV2 {
	var contentID *uuid.UUID
	if e.ContentID != uuid.Nil {
		id := e.ContentID
		contentID = &id
	}
	messageID, hasMessage := int8ToMessageID(e.MessageID)
	return ExerciseV2{
		ID:            e.ID,
		UserID:        e.UserID,
		ItemID:        e.ItemID,
		ContentID:     contentID,
		Mode:          e.Mode,
		Prompt:        e.Prompt.String,
		Options:       e.Options,
		CorrectAnswer: e.CorrectAnswer.String,
		IsMedia:       e.IsMedia,
		Practice:      e.Practice,
		CreatedAt:     tsTime(e.CreatedAt),
		AnsweredAt:    tsTime(e.AnsweredAt),
		Answered:      e.AnsweredAt.Valid,
		MessageID:     messageID,
		HasMessage:    hasMessage,
	}
}

// InsertExerciseV2Params is the argument to InsertExerciseV2.
type InsertExerciseV2Params struct {
	UserID        uuid.UUID
	SkillID       uuid.UUID // legacy NOT NULL column; see internal/study's bridge.go
	ContentID     uuid.UUID // legacy NOT NULL column; real content id, or the bridge placeholder
	ItemID        uuid.UUID
	Mode          int16
	Prompt        string
	Options       []byte // jsonb
	CorrectAnswer string
	IsMedia       bool
	Practice      bool
}

// InsertExerciseV2 inserts a mode-aware exercise row in one shot (architecture
// §2.5), instead of the legacy two-step InsertExercise+SetExerciseItemFields
// path — the sole insertion path internal/study.Service uses.
func (s *Store) InsertExerciseV2(ctx context.Context, p InsertExerciseV2Params) (id uuid.UUID, createdAt time.Time, err error) {
	itemID := p.ItemID
	r, err := s.q.InsertExerciseV2(ctx, db.InsertExerciseV2Params{
		UserID:        p.UserID,
		SkillID:       p.SkillID,
		ContentID:     p.ContentID,
		ItemID:        &itemID,
		Mode:          p.Mode,
		Prompt:        pgText(p.Prompt),
		Options:       p.Options,
		CorrectAnswer: pgText(p.CorrectAnswer),
		IsMedia:       p.IsMedia,
		Practice:      p.Practice,
	})
	if err != nil {
		return uuid.Nil, time.Time{}, err
	}
	return r.ID, tsTime(r.CreatedAt), nil
}

// GetExerciseByIDV2 fetches one exercise by id with the full v2 field set
// (unlike the legacy GetExercise, which predates item_id/mode/prompt/
// correct_answer/is_media/practice). found=false means no such exercise.
func (s *Store) GetExerciseByIDV2(ctx context.Context, id uuid.UUID) (ExerciseV2, bool, error) {
	e, err := s.q.GetExerciseByIDV2(ctx, id)
	if IsNotFound(err) {
		return ExerciseV2{}, false, nil
	}
	if err != nil {
		return ExerciseV2{}, false, err
	}
	return exerciseV2From(e), true, nil
}

// GetOpenExerciseV2ByMode is GetOpenExerciseByMode's full-v2-field-set
// counterpart (same underlying query; the existing GetOpenExerciseByMode
// wrapper maps into the legacy, mode-unaware Exercise type and would
// silently drop item_id/mode/correct_answer — exactly what
// internal/study.Service.AnswerText needs to grade a free-typed answer).
func (s *Store) GetOpenExerciseV2ByMode(ctx context.Context, userID uuid.UUID, mode int16) (ExerciseV2, bool, error) {
	e, err := s.q.GetOpenExerciseByMode(ctx, db.GetOpenExerciseByModeParams{UserID: userID, Mode: mode})
	if IsNotFound(err) {
		return ExerciseV2{}, false, nil
	}
	if err != nil {
		return ExerciseV2{}, false, err
	}
	return exerciseV2FromOpenRow(e), true, nil
}

// SetExerciseItemFields attaches item/mode-aware metadata (architecture §2.5)
// to an existing exercise row. itemID stays nullable until the old-quiz
// migration backfills it (§3.1); pass nil to leave it unset.
func (s *Store) SetExerciseItemFields(ctx context.Context, exerciseID uuid.UUID, itemID *uuid.UUID, mode int16, prompt, correctAnswer string, isMedia, practice bool) error {
	return s.q.SetExerciseItemFields(ctx, db.SetExerciseItemFieldsParams{
		ID:            exerciseID,
		ItemID:        itemID,
		Mode:          mode,
		Prompt:        pgText(prompt),
		CorrectAnswer: pgText(correctAnswer),
		IsMedia:       isMedia,
		Practice:      practice,
	})
}

// GetExercisesByItem returns every exercise (open or answered) created for
// one item, newest first.
func (s *Store) GetExercisesByItem(ctx context.Context, itemID uuid.UUID) ([]Exercise, error) {
	rows, err := s.q.GetExercisesByItem(ctx, &itemID)
	if err != nil {
		return nil, err
	}
	out := make([]Exercise, len(rows))
	for i, e := range rows {
		out[i] = Exercise{
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
	}
	return out, nil
}

// GetOpenExerciseByMode returns the newest open (unanswered) exercise of a
// given quiz.Mode for a user — used to resolve a free-text reply, which
// arrives as a plain message rather than a callback (architecture §5.4).
func (s *Store) GetOpenExerciseByMode(ctx context.Context, userID uuid.UUID, mode int16) (Exercise, bool, error) {
	e, err := s.q.GetOpenExerciseByMode(ctx, db.GetOpenExerciseByModeParams{UserID: userID, Mode: mode})
	if IsNotFound(err) {
		return Exercise{}, false, nil
	}
	if err != nil {
		return Exercise{}, false, err
	}
	return Exercise{
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
	}, true, nil
}

// ReviewInsertV2 is ReviewInsert plus the generalized item_id/mode/chosen/
// correct_answer columns (architecture §2.5, transitional: the legacy
// SkillID/ChosenKey/CorrectKey stay required until 000007 drops them).
type ReviewInsertV2 struct {
	ReviewInsert
	ItemID        *uuid.UUID
	Mode          int16
	Chosen        string
	CorrectAnswer string
}

// InsertReviewV2 appends a review carrying both the legacy and generalized
// columns (architecture §2.5).
func (s *Store) InsertReviewV2(ctx context.Context, r ReviewInsertV2) error {
	return s.q.InsertReviewV2(ctx, db.InsertReviewV2Params{
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
		ItemID:           r.ItemID,
		Mode:             r.Mode,
		Chosen:           pgText(r.Chosen),
		CorrectAnswer:    pgText(r.CorrectAnswer),
	})
}

// GetReviewsByItem returns every review recorded against one item, oldest first.
func (s *Store) GetReviewsByItem(ctx context.Context, itemID uuid.UUID) ([]ReviewRecord, error) {
	rows, err := s.q.GetReviewsByItem(ctx, &itemID)
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
