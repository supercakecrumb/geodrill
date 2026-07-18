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

// TopicProgress is the aggregate total/introduced/good-shape progress for one
// user across an entire topic subtree (architecture §5.2 TopicRow) — no Tier
// field, unlike TierProgress, since it rolls up every tier under the topic.
type TopicProgress struct {
	TotalItems      int
	IntroducedItems int
	GoodShapeItems  int
}

// RecomputeTopicProgress aggregates progress across topicID's ENTIRE subtree
// (itself plus every descendant topic, via the topic_paths view) for one
// user — the "Introduced 48/50" line a container topic like "languages"
// shows, rolled up over every quizzable topic beneath it.
func (s *Store) RecomputeTopicProgress(ctx context.Context, userID, topicID uuid.UUID) (TopicProgress, error) {
	r, err := s.q.RecomputeTopicProgress(ctx, db.RecomputeTopicProgressParams{UserID: userID, ID: topicID})
	if err != nil {
		return TopicProgress{}, err
	}
	return TopicProgress{
		TotalItems:      int(r.TotalItems),
		IntroducedItems: int(r.IntroducedItems),
		GoodShapeItems:  int(r.GoodShapeItems),
	}, nil
}

// ListDistinctTiersUnderTopic returns every effective tier used by an item
// anywhere in topicID's subtree (itself + descendants), ascending — the
// input to the 🔒 AnyLocked/LockedTier badge (architecture §5.2).
func (s *Store) ListDistinctTiersUnderTopic(ctx context.Context, topicID uuid.UUID) ([]int16, error) {
	return s.q.ListDistinctTiersUnderTopic(ctx, topicID)
}

// TierBreakdownRow is one tier's progress within a single quizzable topic's
// own items (architecture §5.2 TierRow's source data).
type TierBreakdownRow struct {
	Tier            int16
	TotalItems      int
	IntroducedItems int
	GoodShapeItems  int
}

// RecomputeTopicTierBreakdown returns per-tier progress within ONE quizzable
// topic's own items (non-recursive — a quizzable topic holds items
// directly), ascending by tier.
func (s *Store) RecomputeTopicTierBreakdown(ctx context.Context, userID, topicID uuid.UUID) ([]TierBreakdownRow, error) {
	rows, err := s.q.RecomputeTopicTierBreakdown(ctx, db.RecomputeTopicTierBreakdownParams{UserID: userID, TopicID: topicID})
	if err != nil {
		return nil, err
	}
	out := make([]TierBreakdownRow, len(rows))
	for i, r := range rows {
		out[i] = TierBreakdownRow{
			Tier:            r.Tier,
			TotalItems:      int(r.TotalItems),
			IntroducedItems: int(r.IntroducedItems),
			GoodShapeItems:  int(r.GoodShapeItems),
		}
	}
	return out, nil
}
