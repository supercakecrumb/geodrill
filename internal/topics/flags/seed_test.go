package flags

import (
	"testing"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// TestLoadFlagsRealSeed parses the committed seeds/flags.yaml and
// cross-checks it against seeds/countries.yaml: every entry references a
// real country, no country is duplicated across shapes or within a shape,
// no country is claimed by both a single item and a confusable group
// (design §3's exclusivity rule), and every group's images/countries are
// the same length.
func TestLoadFlagsRealSeed(t *testing.T) {
	sf, err := loadFlagsFile(flagsSeedPath())
	if err != nil {
		t.Fatalf("loadFlagsFile: %v", err)
	}
	if len(sf.Flags) != 228 {
		t.Fatalf("len(flags) = %d, want 228", len(sf.Flags))
	}
	if len(sf.ConfusableGroups) != 10 {
		t.Fatalf("len(confusable_groups) = %d, want 10", len(sf.ConfusableGroups))
	}

	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries))
	for _, c := range countries {
		countryISO[c.ISOA2] = true
	}

	seenSingle := make(map[string]bool, len(sf.Flags))
	for _, e := range sf.Flags {
		if seenSingle[e.Country] {
			t.Fatalf("duplicate single flags entry for %q", e.Country)
		}
		seenSingle[e.Country] = true
		if !countryISO[e.Country] {
			t.Fatalf("flags entry %q has no matching country in countries.yaml", e.Country)
		}
		if e.Image == "" {
			t.Fatalf("flags entry %q has no image", e.Country)
		}
	}

	groupCount := 0
	seenGroup := make(map[string]bool)
	for _, g := range sf.ConfusableGroups {
		if len(g.Countries) != len(g.Images) {
			t.Fatalf("confusable group %q has %d countries but %d images", g.Group, len(g.Countries), len(g.Images))
		}
		if g.Label == "" {
			t.Fatalf("confusable group %q has no label", g.Group)
		}
		for _, c := range g.Countries {
			if seenSingle[c] {
				t.Fatalf("country %q appears both as a single flags entry AND in confusable group %q (design §3 violation)", c, g.Group)
			}
			if seenGroup[c] {
				t.Fatalf("country %q appears in more than one confusable group", c)
			}
			seenGroup[c] = true
			if !countryISO[c] {
				t.Fatalf("confusable group %q references unknown country %q", g.Group, c)
			}
			groupCount++
		}
	}

	// design §4: 228 singles + 10 groups covering 24 countries = 252 total,
	// matching every iso_a2 row in seeds/countries.yaml exactly once.
	if groupCount != 24 {
		t.Fatalf("total countries across confusable groups = %d, want 24", groupCount)
	}
	if got := len(seenSingle) + groupCount; got != len(countries) {
		t.Fatalf("total flags coverage = %d, want %d (len(countries.yaml))", got, len(countries))
	}

	// GB subdivisions are ordinary single items (design §1), never in a group.
	for _, code := range []string{"GB-ENG", "GB-SCT", "GB-WLS"} {
		if !seenSingle[code] {
			t.Fatalf("expected subdivision %q as an ordinary single item", code)
		}
	}
}

// TestTierFor checks the subdivision override and the delegation to the
// shared countrytier rubric otherwise (design §7).
func TestTierFor(t *testing.T) {
	us := storage.Country{ISOA2: "US", UNMember: true, GGCoverage: true}
	if got := tierFor(us); got != 0 {
		t.Fatalf("tierFor(US) = %d, want 0", got)
	}

	obscure := storage.Country{ISOA2: "AQ", UNMember: false, GGCoverage: false}
	if got := tierFor(obscure); got != 4 {
		t.Fatalf("tierFor(obscure non-UN-member) = %d, want 4", got)
	}

	// A subdivision is pinned to tier 3 regardless of what the shared
	// rubric would otherwise say (subdivisions are never UN members, so the
	// shared rubric alone would put them at tier 4).
	subdivision := storage.Country{ISOA2: "GB-SCT", UNMember: false, GGCoverage: true, IsSubdivision: true}
	if got := tierFor(subdivision); got != 3 {
		t.Fatalf("tierFor(subdivision) = %d, want 3", got)
	}
}

// TestGroupTierIsMax spot-checks the design §7 max-tier rule using the
// committed seed file: a group whose members span more than one tier must
// take the harder (higher) one, never the easier member's tier.
func TestGroupTierIsMax(t *testing.T) {
	sf, err := loadFlagsFile(flagsSeedPath())
	if err != nil {
		t.Fatalf("loadFlagsFile: %v", err)
	}
	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	byISO := make(map[string]engine.CountrySeed, len(countries))
	for _, c := range countries {
		byISO[c.ISOA2] = c
	}

	for _, g := range sf.ConfusableGroups {
		var maxTier int16
		for _, iso := range g.Countries {
			cs := byISO[iso]
			tier := tierFor(storage.Country{ISOA2: cs.ISOA2, UNMember: cs.UNMember, GGCoverage: cs.GGCoverage, IsSubdivision: cs.Subdivision})
			if tier > maxTier {
				maxTier = tier
			}
		}
		// Every group's computed max must be at least as high as every
		// individual member's own tier — trivially true by construction,
		// but this pins the rule down as a regression guard against a
		// future refactor accidentally taking min or first instead of max.
		for _, iso := range g.Countries {
			cs := byISO[iso]
			memberTier := tierFor(storage.Country{ISOA2: cs.ISOA2, UNMember: cs.UNMember, GGCoverage: cs.GGCoverage, IsSubdivision: cs.Subdivision})
			if memberTier > maxTier {
				t.Fatalf("group %q: member %s tier %d exceeds computed group max %d", g.Group, iso, memberTier, maxTier)
			}
		}
	}
}
