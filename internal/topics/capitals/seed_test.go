package capitals

import (
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// TestLoadCapitalsRealSeed parses the committed seeds/capitals.yaml and
// cross-checks it against seeds/countries.yaml: every entry references a
// real country, no iso is duplicated, and the with/without-capital split
// matches the known set of capital-less territories.
func TestLoadCapitalsRealSeed(t *testing.T) {
	sf, err := loadCapitalsFile(capitalsSeedPath())
	if err != nil {
		t.Fatalf("loadCapitalsFile: %v", err)
	}
	if len(sf.Entries) != 249 {
		t.Fatalf("len(entries) = %d, want 249", len(sf.Entries))
	}

	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries))
	for _, c := range countries {
		countryISO[c.ISOA2] = true
	}

	seen := make(map[string]bool, len(sf.Entries))
	withCapital, withoutCapital := 0, 0
	for _, e := range sf.Entries {
		if seen[e.Country] {
			t.Fatalf("duplicate capitals entry for %q", e.Country)
		}
		seen[e.Country] = true
		if !countryISO[e.Country] {
			t.Fatalf("capitals entry %q has no matching country in countries.yaml", e.Country)
		}
		if len(e.Capitals) == 0 {
			withoutCapital++
			if e.Note == "" {
				t.Fatalf("capitals entry %q has no capitals and no note explaining why", e.Country)
			}
			continue
		}
		withCapital++
		for _, c := range e.Capitals {
			if c.Name == "" {
				t.Fatalf("capitals entry %q has a capital with an empty name", e.Country)
			}
			if c.Role == "" {
				t.Fatalf("capitals entry %q capital %q has an empty role", e.Country, c.Name)
			}
		}
	}
	if withCapital != 242 {
		t.Fatalf("entries with a capital = %d, want 242", withCapital)
	}
	if withoutCapital != 7 {
		t.Fatalf("entries without a capital = %d, want 7", withoutCapital)
	}

	// Spot checks from the task brief: famous and multi-capital cases.
	byISO := make(map[string]capitalSeed, len(sf.Entries))
	for _, e := range sf.Entries {
		byISO[e.Country] = e
	}
	if got := byISO["CO"].Capitals[0].Name; got != "Bogotá" {
		t.Fatalf("CO primary capital = %q, want Bogotá", got)
	}
	// South Africa: three capitals, primary (executive) first.
	za := byISO["ZA"].Capitals
	if len(za) != 3 || za[0].Name != "Pretoria" {
		t.Fatalf("ZA capitals = %v, want [Pretoria(executive) Cape Town(legislative) Bloemfontein(judicial)]", za)
	}
	// Bolivia: constitutional capital Sucre is primary, La Paz is the seat.
	bo := byISO["BO"].Capitals
	if len(bo) != 2 || bo[0].Name != "Sucre" || bo[1].Name != "La Paz" {
		t.Fatalf("BO capitals = %v, want [Sucre La Paz]", bo)
	}
	// Antarctica has no capital at all.
	if len(byISO["AQ"].Capitals) != 0 {
		t.Fatalf("AQ capitals = %v, want none", byISO["AQ"].Capitals)
	}
}

// TestLookupTables builds the real lookup tables and asserts the entries
// both descriptors' ModeText hooks need resolve correctly, including the
// multi-capital Accept case.
func TestLookupTables(t *testing.T) {
	tbl, err := loadLookupTables()
	if err != nil {
		t.Fatalf("loadLookupTables: %v", err)
	}
	if tbl.countryLabels["CO"] != "Colombia" {
		t.Fatalf("countryLabels[CO] = %q, want Colombia", tbl.countryLabels["CO"])
	}
	if tbl.capitalLabels["CO"] != "Bogotá" {
		t.Fatalf("capitalLabels[CO] = %q, want Bogotá", tbl.capitalLabels["CO"])
	}
	if !contains(tbl.countryAccept["US"], "USA") {
		t.Fatalf("countryAccept[US] = %v, want to contain USA", tbl.countryAccept["US"])
	}

	// Multi-capital Accept: South Africa's country->capital Accept must
	// contain all three listed capitals, but its CorrectAnswer label (the
	// primary) must still be Pretoria.
	if tbl.capitalLabels["ZA"] != "Pretoria" {
		t.Fatalf("capitalLabels[ZA] = %q, want Pretoria", tbl.capitalLabels["ZA"])
	}
	for _, want := range []string{"Pretoria", "Cape Town", "Bloemfontein"} {
		if !contains(tbl.capitalAccept["ZA"], want) {
			t.Fatalf("capitalAccept[ZA] = %v, want to contain %q", tbl.capitalAccept["ZA"], want)
		}
	}

	// Countries without a capital have no entry in either capital table.
	if _, ok := tbl.capitalLabels["AQ"]; ok {
		t.Fatalf("capitalLabels[AQ] should not exist (Antarctica has no capital)")
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
