package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func introductionFrom(i db.Introduction) Introduction {
	messageID, hasMessage := int8ToMessageID(i.MessageID)
	return Introduction{
		ID:         i.ID,
		UserID:     i.UserID,
		ItemID:     i.ItemID,
		Seq:        int(i.Seq),
		Outcome:    int2Ptr(i.Outcome),
		ShownAt:    tsTime(i.ShownAt),
		AnsweredAt: tsTime(i.AnsweredAt),
		MessageID:  messageID,
		HasMessage: hasMessage,
	}
}

// NextIntroSeq returns the seq for the next introduction row of this
// user+item (1 = first exposure, >1 = re-view, architecture §2.4).
func (s *Store) NextIntroSeq(ctx context.Context, userID, itemID uuid.UUID) (int, error) {
	n, err := s.q.NextIntroSeq(ctx, db.NextIntroSeqParams{UserID: userID, ItemID: itemID})
	return int(n), err
}

// InsertIntroduction creates one introduction row (a card shown, outcome nil
// until answered).
func (s *Store) InsertIntroduction(ctx context.Context, userID, itemID uuid.UUID, seq int, shownAt time.Time) (Introduction, error) {
	i, err := s.q.InsertIntroduction(ctx, db.InsertIntroductionParams{
		UserID:     userID,
		ItemID:     itemID,
		Seq:        int32(seq),
		Outcome:    pgInt2(nil),
		ShownAt:    timeTs(shownAt),
		AnsweredAt: timeTs(time.Time{}),
		MessageID:  ptrInt8(nil),
	})
	if err != nil {
		return Introduction{}, err
	}
	return introductionFrom(i), nil
}

// AnswerIntroduction records the user's three-button outcome and answer time
// (engram.IntroOutcome: 0 got_it,1 known,2 test_me).
func (s *Store) AnswerIntroduction(ctx context.Context, introID uuid.UUID, outcome int16, answeredAt time.Time) (Introduction, error) {
	i, err := s.q.AnswerIntroduction(ctx, db.AnswerIntroductionParams{
		ID:         introID,
		Outcome:    pgInt2(&outcome),
		AnsweredAt: timeTs(answeredAt),
	})
	if err != nil {
		return Introduction{}, err
	}
	return introductionFrom(i), nil
}

// SetIntroductionMessageID records the Telegram message id for an introduction card.
func (s *Store) SetIntroductionMessageID(ctx context.Context, introID uuid.UUID, messageID int64) error {
	return s.q.SetIntroductionMessageID(ctx, db.SetIntroductionMessageIDParams{ID: introID, MessageID: pgInt8(messageID)})
}

// GetLatestOpenIntroductionForItem returns the newest shown-but-unanswered
// introduction for a user+item, if any.
func (s *Store) GetLatestOpenIntroductionForItem(ctx context.Context, userID, itemID uuid.UUID) (Introduction, bool, error) {
	i, err := s.q.GetLatestOpenIntroductionForItem(ctx, db.GetLatestOpenIntroductionForItemParams{UserID: userID, ItemID: itemID})
	if IsNotFound(err) {
		return Introduction{}, false, nil
	}
	if err != nil {
		return Introduction{}, false, err
	}
	return introductionFrom(i), true, nil
}

// GetLatestOpenIntroduction returns the newest shown-but-unanswered
// introduction for a user, regardless of item — resolves a bare
// callback/reply to whichever card is still on screen (architecture §5.1).
func (s *Store) GetLatestOpenIntroduction(ctx context.Context, userID uuid.UUID) (Introduction, bool, error) {
	i, err := s.q.GetLatestOpenIntroduction(ctx, userID)
	if IsNotFound(err) {
		return Introduction{}, false, nil
	}
	if err != nil {
		return Introduction{}, false, err
	}
	return introductionFrom(i), true, nil
}

// GetIntroductionByID looks up one introduction row by primary key
// (internal/study.Service.AnswerIntro: resolve the item an intro-card
// callback refers to).
func (s *Store) GetIntroductionByID(ctx context.Context, id uuid.UUID) (Introduction, bool, error) {
	i, err := s.q.GetIntroductionByID(ctx, id)
	if IsNotFound(err) {
		return Introduction{}, false, nil
	}
	if err != nil {
		return Introduction{}, false, err
	}
	return introductionFrom(i), true, nil
}

// AnswerIntroductionOnce is the atomic single-use counterpart to
// AnswerIntroduction (mirrors Store.MarkExerciseAnswered): it flips
// outcome/answered_at only if the introduction is still open. found=false
// means it was already answered (a stale second tap on the same intro card,
// architecture §5.1/§5.5) — the caller must not re-apply the lifecycle
// transition in that case.
func (s *Store) AnswerIntroductionOnce(ctx context.Context, introID uuid.UUID, outcome int16, answeredAt time.Time) (Introduction, bool, error) {
	i, err := s.q.AnswerIntroductionOnce(ctx, db.AnswerIntroductionOnceParams{
		ID:         introID,
		Outcome:    pgInt2(&outcome),
		AnsweredAt: timeTs(answeredAt),
	})
	if IsNotFound(err) {
		return Introduction{}, false, nil
	}
	if err != nil {
		return Introduction{}, false, err
	}
	return introductionFrom(i), true, nil
}

// CountIntroductionsToday counts distinct items with a first-exposure
// (seq=1), answered outcome, inside the caller-supplied local-day [from, to)
// bounds — the daily introduction budget's spent count (architecture §2.4).
func (s *Store) CountIntroductionsToday(ctx context.Context, userID uuid.UUID, from, to time.Time) (int, error) {
	n, err := s.q.CountIntroductionsToday(ctx, db.CountIntroductionsTodayParams{
		UserID:       userID,
		AnsweredAt:   timeTs(from),
		AnsweredAt_2: timeTs(to),
	})
	return int(n), err
}
