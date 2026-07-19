package profiles_test

// Integration test against a real PostgreSQL, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, mirroring
// internal/topics/tld/integration_test.go and capitals/integration_test.go).
// Seeds a fresh schema and asserts: the countries/profiles/language topic
// exists with the right quiz_kind + exercise_modes, the container topics are
// not quizzable, all three fact_defs exist with the right cardinalities and
// counts, one item per country, item payload / fact consistency, and
// countrytier-derived tiers.
//
// WARNING: freshSchema below drops every table (it exercises the down
// migration), so the target MUST be a disposable database whose name
// contains "test" — see testDSN's safety fuse, copied from tld's integration
// test.

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
	"github.com/supercakecrumb/geodrill/internal/topics/profiles"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run profiles integration tests")
	}
	if name := databaseName(dsn); !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("refusing to run destructive integration tests against database %q: "+
			"GEODRILL_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\" "+
			"(e.g. geodrill_test), never the live database", name)
	}
	return dsn
}

func databaseName(dsn string) string {
	s := dsn
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return ""
	}
	return strings.Trim(s[i+1:], "/")
}

func freshSchema(t *testing.T, dsn string) {
	t.Helper()
	url := storage.MigrateURL(dsn)
	if err := storage.MigrateUp(url); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if err := storage.MigrateDown(url); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := storage.MigrateUp(url); err != nil {
		t.Fatalf("migrate up (again): %v", err)
	}
}

func TestSeedAndConsistency(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// roadside owns country seeding; profiles resolves its references
	// against it.
	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}
	if err := profiles.Seed(ctx, store); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	// Idempotency: reseeding must converge, not duplicate or error.
	if err := profiles.Seed(ctx, store); err != nil {
		t.Fatalf("reseed profiles: %v", err)
	}

	const wantCountries = 247    // seeds/country_profiles.yaml entries
	const wantLanguageRows = 374 // total languages across all entries (multi-valued fact)

	// The quiz-bearing leaf exists with the right quiz_kind + exercise mode.
	leaf, found, err := store.GetTopicByPath(ctx, profiles.CountriesRootSlug+"/"+profiles.ProfilesSlug+"/"+profiles.LanguageSlug)
	if err != nil || !found {
		t.Fatalf("get country-to-language topic: found=%v err=%v", found, err)
	}
	if leaf.QuizKind != profiles.Kind {
		t.Fatalf("country-to-language quiz_kind = %q, want %q", leaf.QuizKind, profiles.Kind)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "single" {
		t.Fatalf("country-to-language exercise_modes = %v, want [single]", leaf.ExerciseModes)
	}
	if !leaf.IsQuizzable {
		t.Fatalf("country-to-language topic should be quizzable")
	}

	// The container topics are not quizzable.
	for _, path := range []string{
		profiles.CountriesRootSlug,
		profiles.CountriesRootSlug + "/" + profiles.ProfilesSlug,
	} {
		container, found, err := store.GetTopicByPath(ctx, path)
		if err != nil || !found {
			t.Fatalf("get container %q: found=%v err=%v", path, found, err)
		}
		if container.IsQuizzable {
			t.Fatalf("container %q should not be quizzable", path)
		}
	}

	// All three fact_defs exist with the right cardinality.
	wantCardinality := map[string]string{
		profiles.FactKeyRegion:          "single",
		profiles.FactKeyMainReligion:    "single",
		profiles.FactKeyLanguagesSpoken: "multi",
	}
	for key, wantCard := range wantCardinality {
		fd, found, err := store.GetFactDefByKey(ctx, key)
		if err != nil || !found {
			t.Fatalf("get fact def %q: found=%v err=%v", key, found, err)
		}
		if fd.Cardinality != wantCard {
			t.Fatalf("fact def %q cardinality = %q, want %q", key, fd.Cardinality, wantCard)
		}
	}

	// Exactly one region and one main_religion fact per country; the right
	// total row count for the multi-valued languages_spoken fact.
	regionFacts, err := store.ListCountryFactsByDefKey(ctx, profiles.FactKeyRegion)
	if err != nil {
		t.Fatalf("list region facts: %v", err)
	}
	if len(regionFacts) != wantCountries {
		t.Fatalf("len(region facts) = %d, want %d", len(regionFacts), wantCountries)
	}
	religionFacts, err := store.ListCountryFactsByDefKey(ctx, profiles.FactKeyMainReligion)
	if err != nil {
		t.Fatalf("list main_religion facts: %v", err)
	}
	if len(religionFacts) != wantCountries {
		t.Fatalf("len(main_religion facts) = %d, want %d", len(religionFacts), wantCountries)
	}
	languageFacts, err := store.ListCountryFactsByDefKey(ctx, profiles.FactKeyLanguagesSpoken)
	if err != nil {
		t.Fatalf("list languages_spoken facts: %v", err)
	}
	if len(languageFacts) != wantLanguageRows {
		t.Fatalf("len(languages_spoken facts) = %d, want %d", len(languageFacts), wantLanguageRows)
	}

	regionByCountry := make(map[uuid.UUID]string, wantCountries)
	for _, f := range regionFacts {
		if f.ValText == nil {
			t.Fatalf("region fact for country %v has nil val_text", f.CountryID)
		}
		regionByCountry[f.CountryID] = *f.ValText
	}
	languagesByCountry := make(map[uuid.UUID][]string, wantCountries)
	for _, f := range languageFacts {
		if f.ValText == nil {
			t.Fatalf("languages_spoken fact for country %v has nil val_text", f.CountryID)
		}
		languagesByCountry[f.CountryID] = append(languagesByCountry[f.CountryID], *f.ValText)
	}

	countries, err := store.ListCountries(ctx)
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	countryByID := make(map[uuid.UUID]storage.Country, len(countries))
	for _, c := range countries {
		countryByID[c.ID] = c
	}

	// Item assertions: count, key = iso2, country_id set, payload internally
	// consistent and matching the facts (region and the full language set —
	// order doesn't need to match exactly since ListCountryFactsByDefKey's
	// row order isn't guaranteed, so compare as sets), and tier =
	// countrytier rubric.
	items, err := store.ListItemsByTopic(ctx, leaf.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != wantCountries {
		t.Fatalf("len(items) = %d, want %d", len(items), wantCountries)
	}
	seenCountry := make(map[uuid.UUID]bool, len(items))
	for _, it := range items {
		if it.CountryID == nil {
			t.Fatalf("item %s has no country_id", it.Key)
		}
		country, ok := countryByID[*it.CountryID]
		if !ok {
			t.Fatalf("item %s references unknown country_id %v", it.Key, *it.CountryID)
		}
		if it.Key != country.ISOA2 {
			t.Fatalf("item key = %q, want country iso_a2 %q", it.Key, country.ISOA2)
		}
		if seenCountry[*it.CountryID] {
			t.Fatalf("country %v (%s) has more than one item", *it.CountryID, country.ISOA2)
		}
		seenCountry[*it.CountryID] = true

		var p struct {
			Name      string   `json:"name"`
			ISOA2     string   `json:"iso_a2"`
			ISOA3     string   `json:"iso_a3"`
			Region    string   `json:"region"`
			Languages []string `json:"languages"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("item %s: unmarshal payload: %v", it.Key, err)
		}
		if p.ISOA2 != country.ISOA2 || p.Name != country.Name || p.ISOA3 != country.ISOA3 {
			t.Fatalf("item %s payload {%s,%s,%s} disagrees with country {%s,%s,%s}",
				it.Key, p.ISOA2, p.Name, p.ISOA3, country.ISOA2, country.Name, country.ISOA3)
		}
		if p.Region != regionByCountry[*it.CountryID] {
			t.Fatalf("item %s payload.region = %q, but fact says %q", it.Key, p.Region, regionByCountry[*it.CountryID])
		}
		if len(p.Languages) == 0 {
			t.Fatalf("item %s payload has no languages", it.Key)
		}
		wantLangs := append([]string(nil), languagesByCountry[*it.CountryID]...)
		gotLangs := append([]string(nil), p.Languages...)
		sort.Strings(wantLangs)
		sort.Strings(gotLangs)
		if len(wantLangs) != len(gotLangs) {
			t.Fatalf("item %s payload.languages = %v, but fact rows say %v", it.Key, p.Languages, languagesByCountry[*it.CountryID])
		}
		for i := range wantLangs {
			if wantLangs[i] != gotLangs[i] {
				t.Fatalf("item %s payload.languages = %v, but fact rows say %v", it.Key, p.Languages, languagesByCountry[*it.CountryID])
			}
		}

		if it.Tier == nil {
			t.Fatalf("item %s has nil tier (want explicit countrytier override)", it.Key)
		}
		if wantTier := countrytier.Tier(country.ISOA2, country.UNMember, country.GGCoverage); *it.Tier != wantTier {
			t.Fatalf("item %s tier = %d, want %d (countrytier)", it.Key, *it.Tier, wantTier)
		}
	}

	// Spot checks: the task brief's own Kenya example, and a thin-region
	// (North Africa) country.
	spotPrimary := map[string]string{"KE": "Swahili", "FR": "French", "EG": "Arabic"}
	for iso, wantPrimary := range spotPrimary {
		c, found, err := store.GetCountryByISO(ctx, iso)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", iso, found, err)
		}
		langs := languagesByCountry[c.ID]
		if len(langs) == 0 {
			t.Fatalf("%s has no languages_spoken facts", iso)
		}
		// The payload's languages[0] must be the primary; compare against
		// the item directly rather than the (order-unstable) fact rows.
		var p struct {
			Languages []string `json:"languages"`
		}
		for _, it := range items {
			if it.Key == iso {
				if err := json.Unmarshal(it.Payload, &p); err != nil {
					t.Fatalf("unmarshal %s payload: %v", iso, err)
				}
			}
		}
		if p.Languages[0] != wantPrimary {
			t.Fatalf("%s primary language = %q, want %q", iso, p.Languages[0], wantPrimary)
		}
	}
}
