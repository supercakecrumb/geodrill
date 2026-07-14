// Package engramstore adapts geodrill's storage.Store to engram's per-user
// SkillStore and ReviewStore interfaces (contract §3). Each Store instance is
// scoped to exactly one user, as engram requires.
//
// Note on the write path: engram.ReviewStore.Append only carries the FSRS
// fields of engram.Review — it has no answer-key / content context. geodrill's
// /train flow therefore persists full review rows (engram.Review merged with
// quiz.Attempt) via storage.Store.InsertReview directly, and uses this adapter
// mainly for SkillStore (card load/save) and ReviewStore.Log (reading the log
// back as []engram.Review for stats). Append is implemented faithfully for
// contract compliance and generic consumers.
package engramstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// Store implements engram.SkillStore and engram.ReviewStore for one user.
type Store struct {
	s      *storage.Store
	userID uuid.UUID
}

// New returns a per-user adapter over the shared storage.Store.
func New(s *storage.Store, userID uuid.UUID) *Store {
	return &Store{s: s, userID: userID}
}

var (
	_ engram.SkillStore  = (*Store)(nil)
	_ engram.ReviewStore = (*Store)(nil)
)

// ── SkillStore ──────────────────────────────────────────────────────────────

// Skills returns every skill in the given deck.
func (a *Store) Skills(ctx context.Context, deck engram.DeckID) ([]engram.Skill, error) {
	deckID, err := uuid.Parse(string(deck))
	if err != nil {
		return nil, err
	}
	skills, err := a.s.ListSkillsByDeck(ctx, deckID)
	if err != nil {
		return nil, err
	}
	out := make([]engram.Skill, len(skills))
	for i, sk := range skills {
		out[i] = SkillTo(sk)
	}
	return out, nil
}

// Card returns the stored card state for a skill (false = never seen).
func (a *Store) Card(ctx context.Context, skill engram.SkillID) (engram.CardState, bool, error) {
	skillID, err := uuid.Parse(string(skill))
	if err != nil {
		return engram.CardState{}, false, err
	}
	cf, found, err := a.s.GetCard(ctx, a.userID, skillID)
	if err != nil || !found {
		return engram.CardState{}, found, err
	}
	return CardStateFrom(cf), true, nil
}

// PutCard upserts the card state for a skill.
func (a *Store) PutCard(ctx context.Context, skill engram.SkillID, cs engram.CardState) error {
	skillID, err := uuid.Parse(string(skill))
	if err != nil {
		return err
	}
	return a.s.PutCard(ctx, a.userID, skillID, CardFieldsFrom(cs))
}

// ── ReviewStore ─────────────────────────────────────────────────────────────

// Append records one review-log entry (FSRS fields only; see package note).
func (a *Store) Append(ctx context.Context, r engram.Review) error {
	skillID, err := uuid.Parse(string(r.SkillID))
	if err != nil {
		return err
	}
	return a.s.InsertReview(ctx, storage.ReviewInsert{
		UserID:           a.userID,
		SkillID:          skillID,
		Correct:          r.Rating != engram.Again,
		Rating:           int16(r.Rating),
		StabilityBefore:  r.StabilityBefore,
		DifficultyBefore: r.DifficultyBefore,
		StabilityAfter:   r.StabilityAfter,
		DifficultyAfter:  r.DifficultyAfter,
		StateBefore:      int16(r.StateBefore),
		ScheduledDays:    r.ScheduledDays,
		ElapsedDays:      r.ElapsedDays,
		ReviewedAt:       r.ReviewedAt,
	})
}

// Log returns review entries at or after since as []engram.Review.
func (a *Store) Log(ctx context.Context, since time.Time) ([]engram.Review, error) {
	recs, err := a.s.ListReviewsSince(ctx, a.userID, since)
	if err != nil {
		return nil, err
	}
	out := make([]engram.Review, len(recs))
	for i, r := range recs {
		out[i] = engram.Review{
			SkillID:          engram.SkillID(r.SkillID.String()),
			Rating:           engram.Rating(r.Rating),
			ReviewedAt:       r.ReviewedAt,
			StabilityBefore:  r.StabilityBefore,
			DifficultyBefore: r.DifficultyBefore,
			StabilityAfter:   r.StabilityAfter,
			DifficultyAfter:  r.DifficultyAfter,
			StateBefore:      engram.State(r.StateBefore),
			ScheduledDays:    r.ScheduledDays,
			ElapsedDays:      r.ElapsedDays,
		}
	}
	return out, nil
}

// ── conversions (exported for reuse by the train service) ───────────────────

// SkillTo maps a storage.Skill to an engram.Skill (uuids become string ids).
func SkillTo(sk storage.Skill) engram.Skill {
	return engram.Skill{
		ID:     engram.SkillID(sk.ID.String()),
		DeckID: engram.DeckID(sk.DeckID.String()),
		Key:    sk.Key,
		Label:  sk.Label,
	}
}

// CardStateFrom maps storage.CardFields to engram.CardState.
func CardStateFrom(cf storage.CardFields) engram.CardState {
	return engram.CardState{
		Due:        cf.Due,
		Stability:  cf.Stability,
		Difficulty: cf.Difficulty,
		Reps:       cf.Reps,
		Lapses:     cf.Lapses,
		State:      engram.State(cf.State),
		LastReview: cf.LastReview,
	}
}

// CardFieldsFrom maps engram.CardState to storage.CardFields.
func CardFieldsFrom(cs engram.CardState) storage.CardFields {
	return storage.CardFields{
		Due:        cs.Due,
		Stability:  cs.Stability,
		Difficulty: cs.Difficulty,
		Reps:       cs.Reps,
		Lapses:     cs.Lapses,
		State:      int16(cs.State),
		LastReview: cs.LastReview,
	}
}
