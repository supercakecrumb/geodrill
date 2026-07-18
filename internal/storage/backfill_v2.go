package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// LegacyUserSkill is one user_skills row (FSRS state for one user+skill),
// read wholesale for cmd/ingest's -backfill-v2 mode — there is no per-user
// scoping here, unlike GetCard/ListCardsForUser, because the backfill needs
// every user's rows at once.
type LegacyUserSkill struct {
	UserID  uuid.UUID
	SkillID uuid.UUID
	Card    CardFields
}

// ListAllUserSkills returns every user_skills row across all users, ordered
// (user_id, skill_id) for deterministic backfill logging.
func (s *Store) ListAllUserSkills(ctx context.Context) ([]LegacyUserSkill, error) {
	rows, err := s.q.ListAllUserSkills(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LegacyUserSkill, len(rows))
	for i, r := range rows {
		out[i] = LegacyUserSkill{
			UserID:  r.UserID,
			SkillID: r.SkillID,
			Card: CardFields{
				Due:        tsTime(r.Due),
				Stability:  r.Stability,
				Difficulty: r.Difficulty,
				Reps:       int(r.Reps),
				Lapses:     int(r.Lapses),
				State:      r.State,
				LastReview: tsTime(r.LastReview),
			},
		}
	}
	return out, nil
}

// InsertUserItemIfAbsent inserts the lifecycle + FSRS card state for one
// user+item ONLY if no user_items row already exists for that pair
// (ON CONFLICT DO NOTHING — the backfill-safe counterpart to PutUserItem's
// upsert, architecture §3.5). inserted=true means this call created the row;
// false means one already existed (either from the live app or a prior
// backfill run) and was left untouched.
func (s *Store) InsertUserItemIfAbsent(ctx context.Context, userID, itemID uuid.UUID, lifecycle int16, card CardFields, introducedAt time.Time) (inserted bool, err error) {
	n, err := s.q.InsertUserItemIfAbsent(ctx, db.InsertUserItemIfAbsentParams{
		UserID:       userID,
		ItemID:       itemID,
		Lifecycle:    lifecycle,
		Due:          timeTs(card.Due),
		Stability:    card.Stability,
		Difficulty:   card.Difficulty,
		Reps:         int32(card.Reps),
		Lapses:       int32(card.Lapses),
		State:        card.State,
		LastReview:   timeTs(card.LastReview),
		IntroducedAt: timeTs(introducedAt),
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// HasIntroductionForItem reports whether any introduction row (any outcome)
// already exists for a user+item — the "if none exists" guard
// -backfill-v2 uses before synthesizing one (architecture §3.5).
func (s *Store) HasIntroductionForItem(ctx context.Context, userID, itemID uuid.UUID) (bool, error) {
	n, err := s.q.CountIntroductionsForItem(ctx, db.CountIntroductionsForItemParams{UserID: userID, ItemID: itemID})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// InsertSynthesizedIntroduction records a synthesized first-exposure
// introduction (seq=1, outcome=0/got_it, shown_at=answered_at=at) for a
// user_item migrated by -backfill-v2, so it satisfies the "already
// introduced" invariant and is never re-introduced (architecture §3.5).
func (s *Store) InsertSynthesizedIntroduction(ctx context.Context, userID, itemID uuid.UUID, at time.Time) error {
	return s.q.InsertSynthesizedIntroduction(ctx, db.InsertSynthesizedIntroductionParams{
		UserID:  userID,
		ItemID:  itemID,
		ShownAt: timeTs(at),
	})
}

// BackfillExercisesForSkill attaches item_id/mode=0/correct_answer=key to
// every still-unmapped (item_id IS NULL) exercise row for one legacy skill.
// Returns the number of rows updated.
func (s *Store) BackfillExercisesForSkill(ctx context.Context, skillID, itemID uuid.UUID, key string) (int64, error) {
	return s.q.BackfillExercisesForSkill(ctx, db.BackfillExercisesForSkillParams{
		SkillID:       skillID,
		ItemID:        &itemID,
		CorrectAnswer: pgText(key),
	})
}

// BackfillReviewsForSkill attaches item_id/mode=0/chosen/correct_answer
// (copied from each row's own chosen_key/correct_key) to every
// still-unmapped (item_id IS NULL) review row for one legacy skill. Returns
// the number of rows updated.
func (s *Store) BackfillReviewsForSkill(ctx context.Context, skillID, itemID uuid.UUID) (int64, error) {
	return s.q.BackfillReviewsForSkill(ctx, db.BackfillReviewsForSkillParams{
		SkillID: skillID,
		ItemID:  &itemID,
	})
}
