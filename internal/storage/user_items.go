package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func userItemFrom(u db.UserItem) UserItem {
	return UserItem{
		UserID:    u.UserID,
		ItemID:    u.ItemID,
		Lifecycle: u.Lifecycle,
		Card: CardFields{
			Due:        tsTime(u.Due),
			Stability:  u.Stability,
			Difficulty: u.Difficulty,
			Reps:       int(u.Reps),
			Lapses:     int(u.Lapses),
			State:      u.State,
			LastReview: tsTime(u.LastReview),
		},
		IntroducedAt: tsTime(u.IntroducedAt),
		KnownAt:      tsTime(u.KnownAt),
		UpdatedAt:    tsTime(u.UpdatedAt),
	}
}

// GetUserItem returns the lifecycle + FSRS card state for one user+item.
// found=false means the item is implicitly "new" for this user (architecture
// §2.3: absence of a row means new).
func (s *Store) GetUserItem(ctx context.Context, userID, itemID uuid.UUID) (UserItem, bool, error) {
	u, err := s.q.GetUserItem(ctx, db.GetUserItemParams{UserID: userID, ItemID: itemID})
	if IsNotFound(err) {
		return UserItem{}, false, nil
	}
	if err != nil {
		return UserItem{}, false, err
	}
	return userItemFrom(u), true, nil
}

// PutUserItem upserts the lifecycle + FSRS card state for one user+item
// (engram.Lifecycle + engram.CardState, architecture §2.3).
func (s *Store) PutUserItem(ctx context.Context, userID, itemID uuid.UUID, lifecycle int16, card CardFields, introducedAt, knownAt time.Time) error {
	return s.q.PutUserItem(ctx, db.PutUserItemParams{
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
		KnownAt:      timeTs(knownAt),
	})
}

// ListUserItemsByLifecycle returns every user_items row for a user in a given
// lifecycle (engram.Lifecycle: 0 new,1 introduced,2 reviewing,3 known).
func (s *Store) ListUserItemsByLifecycle(ctx context.Context, userID uuid.UUID, lifecycle int16) ([]UserItem, error) {
	rows, err := s.q.ListUserItemsByLifecycle(ctx, db.ListUserItemsByLifecycleParams{UserID: userID, Lifecycle: lifecycle})
	if err != nil {
		return nil, err
	}
	out := make([]UserItem, len(rows))
	for i, r := range rows {
		out[i] = userItemFrom(r)
	}
	return out, nil
}

// ListDueUserItems returns Introduced/Reviewing cards due at or before now,
// joined with their item's topic/key/label (the engram.NextReview candidate
// set — architecture §1.5: no daily-new cap here, novelty is gated upstream
// at introduction).
func (s *Store) ListDueUserItems(ctx context.Context, userID uuid.UUID, now time.Time) ([]DueUserItem, error) {
	rows, err := s.q.ListDueUserItems(ctx, db.ListDueUserItemsParams{UserID: userID, Due: timeTs(now)})
	if err != nil {
		return nil, err
	}
	out := make([]DueUserItem, len(rows))
	for i, r := range rows {
		out[i] = DueUserItem{
			UserItem: UserItem{
				UserID:    r.UserID,
				ItemID:    r.ItemID,
				Lifecycle: r.Lifecycle,
				Card: CardFields{
					Due:        tsTime(r.Due),
					Stability:  r.Stability,
					Difficulty: r.Difficulty,
					Reps:       int(r.Reps),
					Lapses:     int(r.Lapses),
					State:      r.State,
					LastReview: tsTime(r.LastReview),
				},
				IntroducedAt: tsTime(r.IntroducedAt),
				KnownAt:      tsTime(r.KnownAt),
				UpdatedAt:    tsTime(r.UpdatedAt),
			},
			TopicID: r.TopicID,
			Key:     r.Key,
			Label:   r.Label,
		}
	}
	return out, nil
}

// CountDueUserItems counts a user's Introduced/Reviewing cards due at or
// before now — the counterpart of CountDueSkills (internal/study.Service.
// DueCount / the reminder loop's due count).
func (s *Store) CountDueUserItems(ctx context.Context, userID uuid.UUID, now time.Time) (int, error) {
	n, err := s.q.CountDueUserItems(ctx, db.CountDueUserItemsParams{UserID: userID, Due: timeTs(now)})
	return int(n), err
}

// ListUserItemCardsInFSRS returns every Introduced/Reviewing card for a user
// — the counterpart of ListCardsForUser (internal/study.Service.Stats'
// DueForecast input).
func (s *Store) ListUserItemCardsInFSRS(ctx context.Context, userID uuid.UUID) ([]CardFields, error) {
	rows, err := s.q.ListUserItemCardsInFSRS(ctx, userID)
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

// CountIntroducedItems counts items that have left lifecycle=new (Introduced,
// Reviewing, or Known) for a user — /stats' "introduced" count.
func (s *Store) CountIntroducedItems(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := s.q.CountIntroducedItems(ctx, userID)
	return int(n), err
}

// CountKnownItems counts items marked known ("I know this") for a user —
// /stats' "known" count.
func (s *Store) CountKnownItems(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := s.q.CountKnownItems(ctx, userID)
	return int(n), err
}

// ListCandidateIntroItems returns candidate items for the introduction queue:
// active, tier-unlocked (allowedTiers), and either never seen by this user or
// still lifecycle=new. Ordered tier, then topic position, then item position
// — the priority order engram.NextIntroductions expects the caller to supply
// (architecture §1.4/§4.2).
func (s *Store) ListCandidateIntroItems(ctx context.Context, userID uuid.UUID, allowedTiers []int16) ([]IntroCandidate, error) {
	rows, err := s.q.ListCandidateIntroItems(ctx, db.ListCandidateIntroItemsParams{UserID: userID, Column2: allowedTiers})
	if err != nil {
		return nil, err
	}
	out := make([]IntroCandidate, len(rows))
	for i, r := range rows {
		out[i] = IntroCandidate{
			ItemID:  r.ItemID,
			TopicID: r.TopicID,
			Key:     r.Key,
			Label:   r.Label,
			Tier:    r.Tier,
		}
	}
	return out, nil
}
