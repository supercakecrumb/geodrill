package countrytier_test

import (
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
)

func TestTier(t *testing.T) {
	cases := []struct {
		name       string
		iso2       string
		unMember   bool
		ggCoverage bool
		want       int16
	}{
		{"tier0 US", "US", true, true, 0},
		{"tier0 GB", "GB", true, true, 0},
		{"tier0 wins over G20 membership", "FR", true, true, 0}, // FR is also G20 but tier0 takes priority
		{"tier1 remaining G20", "BR", true, true, 1},
		{"tier1 remaining G20 other", "TR", true, true, 1},
		{"tier2 UN member with GG coverage", "KE", true, true, 2},
		{"tier3 UN member without GG coverage", "SO", true, false, 3},
		{"tier4 non-UN territory with coverage", "GG", false, true, 4},
		{"tier4 non-UN territory without coverage", "AQ", false, false, 4},
		{"tier4 non-UN subdivision", "GB-ENG", false, true, 4},
		{"tier0 iso2 ignores flags", "DE", false, false, 0}, // hardcoded set wins regardless of flags
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := countrytier.Tier(c.iso2, c.unMember, c.ggCoverage)
			if got != c.want {
				t.Fatalf("Tier(%q, unMember=%v, ggCoverage=%v) = %d, want %d", c.iso2, c.unMember, c.ggCoverage, got, c.want)
			}
		})
	}
}

func TestTierOrderingIsFirstMatchWins(t *testing.T) {
	// A tier1 ISO with unMember=false must still resolve to tier1 (its ISO
	// membership is checked before the unMember/ggCoverage fallback rules).
	if got := countrytier.Tier("BR", false, false); got != 1 {
		t.Fatalf("Tier(BR, false, false) = %d, want 1 (tier1 ISO set wins regardless of flags)", got)
	}
}
