package capitals_test

// Integration test against a real PostgreSQL, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, mirroring
// internal/topics/tld/integration_test.go). Seeds a fresh schema and
// asserts the design invariants: both direction-topics exist with the right
// quiz_kind + exercise_modes, one item per country-with-a-capital in each,
// direction parity, exactly one capital fact per such country, item
// payload/fact consistency, and countrytier-derived tiers.
//
// WARNING: freshSchema below drops every table (it exercises the down
// migration), so the target MUST be a disposable database whose name
// contains "test" — see testDSN's safety fuse, copied from tld's
// integration test.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/capitals"
	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run capitals integration tests")
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

	// roadside owns country seeding; capitals resolves its references
	// against it.
	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}
	if err := capitals.Seed(ctx, store); err != nil {
		t.Fatalf("seed capitals: %v", err)
	}
	// Idempotency: reseeding must converge, not duplicate or error.
	if err := capitals.Seed(ctx, store); err != nil {
		t.Fatalf("reseed capitals: %v", err)
	}

	const wantEntries = 242 // seeds/capitals.yaml entries with a capitals: list

	// Both direction-topics exist with the right quiz_kind + exercise modes.
	c2c, found, err := store.GetTopicByPath(ctx, capitals.CountriesRootSlug+"/"+capitals.CapitalsSlug+"/"+capitals.CountryToCapitalSlug)
	if err != nil || !found {
		t.Fatalf("get country-to-capital topic: found=%v err=%v", found, err)
	}
	if c2c.QuizKind != capitals.KindCountryToCapital {
		t.Fatalf("country-to-capital quiz_kind = %q, want %q", c2c.QuizKind, capitals.KindCountryToCapital)
	}
	if len(c2c.ExerciseModes) != 1 || c2c.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("country-to-capital exercise_modes = %v, want [autocomplete]", c2c.ExerciseModes)
	}

	cap2c, found, err := store.GetTopicByPath(ctx, capitals.CountriesRootSlug+"/"+capitals.CapitalsSlug+"/"+capitals.CapitalToCountrySlug)
	if err != nil || !found {
		t.Fatalf("get capital-to-country topic: found=%v err=%v", found, err)
	}
	if cap2c.QuizKind != capitals.KindCapitalToCountry {
		t.Fatalf("capital-to-country quiz_kind = %q, want %q", cap2c.QuizKind, capitals.KindCapitalToCountry)
	}
	if len(cap2c.ExerciseModes) != 1 || cap2c.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("capital-to-country exercise_modes = %v, want [autocomplete]", cap2c.ExerciseModes)
	}

	// The container topic is not quizzable.
	container, found, err := store.GetTopicByPath(ctx, capitals.CountriesRootSlug+"/"+capitals.CapitalsSlug)
	if err != nil || !found {
		t.Fatalf("get capitals container: found=%v err=%v", found, err)
	}
	if container.IsQuizzable {
		t.Fatalf("capitals container should not be quizzable")
	}

	// Exactly one capital fact per country that has one.
	capDef, found, err := store.GetFactDefByKey(ctx, capitals.FactKeyCapital)
	if err != nil || !found {
		t.Fatalf("get capital fact def: found=%v err=%v", found, err)
	}
	facts, err := store.ListCountryFactsByDefKey(ctx, capitals.FactKeyCapital)
	if err != nil {
		t.Fatalf("list capital facts: %v", err)
	}
	if len(facts) != wantEntries {
		t.Fatalf("len(capital facts) = %d, want %d", len(facts), wantEntries)
	}
	factCountByCountry := make(map[uuid.UUID]int, wantEntries)
	factCapitalByCountry := make(map[uuid.UUID]string, wantEntries)
	for _, f := range facts {
		if f.FactDefID != capDef.ID {
			t.Fatalf("fact %+v has unexpected fact_def_id", f)
		}
		if f.ValText == nil {
			t.Fatalf("capital fact for country %v has nil val_text", f.CountryID)
		}
		factCountByCountry[f.CountryID]++
		factCapitalByCountry[f.CountryID] = *f.ValText
	}
	for cid, n := range factCountByCountry {
		if n != 1 {
			t.Fatalf("country %v has %d capital facts, want exactly 1", cid, n)
		}
	}

	countries, err := store.ListCountries(ctx)
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	countryByID := make(map[uuid.UUID]storage.Country, len(countries))
	for _, c := range countries {
		countryByID[c.ID] = c
	}

	// Per-topic item assertions + gather the two country-id sets for parity.
	c2cCountrySet := assertTopicItems(t, ctx, store, c2c, wantEntries, countryByID, factCapitalByCountry)
	cap2cCountrySet := assertTopicItems(t, ctx, store, cap2c, wantEntries, countryByID, factCapitalByCountry)

	// Direction parity: identical country coverage in both topics (mirrors
	// tld's design §8 invariant) — the deliberate primary-only-for-
	// capital->country rule (see generator.go's package doc) keeps both
	// item sets exactly the same countries, never a superset/subset.
	if len(c2cCountrySet) != len(cap2cCountrySet) {
		t.Fatalf("direction parity: %d countries under country-to-capital, %d under capital-to-country", len(c2cCountrySet), len(cap2cCountrySet))
	}
	for cid := range c2cCountrySet {
		if !cap2cCountrySet[cid] {
			t.Fatalf("country %v present under country-to-capital but not capital-to-country", cid)
		}
	}

	// Spot checks on the fact value, including a multi-capital country
	// (primary only) and a few famous single-capital ones.
	spotCapital := map[string]string{"CO": "Bogotá", "FR": "Paris", "JP": "Tokyo", "ZA": "Pretoria", "BO": "Sucre"}
	for iso, wantCapital := range spotCapital {
		c, found, err := store.GetCountryByISO(ctx, iso)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", iso, found, err)
		}
		if got := factCapitalByCountry[c.ID]; got != wantCapital {
			t.Fatalf("capital fact for %s = %q, want %q", iso, got, wantCapital)
		}
	}

	// Countries with no capital (e.g. Antarctica) have no item in either
	// direction.
	aq, found, err := store.GetCountryByISO(ctx, "AQ")
	if err != nil || !found {
		t.Fatalf("get AQ: found=%v err=%v", found, err)
	}
	if c2cCountrySet[aq.ID] {
		t.Fatalf("AQ (no capital) should not have a country-to-capital item")
	}

	// Tier rubric sanity: the universally-known set is tier 0 regardless of
	// flags (per-item tiers are already validated against countrytier above).
	if countrytier.Tier("US", true, true) != 0 || countrytier.Tier("FR", true, true) != 0 {
		t.Fatalf("countrytier rubric sanity: US/FR should be tier 0")
	}
}

// assertTopicItems checks a direction-topic's items: count, key = iso2,
// country_id set, payload internally consistent and matching the capital
// fact (always the primary), and tier = countrytier rubric. Returns the set
// of country_ids seen (for the direction-parity check).
func assertTopicItems(t *testing.T, ctx context.Context, store *storage.Store, topic storage.Topic, want int, countryByID map[uuid.UUID]storage.Country, factCapital map[uuid.UUID]string) map[uuid.UUID]bool {
	t.Helper()
	items, err := store.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items for %s: %v", topic.Slug, err)
	}
	if len(items) != want {
		t.Fatalf("%s: len(items) = %d, want %d", topic.Slug, len(items), want)
	}

	set := make(map[uuid.UUID]bool, len(items))
	for _, it := range items {
		if it.CountryID == nil {
			t.Fatalf("%s item %s has no country_id", topic.Slug, it.Key)
		}
		country, ok := countryByID[*it.CountryID]
		if !ok {
			t.Fatalf("%s item %s references unknown country_id %v", topic.Slug, it.Key, *it.CountryID)
		}
		if it.Key != country.ISOA2 {
			t.Fatalf("%s item key = %q, want country iso_a2 %q", topic.Slug, it.Key, country.ISOA2)
		}
		set[*it.CountryID] = true

		var p struct {
			Name    string `json:"name"`
			ISOA2   string `json:"iso_a2"`
			ISOA3   string `json:"iso_a3"`
			Capital string `json:"capital"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("%s item %s: unmarshal payload: %v", topic.Slug, it.Key, err)
		}
		if p.ISOA2 != country.ISOA2 || p.Name != country.Name || p.ISOA3 != country.ISOA3 {
			t.Fatalf("%s item %s payload {%s,%s,%s} disagrees with country {%s,%s,%s}",
				topic.Slug, it.Key, p.ISOA2, p.Name, p.ISOA3, country.ISOA2, country.Name, country.ISOA3)
		}
		if p.Capital != factCapital[*it.CountryID] {
			t.Fatalf("%s item %s payload.capital = %q, but fact says %q", topic.Slug, it.Key, p.Capital, factCapital[*it.CountryID])
		}
		if it.Tier == nil {
			t.Fatalf("%s item %s has nil tier (want explicit countrytier override)", topic.Slug, it.Key)
		}
		if wantTier := countrytier.Tier(country.ISOA2, country.UNMember, country.GGCoverage); *it.Tier != wantTier {
			t.Fatalf("%s item %s tier = %d, want %d (countrytier)", topic.Slug, it.Key, *it.Tier, wantTier)
		}
	}
	return set
}
