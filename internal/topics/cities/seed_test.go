package cities

import (
	"strings"
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// TestLoadCitiesRealSeed parses the committed seeds/cities.yaml and
// cross-checks it against seeds/countries.yaml: every entry references a
// real country, no key is duplicated, every key/name/population/tier is
// well-formed, all seven tiers (0..6) are represented, and the file is
// ordered population-descending. Deliberately does NOT assert an exact city
// count — cmd/citygen regenerates this file from the (frequently updated)
// GeoNames dataset, so a brittle count would break on every refresh; the
// structural invariants below are what actually matter. Runs without a DB
// so a structural break in the data fails fast (mirrors tld's
// TestLoadTLDsRealSeed).
func TestLoadCitiesRealSeed(t *testing.T) {
	sf, err := loadCitiesFile(citiesSeedPath())
	if err != nil {
		t.Fatalf("loadCitiesFile: %v", err)
	}
	if len(sf.Cities) == 0 {
		t.Fatalf("len(cities) = 0, want at least one city")
	}

	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries))
	for _, c := range countries {
		countryISO[c.ISOA2] = true
	}

	tiersSeen := make(map[int16]bool)

	// Every key must be unique — the seeder upserts by key, so a repeated
	// key would silently overwrite an earlier city.
	seen := make(map[string]citySeed, len(sf.Cities))
	for _, e := range sf.Cities {
		if _, ok := seen[e.Key]; ok {
			t.Fatalf("city key %q appears more than once", e.Key)
		}
		seen[e.Key] = e

		if e.Name == "" {
			t.Fatalf("city %q has empty name", e.Key)
		}
		if e.Population <= 0 {
			t.Fatalf("city %q has non-positive population %d", e.Key, e.Population)
		}
		if !countryISO[e.Country] {
			t.Fatalf("city %q references unknown country %q", e.Key, e.Country)
		}
		if e.Tier < 0 || e.Tier > 6 {
			t.Fatalf("city %q has tier %d, want in [0,6]", e.Key, e.Tier)
		}
		tiersSeen[e.Tier] = true
		// Keys are collision-safe <iso2>:<slug> identifiers; only shape
		// and uniqueness (checked above) are enforced here.
		if !strings.Contains(e.Key, ":") {
			t.Fatalf("city key %q is not in the documented <iso2>:<slug> shape", e.Key)
		}
	}

	for tier := int16(0); tier <= 6; tier++ {
		if !tiersSeen[tier] {
			t.Fatalf("no city has tier %d — expected all seven tiers (0..6) to be represented", tier)
		}
	}

	// The seed file itself must already be ordered population-descending —
	// Seed relies on sortByPopulationDesc, but the committed file (produced
	// by cmd/citygen) should already be in that order.
	for i := 1; i < len(sf.Cities); i++ {
		if sf.Cities[i].Population > sf.Cities[i-1].Population {
			t.Fatalf("seeds/cities.yaml is not population-descending: entry %d (%s, pop=%d) follows entry %d (%s, pop=%d)",
				i, sf.Cities[i].Key, sf.Cities[i].Population, i-1, sf.Cities[i-1].Key, sf.Cities[i-1].Population)
		}
	}

	// Spot checks from the brief: a few famous, population-heavy cities,
	// which must keep their curated English exonym after a regeneration.
	spot := map[string]struct {
		name    string
		country string
	}{
		"cn:shanghai": {"Shanghai", "CN"},
		"in:mumbai":   {"Mumbai", "IN"},
		"fr:paris":    {"Paris", "FR"},
		"de:munich":   {"Munich", "DE"},
		"ru:moscow":   {"Moscow", "RU"},
		"at:vienna":   {"Vienna", "AT"},
		"it:rome":     {"Rome", "IT"},
	}
	byKey := make(map[string]citySeed, len(sf.Cities))
	for _, e := range sf.Cities {
		byKey[e.Key] = e
	}
	for key, want := range spot {
		got, ok := byKey[key]
		if !ok {
			t.Fatalf("expected city %q not found in seed", key)
		}
		if got.Name != want.name || got.Country != want.country {
			t.Fatalf("city %q = {%s,%s}, want {%s,%s}", key, got.Name, got.Country, want.name, want.country)
		}
	}
}

// TestLookupTables builds the real lookup table and asserts the entries
// Accept/Labels close over resolve correctly.
func TestLookupTables(t *testing.T) {
	tbl, err := loadLookupTables()
	if err != nil {
		t.Fatalf("loadLookupTables: %v", err)
	}
	if tbl.countryLabels["FR"] != "France" {
		t.Fatalf("countryLabels[FR] = %q, want France", tbl.countryLabels["FR"])
	}
	if !contains(tbl.countryAccept["US"], "USA") {
		t.Fatalf("countryAccept[US] = %v, want to contain USA", tbl.countryAccept["US"])
	}
	if !contains(tbl.countryAccept["GB"], "Britain") {
		t.Fatalf("countryAccept[GB] = %v, want to contain Britain", tbl.countryAccept["GB"])
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestSortByPopulationDesc is a pure unit test of the population-descending
// ordering Seed relies on to set items.position: biggest first, stable
// tiebreak on Key for equal populations so re-seeding never reorders ties.
func TestSortByPopulationDesc(t *testing.T) {
	in := []citySeed{
		{Key: "aa:small", Name: "Small", Country: "AA", Population: 100},
		{Key: "bb:big", Name: "Big", Country: "BB", Population: 1000},
		{Key: "cc:tie-2", Name: "Tie2", Country: "CC", Population: 500},
		{Key: "cc:tie-1", Name: "Tie1", Country: "CC", Population: 500},
	}
	got := sortByPopulationDesc(in)
	wantOrder := []string{"bb:big", "cc:tie-1", "cc:tie-2", "aa:small"}
	if len(got) != len(wantOrder) {
		t.Fatalf("len(sorted) = %d, want %d", len(got), len(wantOrder))
	}
	for i, k := range wantOrder {
		if got[i].Key != k {
			t.Fatalf("sorted[%d].Key = %q, want %q (full order: %v)", i, got[i].Key, k, keysOf(got))
		}
	}
	// Input slice must not be mutated (Seed reuses sf.Cities elsewhere).
	if in[0].Key != "aa:small" {
		t.Fatalf("sortByPopulationDesc mutated its input slice")
	}
}

func keysOf(entries []citySeed) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	return out
}
