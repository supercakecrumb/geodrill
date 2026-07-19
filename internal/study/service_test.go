package study

import (
	"testing"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// ── currentTierFrom ──────────────────────────────────────────────────────
//
// currentTierFrom is exercised directly (no DB): Service.CurrentTier is a
// thin wrapper around Store.RecomputeTierProgress + this pure function, the
// same split tierComplete already uses, so the actual "current tier" policy
// stays unit-testable without a real store.

func TestCurrentTierFrom_BrandNewUser(t *testing.T) {
	// A brand-new user has zero introduced items everywhere. Every tier
	// that exists in the catalog still shows up (RecomputeTierProgress's
	// LEFT JOIN to user_items), just with IntroducedItems/GoodShapeItems at
	// 0 — so tier 0, the lowest tier, reads as the current tier, even
	// though tiers 0 and 1 are both unlocked from the start.
	progress := []storage.TierProgress{
		{Tier: 0, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
		{Tier: 1, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
		{Tier: 2, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
		{Tier: 3, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
	}

	tier, maxTier := currentTierFrom(progress)

	if tier != 0 {
		t.Fatalf("expected current tier 0 for a brand-new user, got %d", tier)
	}
	if maxTier != 3 {
		t.Fatalf("expected max tier 3 (highest existing catalog tier), got %d", maxTier)
	}
}

func TestCurrentTierFrom_NoProgressAtAll(t *testing.T) {
	tier, maxTier := currentTierFrom(nil)
	if tier != 0 || maxTier != 0 {
		t.Fatalf("expected tier=0 maxTier=0 for empty progress, got tier=%d maxTier=%d", tier, maxTier)
	}
}

func TestCurrentTierFrom_Tier0Complete(t *testing.T) {
	// Tier 0 is now complete (100% introduced, >= 80% good shape); tier 1 is
	// still open from the start but untouched. The frontier advances to the
	// lowest tier that is not yet complete: tier 1 — per the gating rule
	// (finishing tier 0 unlocks tier 2, but tier 1 was already unlocked and
	// is still the lowest incomplete one).
	progress := []storage.TierProgress{
		{Tier: 0, TotalItems: 10, IntroducedItems: 10, GoodShapeItems: 9},
		{Tier: 1, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
		{Tier: 2, TotalItems: 10, IntroducedItems: 3, GoodShapeItems: 0},
		{Tier: 3, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
	}

	tier, maxTier := currentTierFrom(progress)

	if tier != 1 {
		t.Fatalf("expected current tier 1 once tier 0 completes, got %d", tier)
	}
	if maxTier != 3 {
		t.Fatalf("expected max tier 3, got %d", maxTier)
	}
}

func TestCurrentTierFrom_Tier0And1Complete(t *testing.T) {
	// Once both base tiers complete, tiers 2 and 3 unlock (finishing tier 0
	// unlocks tier 2, finishing tier 1 unlocks tier 3): the frontier jumps
	// to tier 2, the new lowest incomplete tier.
	progress := []storage.TierProgress{
		{Tier: 0, TotalItems: 10, IntroducedItems: 10, GoodShapeItems: 9},
		{Tier: 1, TotalItems: 10, IntroducedItems: 10, GoodShapeItems: 8},
		{Tier: 2, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
		{Tier: 3, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
	}

	tier, maxTier := currentTierFrom(progress)

	if tier != 2 {
		t.Fatalf("expected current tier 2 once tiers 0 and 1 complete, got %d", tier)
	}
	if maxTier != 3 {
		t.Fatalf("expected max tier 3, got %d", maxTier)
	}
}

func TestCurrentTierFrom_EverythingComplete(t *testing.T) {
	// No incomplete tier left at all: current tier falls back to the
	// highest tier that exists, since there's nothing left to be "not yet
	// complete" in.
	progress := []storage.TierProgress{
		{Tier: 0, TotalItems: 5, IntroducedItems: 5, GoodShapeItems: 5},
		{Tier: 1, TotalItems: 5, IntroducedItems: 5, GoodShapeItems: 5},
	}

	tier, maxTier := currentTierFrom(progress)

	if tier != 1 || maxTier != 1 {
		t.Fatalf("expected tier=1 maxTier=1 when everything is complete, got tier=%d maxTier=%d", tier, maxTier)
	}
}

func TestCurrentTierFrom_UnorderedInput(t *testing.T) {
	// currentTierFrom must not assume ascending input order (defensive:
	// RecomputeTierProgress's SQL orders by tier, but the pure function
	// shouldn't silently depend on that).
	progress := []storage.TierProgress{
		{Tier: 3, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
		{Tier: 0, TotalItems: 10, IntroducedItems: 10, GoodShapeItems: 9},
		{Tier: 2, TotalItems: 10, IntroducedItems: 3, GoodShapeItems: 0},
		{Tier: 1, TotalItems: 10, IntroducedItems: 0, GoodShapeItems: 0},
	}

	tier, maxTier := currentTierFrom(progress)

	if tier != 1 {
		t.Fatalf("expected current tier 1 (lowest incomplete) regardless of input order, got %d", tier)
	}
	if maxTier != 3 {
		t.Fatalf("expected max tier 3 regardless of input order, got %d", maxTier)
	}
}
