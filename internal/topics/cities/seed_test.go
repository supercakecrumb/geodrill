package cities

import (
	"strings"
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// TestLoadCitiesRealSeed parses the committed seeds/cities.yaml and
// cross-checks it against seeds/countries.yaml: every entry references a
// real country, no key is duplicated, and every key/name/population is
// well-formed. Runs without a DB so a structural break in the data fails
// fast (mirrors tld's TestLoadTLDsRealSeed).
func TestLoadCitiesRealSeed(t *testing.T) {
	sf, err := loadCitiesFile(citiesSeedPath())
	if err != nil {
		t.Fatalf("loadCitiesFile: %v", err)
	}
	if len(sf.Cities) != 451 {
		t.Fatalf("len(cities) = %d, want 451", len(sf.Cities))
	}

	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries))
	for _, c := range countries {
		countryISO[c.ISOA2] = true
	}

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
		// Keys are collision-safe <iso2>:<slug> identifiers; only shape
		// and uniqueness (checked above) are enforced here.
		if !strings.Contains(e.Key, ":") {
			t.Fatalf("city key %q is not in the documented <iso2>:<slug> shape", e.Key)
		}
	}

	// Spot checks from the brief: a few famous, population-heavy cities.
	spot := map[string]struct {
		name    string
		country string
	}{
		"cn:shanghai": {"Shanghai", "CN"},
		"in:mumbai":   {"Mumbai", "IN"},
		"fr:paris":    {"Paris", "FR"},
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
