package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// Exercise is a mode-aware exercise row (architecture §2.5): the full
// item_id/mode/prompt/options/correct_answer/is_media/practice column set,
// alongside the single-use answered_at guard.
type Exercise struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	ItemID        uuid.UUID
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

func exerciseFrom(e db.Exercise) Exercise {
	messageID, hasMessage := int8ToMessageID(e.MessageID)
	return Exercise{
		ID:            e.ID,
		UserID:        e.UserID,
		ItemID:        e.ItemID,
		ContentID:     e.ContentID,
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

// exerciseFromOpenRow mirrors exerciseFrom for GetOpenExerciseByModeRow —
// a structurally-identical but distinct sqlc row type (its query selects an
// explicit column list rather than exercises.*, so sqlc does not reuse
// db.Exercise for it).
func exerciseFromOpenRow(e db.GetOpenExerciseByModeRow) Exercise {
	messageID, hasMessage := int8ToMessageID(e.MessageID)
	return Exercise{
		ID:            e.ID,
		UserID:        e.UserID,
		ItemID:        e.ItemID,
		ContentID:     e.ContentID,
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

// exerciseFromItemRow mirrors exerciseFrom for GetExercisesByItemRow — the
// counterpart for GetExercisesByItem's explicit column-list query.
func exerciseFromItemRow(e db.GetExercisesByItemRow) Exercise {
	messageID, hasMessage := int8ToMessageID(e.MessageID)
	return Exercise{
		ID:            e.ID,
		UserID:        e.UserID,
		ItemID:        e.ItemID,
		ContentID:     e.ContentID,
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

// InsertExerciseParams is the argument to InsertExercise.
type InsertExerciseParams struct {
	UserID        uuid.UUID
	ItemID        uuid.UUID
	ContentID     *uuid.UUID // optional: shared/topic-scoped content, or none
	Mode          int16
	Prompt        string
	Options       []byte // jsonb
	CorrectAnswer string
	IsMedia       bool
	Practice      bool
}

// InsertExercise inserts a mode-aware exercise row in one shot (architecture
// §2.5) — the sole insertion path internal/study.Service uses.
func (s *Store) InsertExercise(ctx context.Context, p InsertExerciseParams) (id uuid.UUID, createdAt time.Time, err error) {
	r, err := s.q.InsertExercise(ctx, db.InsertExerciseParams{
		UserID:        p.UserID,
		ItemID:        p.ItemID,
		ContentID:     p.ContentID,
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

// GetExerciseByID fetches one exercise by id with the full mode-aware field
// set. found=false means no such exercise.
func (s *Store) GetExerciseByID(ctx context.Context, id uuid.UUID) (Exercise, bool, error) {
	e, err := s.q.GetExerciseByID(ctx, id)
	if IsNotFound(err) {
		return Exercise{}, false, nil
	}
	if err != nil {
		return Exercise{}, false, err
	}
	return exerciseFrom(e), true, nil
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
	return exerciseFromOpenRow(e), true, nil
}

// SetExerciseItemFields updates item/mode-aware metadata on an existing
// exercise row.
func (s *Store) SetExerciseItemFields(ctx context.Context, exerciseID, itemID uuid.UUID, mode int16, prompt, correctAnswer string, isMedia, practice bool) error {
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
	rows, err := s.q.GetExercisesByItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	out := make([]Exercise, len(rows))
	for i, e := range rows {
		out[i] = exerciseFromItemRow(e)
	}
	return out, nil
}

// ReviewInsert is the full reviews row: engram.Review FSRS fields merged
// with the generalized item/mode/chosen/correct_answer columns (architecture
// §2.5). This is geodrill's single review write path.
type ReviewInsert struct {
	UserID           uuid.UUID
	ItemID           uuid.UUID
	ExerciseID       *uuid.UUID
	ContentID        *uuid.UUID
	Mode             int16
	Chosen           string
	CorrectAnswer    string
	Correct          bool
	Rating           int16
	ResponseMS       *int
	StabilityBefore  float64
	DifficultyBefore float64
	StabilityAfter   float64
	DifficultyAfter  float64
	StateBefore      int16
	ScheduledDays    int
	ElapsedDays      int
	ReviewedAt       time.Time
	Practice         bool
}

// InsertReview appends a review carrying the item-based, mode-aware column
// set (architecture §2.5).
func (s *Store) InsertReview(ctx context.Context, r ReviewInsert) error {
	return s.q.InsertReview(ctx, db.InsertReviewParams{
		UserID:           r.UserID,
		ItemID:           r.ItemID,
		ExerciseID:       r.ExerciseID,
		ContentID:        r.ContentID,
		Mode:             r.Mode,
		Chosen:           pgText(r.Chosen),
		CorrectAnswer:    pgText(r.CorrectAnswer),
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

// GetReviewsByItem returns every review recorded against one item, oldest first.
func (s *Store) GetReviewsByItem(ctx context.Context, itemID uuid.UUID) ([]ReviewRecord, error) {
	rows, err := s.q.GetReviewsByItem(ctx, itemID)
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
