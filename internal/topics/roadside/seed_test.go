package roadside

import (
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

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
// needed, via the engine's shared loader) so a structural break in the
// committed data fails fast without requiring GEODRILL_TEST_DATABASE_URL.
func TestLoadCountriesRealSeed(t *testing.T) {
	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	if len(countries) != 252 {
		t.Fatalf("len(countries) = %d, want 252", len(countries))
	}

	var unCount, subdivCount int
	byISO := make(map[string]engine.CountrySeed, len(countries))
	for _, c := range countries {
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

	for _, c := range countries {
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
	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries))
	for _, c := range countries {
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
