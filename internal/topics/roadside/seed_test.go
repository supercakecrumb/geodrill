package roadside

import "testing"

func TestCountryTier(t *testing.T) {
	tests := []struct {
		name     string
		iso      string
		unMember bool
		gg       bool
		want     int16
	}{
		{"tier0 core us", "US", true, true, 0},
		{"tier0 core gb", "GB", true, true, 0},
		{"tier0 core fr", "FR", true, true, 0},
		{"tier0 core de", "DE", true, true, 0},
		{"tier0 core jp", "JP", true, true, 0},
		{"tier0 core ca", "CA", true, true, 0},
		{"tier0 core au", "AU", true, true, 0},
		{"tier0 core it", "IT", true, true, 0},
		{"tier0 core es", "ES", true, true, 0},
		{"tier1 g20 brazil", "BR", true, true, 1},
		{"tier1 g20 india", "IN", true, true, 1},
		{"tier1 g20 china", "CN", true, true, 1},
		{"tier1 g20 turkey", "TR", true, false, 1},
		{"tier1 wins even without coverage", "RU", true, false, 1},
		{"tier2 un member with coverage", "PL", true, true, 2},
		{"tier3 un member without coverage", "AF", true, false, 3},
		{"tier4 non-un territory with coverage", "AI", false, true, 4},
		{"tier4 non-un subdivision", "GB-ENG", false, true, 4},
		{"tier4 non-un no coverage", "AQ", false, false, 4},
		{"tier0 takes priority over g20 status", "US", true, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := countryTier(tc.iso, tc.unMember, tc.gg)
			if got != tc.want {
				t.Fatalf("countryTier(%q, %v, %v) = %d, want %d", tc.iso, tc.unMember, tc.gg, got, tc.want)
			}
		})
	}
}

// TestTierSetSizes guards the hardcoded tier0/tier1 membership lists against
// accidental additions/removals: tier0 is the task-specified 9-country set,
// tier1 is the 19 G20 country members (EU excluded, not a country) minus
// the 8 of those 9 tier0 countries that are also G20 members (ES is not a
// G20 country member, so 19-8=11).
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

func TestNormalizeSide(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"left", "left", false},
		{"right", "right", false},
		{"LEFT", "", true},
		{"", "", true},
		{"top", "", true},
	}
	for _, tc := range tests {
		got, err := normalizeSide(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("normalizeSide(%q) err = nil, want error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeSide(%q) unexpected error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeSide(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSideCode(t *testing.T) {
	if got := sideCode("left"); got != "L" {
		t.Fatalf("sideCode(\"left\") = %q, want %q", got, "L")
	}
	if got := sideCode("right"); got != "R" {
		t.Fatalf("sideCode(\"right\") = %q, want %q", got, "R")
	}
}

// TestLoadCountriesRealSeed parses the real seeds/countries.yaml (no DB
// needed) so a structural break in the committed data fails fast without
// requiring GEODRILL_TEST_DATABASE_URL.
func TestLoadCountriesRealSeed(t *testing.T) {
	sf, err := loadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("loadCountriesFile: %v", err)
	}
	if len(sf.Countries) != 252 {
		t.Fatalf("len(countries) = %d, want 252", len(sf.Countries))
	}

	var unCount, subdivCount int
	byISO := make(map[string]countrySeed, len(sf.Countries))
	for _, c := range sf.Countries {
		if c.ISOA2 == "" {
			t.Fatalf("country %+v has empty iso_a2", c)
		}
		if _, dup := byISO[c.ISOA2]; dup {
			t.Fatalf("duplicate iso_a2 %q in countries.yaml", c.ISOA2)
		}
		byISO[c.ISOA2] = c
		if c.UNMember {
			unCount++
		}
		if c.Subdivision {
			subdivCount++
		}
	}
	if unCount != 193 {
		t.Fatalf("un_member count = %d, want 193", unCount)
	}
	if subdivCount != 3 {
		t.Fatalf("subdivision count = %d, want 3 (GB-ENG/GB-SCT/GB-WLS)", subdivCount)
	}

	for _, c := range sf.Countries {
		if c.Parent == "" {
			continue
		}
		if _, ok := byISO[c.Parent]; !ok {
			t.Fatalf("country %s references unknown parent %q", c.ISOA2, c.Parent)
		}
	}
}

// TestLoadRoadSidesRealSeed parses the real seeds/road_sides.yaml and
// cross-checks it against countries.yaml: every road-side entry must
// reference a real country and vice versa (1:1 coverage), and every side
// value must be a valid "left"/"right".
func TestLoadRoadSidesRealSeed(t *testing.T) {
	countries, err := loadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("loadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries.Countries))
	for _, c := range countries.Countries {
		countryISO[c.ISOA2] = true
	}

	sf, err := loadRoadSidesFile(roadSidesSeedPath())
	if err != nil {
		t.Fatalf("loadRoadSidesFile: %v", err)
	}
	if len(sf.RoadSides) != 252 {
		t.Fatalf("len(road_sides) = %d, want 252", len(sf.RoadSides))
	}

	seen := make(map[string]bool, len(sf.RoadSides))
	for _, rs := range sf.RoadSides {
		if seen[rs.Country] {
			t.Fatalf("duplicate road-side entry for %q", rs.Country)
		}
		seen[rs.Country] = true
		if !countryISO[rs.Country] {
			t.Fatalf("road-side entry %q has no matching country in countries.yaml", rs.Country)
		}
		if _, err := normalizeSide(rs.Side); err != nil {
			t.Fatalf("road-side entry %q: %v", rs.Country, err)
		}
	}
	for iso := range countryISO {
		if !seen[iso] {
			t.Fatalf("country %q has no road-side entry", iso)
		}
	}
}
