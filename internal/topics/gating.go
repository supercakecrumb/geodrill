package topics

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// Good-shape / tier-complete thresholds (architecture §4.1). This is the ONE
// place in Go to change these two numbers. The on-the-fly SQL that actually
// computes good_shape_items — Store.RecomputeTierProgress /
// RecomputeTierProgressForTier, backed by
// internal/storage/query/tiers.sql — hardcodes the same values inline
// (`ui.state = 2 AND ui.stability >= 21`); that file is storage's, not
// topics', so it is not touched here. If GoodShapeMinStabilityDays ever
// changes, tiers.sql's inline `21` must change with it (cross-reference,
// not a shared constant, since SQL can't import Go).
const (
	// GoodShapeMinStabilityDays is the minimum FSRS stability (in days) for
	// a Reviewing item to count as "good shape": state=Review (graduated
	// out of Learning) AND stability >= this many days (comfortably past
	// the learning hump at retention 0.9, ruling out cards that just barely
	// graduated). lifecycle=Known always counts as good shape regardless of
	// this threshold — the user asserted mastery.
	GoodShapeMinStabilityDays = 21

	// TierCompleteShare is the minimum share of a tier's items that must be
	// in good shape — in addition to 100% of the tier's items being
	// introduced — for the tier to count as complete. 80%, not 100%, so one
	// stubborn item can't hard-block progression to tier n+2.
	TierCompleteShare = 0.80
)

// UnlockedTiers computes the gated tier set from cached per-user tier
// progress (architecture §4.2): {0,1} ∪ {n+2 : tier n is complete}. Pure —
// trusts progress[i].Complete (already derived by whoever populated the
// cache per the GoodShapeMinStabilityDays/TierCompleteShare rule) and
// touches no storage. Returns the base {0,1} set for empty/nil progress.
func UnlockedTiers(progress []storage.TierProgress) []int {
	unlocked := map[int]bool{0: true, 1: true}
	for _, p := range progress {
		if p.Complete {
			unlocked[int(p.Tier)+2] = true
		}
	}

	out := make([]int, 0, len(unlocked))
	for t := range unlocked {
		out = append(out, t)
	}
	sort.Ints(out)
	return out
}

// tierStore is the narrow slice of storage the gating Service needs —
// declared locally so Service depends on an interface, not *storage.Store,
// keeping this package trivially testable without a DB.
type tierStore interface {
	ListTierProgressForUser(ctx context.Context, userID uuid.UUID) ([]storage.TierProgress, error)
	RecomputeTierProgressForTier(ctx context.Context, userID uuid.UUID, tier int16) (storage.TierProgress, bool, error)
}

// Service wraps tier-gating reads (and a passthrough recompute) over a
// tierStore. It stays deliberately thin: the transactional recompute — the
// affected tier's RecomputeTierProgressForTier + UpsertTierProgress running
// inside the same transaction as the answer/introduction write that touched
// it — is train/storage wiring for a later wave (architecture §5.5), not
// this framework skeleton.
type Service struct {
	store tierStore
}

// NewService builds a gating Service over store.
func NewService(store tierStore) *Service {
	return &Service{store: store}
}

// AllowedTiers returns userID's currently unlocked tiers by reading the
// cached per-tier progress and applying UnlockedTiers.
func (s *Service) AllowedTiers(ctx context.Context, userID uuid.UUID) ([]int, error) {
	progress, err := s.store.ListTierProgressForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return UnlockedTiers(progress), nil
}

// Recompute passes through to the store's on-the-fly single-tier recompute
// (architecture §5.5: called by the caller inside the same transaction as
// the write that touched tier). found=false means tier has no items.
func (s *Service) Recompute(ctx context.Context, userID uuid.UUID, tier int16) (storage.TierProgress, bool, error) {
	return s.store.RecomputeTierProgressForTier(ctx, userID, tier)
}
