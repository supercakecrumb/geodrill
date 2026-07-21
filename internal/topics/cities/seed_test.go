package cities

import (
	"path/filepath"
	"testing"
)

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

// TestLoadCitiesSpotChecks parses the committed seed and spot-checks that the
// new geo fields are decoded for a couple of well-known cities.
func TestLoadCitiesSpotChecks(t *testing.T) {
	sf, err := loadCitiesFile(citiesSeedPath())
	if err != nil {
		t.Fatalf("loadCitiesFile: %v", err)
	}
	byKey := make(map[string]citySeed, len(sf.Cities))
	for _, e := range sf.Cities {
		byKey[e.Key] = e
	}

	shanghai, ok := byKey["cn:shanghai"]
	if !ok {
		t.Fatalf("cn:shanghai not in seed")
	}
	if shanghai.Name != "Shanghai" || shanghai.Country != "CN" || shanghai.Tier != 0 {
		t.Fatalf("shanghai = %+v, want Shanghai/CN/tier0", shanghai)
	}
	if shanghai.Lat == 0 || shanghai.Lng == 0 || shanghai.Region == "" || shanghai.GeonameID == "" {
		t.Fatalf("shanghai missing geo fields: %+v", shanghai)
	}

	munich, ok := byKey["de:munich"]
	if !ok {
		t.Fatalf("de:munich not in seed")
	}
	if munich.Region != "Bavaria" {
		t.Fatalf("munich region = %q, want Bavaria", munich.Region)
	}
	if munich.Elevation == nil || *munich.Elevation != 524 {
		t.Fatalf("munich elevation = %v, want 524", munich.Elevation)
	}
	if munich.Lat == 0 || munich.Lng == 0 {
		t.Fatalf("munich missing lat/lng: %+v", munich)
	}
}

// TestLoadCityFactsReal loads the committed facts file and checks a known key.
func TestLoadCityFactsReal(t *testing.T) {
	facts, err := loadCityFacts(cityFactsSeedPath())
	if err != nil {
		t.Fatalf("loadCityFacts: %v", err)
	}
	if len(facts) == 0 {
		t.Fatalf("no facts loaded")
	}
	munich, ok := facts["de:munich"]
	if !ok {
		t.Fatalf("de:munich fact missing")
	}
	if munich.Blurb == "" || munich.SourceURL == "" {
		t.Fatalf("de:munich fact = %+v, want blurb + source_url", munich)
	}
}

// TestLoadCityFactsMissingFileTolerated: a missing facts file is fine (facts
// are an enhancement, not a hard dependency).
func TestLoadCityFactsMissingFileTolerated(t *testing.T) {
	facts, err := loadCityFacts(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("missing facts file should not error, got %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("missing facts file should yield an empty map, got %d entries", len(facts))
	}
}

// TestFactForTierGating: facts fold ONLY for tier 0-2.
func TestFactForTierGating(t *testing.T) {
	facts := map[string]cityFactSeed{
		"x:city": {Key: "x:city", Blurb: "A blurb.", SourceURL: "https://example.org/x"},
	}
	for tier := int16(0); tier <= 2; tier++ {
		blurb, url := factFor(tier, "x:city", facts)
		if blurb != "A blurb." || url != "https://example.org/x" {
			t.Fatalf("tier %d: fact not folded (blurb=%q url=%q)", tier, blurb, url)
		}
	}
	for tier := int16(3); tier <= 6; tier++ {
		if blurb, url := factFor(tier, "x:city", facts); blurb != "" || url != "" {
			t.Fatalf("tier %d: fact must NOT fold (blurb=%q url=%q)", tier, blurb, url)
		}
	}
	// A city with no fact entry gets nothing, even at tier 0.
	if blurb, url := factFor(0, "y:missing", facts); blurb != "" || url != "" {
		t.Fatalf("missing fact entry should fold nothing (blurb=%q url=%q)", blurb, url)
	}
}

// TestMapImageForCoords: map_image is set for every city WITH coordinates
// (non-zero lat OR lng) — on-demand rendering means no media_files
// pre-registration is required — and left empty for a coordinate-less city.
func TestMapImageForCoords(t *testing.T) {
	if got := mapImageForCoords("de:munich", 48.14, 11.58); got != "de-munich.png" {
		t.Fatalf("mapImageForCoords(de:munich, coords) = %q, want de-munich.png", got)
	}
	// A single non-zero component is enough to frame a marker.
	if got := mapImageForCoords("x:y", 10, 0); got != "x-y.png" {
		t.Fatalf("mapImageForCoords(x:y, 10,0) = %q, want x-y.png", got)
	}
	// Exactly (0,0) = coordinate-less: text fallback.
	if got := mapImageForCoords("fr:paris", 0, 0); got != "" {
		t.Fatalf("mapImageForCoords(fr:paris, 0,0) = %q, want empty", got)
	}
}

// TestLookupTables builds the real city lookup table and asserts the entries
// Accept/Labels close over resolve correctly.
func TestLookupTables(t *testing.T) {
	tbl, err := loadLookupTables()
	if err != nil {
		t.Fatalf("loadLookupTables: %v", err)
	}
	if tbl.cityLabels["de:munich"] != "Munich" {
		t.Fatalf("cityLabels[de:munich] = %q, want Munich", tbl.cityLabels["de:munich"])
	}
	if got := tbl.cityAccept["de:munich"]; len(got) == 0 || got[0] != "Munich" {
		t.Fatalf("cityAccept[de:munich] = %v, want to start with Munich", got)
	}
}
