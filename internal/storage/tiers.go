package storage

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func tierProgressFrom(t db.UserTierProgress) TierProgress {
	return TierProgress{
		UserID:          t.UserID,
		Tier:            t.Tier,
		TotalItems:      int(t.TotalItems),
		IntroducedItems: int(t.IntroducedItems),
		GoodShapeItems:  int(t.GoodShapeItems),
		Complete:        t.Complete,
		UpdatedAt:       tsTime(t.UpdatedAt),
	}
}

// UpsertTierProgress writes the cached per-user, per-tier completion summary
// (architecture §2.9). RecomputeTierProgress[ForTier] is the on-the-fly
// source of truth this cache mirrors.
func (s *Store) UpsertTierProgress(ctx context.Context, p TierProgress) error {
	return s.q.UpsertTierProgress(ctx, db.UpsertTierProgressParams{
		UserID:          p.UserID,
		Tier:            p.Tier,
		TotalItems:      int32(p.TotalItems),
		IntroducedItems: int32(p.IntroducedItems),
		GoodShapeItems:  int32(p.GoodShapeItems),
		Complete:        p.Complete,
	})
}

// ListTierProgressForUser returns the cached progress for every tier of a user.
func (s *Store) ListTierProgressForUser(ctx context.Context, userID uuid.UUID) ([]TierProgress, error) {
	rows, err := s.q.ListTierProgressForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]TierProgress, len(rows))
	for i, r := range rows {
		out[i] = tierProgressFrom(r)
	}
	return out, nil
}

// RecomputeTierProgress is the on-the-fly source of truth for every tier of
// one user (architecture §4.2): totals, introduced, and "good shape" counts
// via a single GROUP BY over item_tiers <-> items <-> user_items. Good-shape
// = known (lifecycle=3) OR graduated-and-durable (state=Review(2) AND
// stability>=21d, §4.1). Complete is left for the caller to derive (tier
// complete iff introduced==total AND good_shape/total >= 0.8, §4.1) since it
// is a policy decision, not a stored fact.
func (s *Store) RecomputeTierProgress(ctx context.Context, userID uuid.UUID) ([]TierProgress, error) {
	rows, err := s.q.RecomputeTierProgress(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]TierProgress, len(rows))
	for i, r := range rows {
		out[i] = TierProgress{
			UserID:          userID,
			Tier:            r.Tier,
			TotalItems:      int(r.TotalItems),
			IntroducedItems: int(r.IntroducedItems),
			GoodShapeItems:  int(r.GoodShapeItems),
		}
	}
	return out, nil
}

// RecomputeTierProgressForTier is the single-tier variant of
// RecomputeTierProgress, for the per-answer / per-introduction transactional
// recompute (architecture §4.2/§5.5: "only the item's tier needs recompute").
// found=false means the tier has no items (nothing to recompute).
func (s *Store) RecomputeTierProgressForTier(ctx context.Context, userID uuid.UUID, tier int16) (TierProgress, bool, error) {
	r, err := s.q.RecomputeTierProgressForTier(ctx, db.RecomputeTierProgressForTierParams{UserID: userID, Tier: tier})
	if IsNotFound(err) {
		return TierProgress{}, false, nil
	}
	if err != nil {
		return TierProgress{}, false, err
	}
	return TierProgress{
		UserID:          userID,
		Tier:            r.Tier,
		TotalItems:      int(r.TotalItems),
		IntroducedItems: int(r.IntroducedItems),
		GoodShapeItems:  int(r.GoodShapeItems),
	}, true, nil
}
