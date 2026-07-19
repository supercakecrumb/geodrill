package profiles

import (
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// regionTaxonomy is the fixed 19-value region taxonomy seeds/country_profiles.yaml
// declares in its header comment ("region uses the fixed taxonomy below — no
// other values allowed"). Pinned here so a future data edit that introduces a
// stray region value fails this test rather than silently fragmenting the
// same-region distractor pool.
var regionTaxonomy = map[string]bool{
	"Caribbean": true, "Central Africa": true, "Central America": true,
	"Central Asia": true, "East Africa": true, "East Asia": true,
	"Eastern Europe": true, "Middle East": true, "North Africa": true,
	"North America": true, "Northern Europe": true, "Oceania": true,
	"South America": true, "South Asia": true, "Southeast Asia": true,
	"Southern Africa": true, "Southern Europe": true, "West Africa": true,
	"Western Europe": true,
}

// TestLoadProfilesRealSeed parses the committed seeds/country_profiles.yaml
// and cross-checks it against seeds/countries.yaml: every entry references a
// real country, no iso is duplicated, every language is non-empty, and every
// region is a member of the fixed taxonomy. Runs without a DB so a structural
// break in the data fails fast.
func TestLoadProfilesRealSeed(t *testing.T) {
	sf, err := loadProfilesFile(profilesSeedPath())
	if err != nil {
		t.Fatalf("loadProfilesFile: %v", err)
	}
	if len(sf.Profiles) != 247 {
		t.Fatalf("len(profiles) = %d, want 247", len(sf.Profiles))
	}

	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries))
	for _, c := range countries {
		countryISO[c.ISOA2] = true
	}

	totalLanguageRows := 0
	seen := make(map[string]bool, len(sf.Profiles))
	for _, e := range sf.Profiles {
		if seen[e.Country] {
			t.Fatalf("duplicate profiles entry for %q", e.Country)
		}
		seen[e.Country] = true
		if !countryISO[e.Country] {
			t.Fatalf("profiles entry %q has no matching country in countries.yaml", e.Country)
		}
		if e.MainReligion == "" {
			t.Fatalf("profiles entry %q has an empty main_religion", e.Country)
		}
		if !regionTaxonomy[e.Region] {
			t.Fatalf("profiles entry %q has region %q, not in the fixed 19-value taxonomy", e.Country, e.Region)
		}
		if len(e.Languages) == 0 {
			t.Fatalf("profiles entry %q has no languages", e.Country)
		}
		for _, l := range e.Languages {
			if l == "" {
				t.Fatalf("profiles entry %q has an empty language", e.Country)
			}
		}
		totalLanguageRows += len(e.Languages)
	}
	if totalLanguageRows != 374 {
		t.Fatalf("total language rows = %d, want 374", totalLanguageRows)
	}

	// Spot checks: the task brief's own Kenya example, plus a multi-language
	// country and a thin-region (North Africa, all-Arabic) country.
	byISO := make(map[string]profileSeed, len(sf.Profiles))
	for _, e := range sf.Profiles {
		byISO[e.Country] = e
	}
	ke := byISO["KE"]
	if len(ke.Languages) != 2 || ke.Languages[0] != "Swahili" || ke.Languages[1] != "English" {
		t.Fatalf("KE languages = %v, want [Swahili English]", ke.Languages)
	}
	if ke.Region != "East Africa" {
		t.Fatalf("KE region = %q, want East Africa", ke.Region)
	}
	eg := byISO["EG"]
	if eg.Region != "North Africa" || eg.Languages[0] != "Arabic" {
		t.Fatalf("EG = %+v, want region North Africa, primary language Arabic", eg)
	}
}

// TestLookupTables builds the real language-label lookup table and asserts
// it resolves the task brief's own examples with no key collisions.
func TestLookupTables(t *testing.T) {
	tbl, err := loadLookupTables()
	if err != nil {
		t.Fatalf("loadLookupTables: %v", err)
	}
	if got := tbl.languageLabels[languageKey("Swahili")]; got != "Swahili" {
		t.Fatalf("languageLabels[swahili] = %q, want Swahili", got)
	}
	if got := tbl.languageLabels[languageKey("Arabic")]; got != "Arabic" {
		t.Fatalf("languageLabels[arabic] = %q, want Arabic", got)
	}
	if len(tbl.languageLabels) == 0 {
		t.Fatalf("languageLabels is empty")
	}
}
