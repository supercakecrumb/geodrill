package countrytier

import "testing"

// TestTierSetSizes guards the hardcoded tier0/tier1 membership lists against
// accidental additions/removals (ported from the original roadside copy's
// guard): tier0 is the task-specified 9-country set, tier1 is the 19 G20
// country members (EU excluded, not a country) minus the 8 of those 9 tier0
// countries that are also G20 members (ES is not a G20 country member, so
// 19-8=11).
func TestTierSetSizes(t *testing.T) {
	if len(tier0ISO) != 9 {
		t.Fatalf("len(tier0ISO) = %d, want 9", len(tier0ISO))
	}
	if len(tier1ISO) != 11 {
		t.Fatalf("len(tier1ISO) = %d, want 11", len(tier1ISO))
	}
	for iso := range tier1ISO {
		if tier0ISO[iso] {
			t.Fatalf("tier1ISO and tier0ISO both contain %q — tiers must be mutually exclusive", iso)
		}
	}
}
