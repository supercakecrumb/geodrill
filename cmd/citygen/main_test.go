package main

import "testing"

func TestTierFor(t *testing.T) {
	cases := []struct {
		name       string
		population int64
		want       int16
	}{
		{"tier 0 exact boundary", 5_000_000, 0},
		{"tier 0 above boundary", 8_000_000, 0},
		{"tier 1 exact boundary", 2_000_000, 1},
		{"tier 1 mid-band", 3_500_000, 1},
		{"tier 2 exact boundary", 1_000_000, 2},
		{"tier 3 exact boundary", 500_000, 3},
		{"tier 4 exact boundary", 200_000, 4},
		{"tier 5 exact boundary", 75_000, 5},
		{"tier 6 below tier 5 floor", 74_999, 6},
		{"tier 6 dataset floor", 15_000, 6},
		{"tier 6 zero", 0, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tierFor(tc.population); got != tc.want {
				t.Errorf("tierFor(%d) = %d, want %d", tc.population, got, tc.want)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

func TestElevationFor(t *testing.T) {
	cases := []struct {
		name      string
		elevField string
		demField  string
		want      *int
	}{
		{"col16 present", "520", "515", intPtr(520)},
		{"col16 empty falls back to dem", "", "515", intPtr(515)},
		{"col16 empty, dem is sentinel -9999", "", "-9999", nil},
		{"both empty", "", "", nil},
		{"col16 non-numeric falls back to dem", "n/a", "100", intPtr(100)},
		{"col16 present as zero", "0", "515", intPtr(0)},
		{"col16 empty, dem non-numeric", "", "n/a", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := elevationFor(tc.elevField, tc.demField)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("elevationFor(%q, %q) = %v, want %v", tc.elevField, tc.demField, got, tc.want)
			}
			if got != nil && *got != *tc.want {
				t.Errorf("elevationFor(%q, %q) = %d, want %d", tc.elevField, tc.demField, *got, *tc.want)
			}
		})
	}
}

func TestRegionFor(t *testing.T) {
	admin1Names := map[string]string{
		"DE.02": "Bavaria",
		"US.CA": "California",
	}
	cases := []struct {
		name       string
		country    string
		admin1Code string
		want       string
	}{
		{"code present and mapped", "DE", "02", "Bavaria"},
		{"another mapped code", "US", "CA", "California"},
		{"blank code", "DE", "", ""},
		{"unmapped code", "FR", "99", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := regionFor(admin1Names, tc.country, tc.admin1Code); got != tc.want {
				t.Errorf("regionFor(%s, %s) = %q, want %q", tc.country, tc.admin1Code, got, tc.want)
			}
		})
	}
}

func TestAltNamesFor(t *testing.T) {
	cases := []struct {
		name         string
		cityName     string
		rowName      string
		rowASCIIName string
		want         []string
	}{
		{"both differ from city name", "Munich", "München", "Munchen", []string{"München", "Munchen"}},
		{"asciiname equals city name", "Shanghai", "上海", "Shanghai", []string{"上海"}},
		{"both equal city name", "Berlin", "Berlin", "Berlin", nil},
		{"name and asciiname identical to each other but differ from city", "Foo", "Bar", "Bar", []string{"Bar"}},
		{"empty row fields", "Foo", "", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := altNamesFor(tc.cityName, tc.rowName, tc.rowASCIIName)
			if len(got) != len(tc.want) {
				t.Fatalf("altNamesFor(%q, %q, %q) = %v, want %v", tc.cityName, tc.rowName, tc.rowASCIIName, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("altNamesFor(%q, %q, %q)[%d] = %q, want %q", tc.cityName, tc.rowName, tc.rowASCIIName, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestMergeUnmatchedCuratedPreservesGeoFields is the regression test for the
// "hand-patched coordinate survives a regen" guarantee described in the
// package doc: a curated city with no GeoNames match in-country must keep
// whatever lat/lng/region/elevation/geoname_id/alt_names it already carried
// in the committed seed, unchanged.
func TestMergeUnmatchedCuratedPreservesGeoFields(t *testing.T) {
	curated := []oldCitySeed{
		{
			Key:        "xx:nomatch",
			Name:       "Nomatchville",
			Country:    "XX",
			Population: 12345,
			Lat:        12.5,
			Lng:        -34.25,
			Region:     "Hand-Patched Region",
			Elevation:  intPtr(42),
			GeonameID:  "999999",
			AltNames:   []string{"OldAlt"},
		},
	}
	// No GeoNames rows at all in-country XX, so nameIndex has nothing under
	// "XX" — this curated city cannot possibly match.
	rows := []geoRow{}
	nameIndex := buildNameIndex(rows)
	admin1Names := map[string]string{}

	res, err := merge(curated, rows, nameIndex, perCountryCap, admin1Names)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(res.cities) != 1 {
		t.Fatalf("expected 1 output city, got %d", len(res.cities))
	}
	got := res.cities[0]
	if got.Lat != 12.5 || got.Lng != -34.25 {
		t.Errorf("lat/lng not preserved: got (%v, %v), want (12.5, -34.25)", got.Lat, got.Lng)
	}
	if got.Region != "Hand-Patched Region" {
		t.Errorf("region not preserved: got %q", got.Region)
	}
	if got.Elevation == nil || *got.Elevation != 42 {
		t.Errorf("elevation not preserved: got %v", got.Elevation)
	}
	if got.GeonameID != "999999" {
		t.Errorf("geoname_id not preserved: got %q", got.GeonameID)
	}
	if len(got.AltNames) != 1 || got.AltNames[0] != "OldAlt" {
		t.Errorf("alt_names not preserved: got %v", got.AltNames)
	}
	// Sanity: population/name/key/country/tier are untouched too.
	if got.Population != 12345 || got.Name != "Nomatchville" || got.Key != "xx:nomatch" || got.Country != "XX" {
		t.Errorf("unexpected mutation of core fields: %+v", got)
	}
	if got.Tier != tierFor(12345) {
		t.Errorf("tier mismatch: got %d, want %d", got.Tier, tierFor(12345))
	}
	if res.unmatchedCount != 1 || res.matchedCount != 0 {
		t.Errorf("expected 1 unmatched/0 matched, got matched=%d unmatched=%d", res.matchedCount, res.unmatchedCount)
	}
}

// TestMergeMatchedCuratedOverridesGeoFields checks the opposite case: when a
// curated city DOES match a GeoNames row, the new geographic fields come
// from that row (not from any pre-existing seed values), same as
// population already does.
func TestMergeMatchedCuratedOverridesGeoFields(t *testing.T) {
	curated := []oldCitySeed{
		{
			Key:        "de:munich",
			Name:       "Munich",
			Country:    "DE",
			Population: 1,
			Lat:        0,
			Lng:        0,
			Region:     "",
			Elevation:  nil,
			GeonameID:  "",
			AltNames:   nil,
		},
	}
	elev := 519
	rows := []geoRow{
		{
			ID:         "2867714",
			Name:       "München",
			ASCIIName:  "Munchen",
			AltNames:   []string{"Munich"},
			Country:    "DE",
			Population: 1_471_508,
			Lat:        48.13743,
			Lng:        11.57549,
			HasCoords:  true,
			Admin1Code: "02",
			Elevation:  &elev,
		},
	}
	nameIndex := buildNameIndex(rows)
	admin1Names := map[string]string{"DE.02": "Bavaria"}

	res, err := merge(curated, rows, nameIndex, perCountryCap, admin1Names)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(res.cities) != 1 {
		t.Fatalf("expected 1 output city, got %d", len(res.cities))
	}
	got := res.cities[0]
	if got.Name != "Munich" || got.Key != "de:munich" {
		t.Errorf("curated name/key must be preserved verbatim, got name=%q key=%q", got.Name, got.Key)
	}
	if got.Population != 1_471_508 {
		t.Errorf("population should come from matched row, got %d", got.Population)
	}
	if got.Lat != 48.13743 || got.Lng != 11.57549 {
		t.Errorf("lat/lng should come from matched row, got (%v, %v)", got.Lat, got.Lng)
	}
	if got.Region != "Bavaria" {
		t.Errorf("region should be resolved via admin1 lookup, got %q", got.Region)
	}
	if got.Elevation == nil || *got.Elevation != 519 {
		t.Errorf("elevation should come from matched row, got %v", got.Elevation)
	}
	if got.GeonameID != "2867714" {
		t.Errorf("geoname_id should come from matched row, got %q", got.GeonameID)
	}
	if len(got.AltNames) != 2 || got.AltNames[0] != "München" || got.AltNames[1] != "Munchen" {
		t.Errorf("alt_names should be the matched row's name+asciiname, got %v", got.AltNames)
	}
}

func TestBestCandidatePrefersMainNameMatch(t *testing.T) {
	realManhattan := &geoRow{ID: "real-manhattan", Name: "Manhattan", ASCIIName: "Manhattan", Population: 1_487_536}
	nycViaAlt := &geoRow{ID: "nyc", Name: "New York City", ASCIIName: "New York City", AltNames: []string{"manhattan"}, Population: 8_804_190}

	got := bestCandidate([]*geoRow{realManhattan, nycViaAlt}, "manhattan")
	if got.ID != "real-manhattan" {
		t.Fatalf("bestCandidate should prefer the main-name match (real Manhattan, pop 1,487,536) over the larger alternatename-only match (NYC via 'manhattan' alt, pop 8,804,190); got ID=%s pop=%d", got.ID, got.Population)
	}

	// Order shouldn't matter.
	got = bestCandidate([]*geoRow{nycViaAlt, realManhattan}, "manhattan")
	if got.ID != "real-manhattan" {
		t.Fatalf("bestCandidate order-independence failed: got ID=%s", got.ID)
	}

	// With ONLY alternatename-only candidates (no main-name match at all),
	// fall back to the best (largest-population) alt-only match.
	got = bestCandidate([]*geoRow{nycViaAlt}, "manhattan")
	if got.ID != "nyc" {
		t.Fatalf("bestCandidate should fall back to the alternatename-only match when no main-name match exists; got ID=%s", got.ID)
	}

	// Two main-name matches: pick the larger population, exactly as before
	// this preference rule existed.
	smallManhattanKS := &geoRow{ID: "ks-manhattan", Name: "Manhattan", ASCIIName: "Manhattan", Population: 56_308}
	got = bestCandidate([]*geoRow{smallManhattanKS, realManhattan}, "manhattan")
	if got.ID != "real-manhattan" {
		t.Fatalf("bestCandidate should pick the larger-population main-name match; got ID=%s", got.ID)
	}
}

// TestMergePrefersMainNameOverAlternatenameMatch is the end-to-end
// regression test for the false-match bug found regenerating seeds/cities.yaml
// against a refreshed GeoNames dump: a curated "Manhattan" (US) must match
// the real, small Manhattan GeoNames row, not New York City's row (which
// lists "manhattan" among its many alternatenames and vastly outweighs the
// real Manhattan borough in population).
func TestMergePrefersMainNameOverAlternatenameMatch(t *testing.T) {
	curated := []oldCitySeed{
		{Key: "us:manhattan", Name: "Manhattan", Country: "US", Population: 1_487_536},
	}
	rows := []geoRow{
		{
			ID: "real-manhattan", Name: "Manhattan", ASCIIName: "Manhattan",
			Country: "US", Population: 1_487_536, Lat: 40.78343, Lng: -73.96625,
			HasCoords: true, Admin1Code: "NY",
		},
		{
			ID: "nyc", Name: "New York City", ASCIIName: "New York City",
			AltNames: []string{"manhattan"}, Country: "US", Population: 8_804_190,
			Lat: 40.71427, Lng: -74.00597, HasCoords: true, Admin1Code: "NY",
		},
	}
	nameIndex := buildNameIndex(rows)
	admin1Names := map[string]string{"US.NY": "New York"}

	res, err := merge(curated, rows, nameIndex, perCountryCap, admin1Names)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// NYC's row is a distinct, unclaimed GeoNames row, so it legitimately
	// survives as its own long-tail city alongside the curated Manhattan —
	// only "us:manhattan" itself is under test here.
	var got *outputCity
	for i := range res.cities {
		if res.cities[i].Key == "us:manhattan" {
			got = &res.cities[i]
		}
	}
	if got == nil {
		t.Fatalf("expected an output city with key us:manhattan, got %+v", res.cities)
	}
	if got.Population != 1_487_536 {
		t.Errorf("us:manhattan must match the real Manhattan row (pop 1,487,536), not NYC's row (pop 8,804,190); got %d", got.Population)
	}
	if got.GeonameID != "real-manhattan" {
		t.Errorf("us:manhattan must claim the real Manhattan geonameid, got %q", got.GeonameID)
	}
}
