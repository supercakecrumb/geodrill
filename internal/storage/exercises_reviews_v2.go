package storage

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

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
