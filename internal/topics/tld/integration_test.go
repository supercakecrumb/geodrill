package tld_test

// Integration test against a real PostgreSQL, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, mirroring
// internal/topics/roadside/integration_test.go). Seeds a fresh schema and
// asserts the design §5/§8 invariants: both direction-topics exist with the
// right quiz_kind + exercise_modes, one item per country in each, direction
// parity, exactly one tld fact per country, item payload / fact consistency,
// and countrytier-derived tiers.
//
// WARNING: freshSchema below drops every table (it exercises the down
// migration), so the target MUST be a disposable database whose name contains
// "test" — see testDSN's safety fuse, copied from roadside's integration test.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
	"github.com/supercakecrumb/geodrill/internal/topics/tld"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run tld integration tests")
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

	// roadside owns country seeding; tld resolves its references against it.
	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}
	if err := tld.Seed(ctx, store); err != nil {
		t.Fatalf("seed tld: %v", err)
	}
	// Idempotency: reseeding must converge, not duplicate or error.
	if err := tld.Seed(ctx, store); err != nil {
		t.Fatalf("reseed tld: %v", err)
	}

	const wantEntries = 248

	// Both direction-topics exist with the right quiz_kind + exercise modes.
	t2c, found, err := store.GetTopicByPath(ctx, tld.CountriesRootSlug+"/"+tld.DomainsSlug+"/"+tld.TLDToCountrySlug)
	if err != nil || !found {
		t.Fatalf("get tld-to-country topic: found=%v err=%v", found, err)
	}
	if t2c.QuizKind != tld.KindTLDToCountry {
		t.Fatalf("tld-to-country quiz_kind = %q, want %q", t2c.QuizKind, tld.KindTLDToCountry)
	}
	if len(t2c.ExerciseModes) != 1 || t2c.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("tld-to-country exercise_modes = %v, want [autocomplete]", t2c.ExerciseModes)
	}

	c2t, found, err := store.GetTopicByPath(ctx, tld.CountriesRootSlug+"/"+tld.DomainsSlug+"/"+tld.CountryToTLDSlug)
	if err != nil || !found {
		t.Fatalf("get country-to-tld topic: found=%v err=%v", found, err)
	}
	if c2t.QuizKind != tld.KindCountryToTLD {
		t.Fatalf("country-to-tld quiz_kind = %q, want %q", c2t.QuizKind, tld.KindCountryToTLD)
	}
	if len(c2t.ExerciseModes) != 1 || c2t.ExerciseModes[0] != "text" {
		t.Fatalf("country-to-tld exercise_modes = %v, want [text]", c2t.ExerciseModes)
	}

	// The container topics are not quizzable.
	domains, found, err := store.GetTopicByPath(ctx, tld.CountriesRootSlug+"/"+tld.DomainsSlug)
	if err != nil || !found {
		t.Fatalf("get domains container: found=%v err=%v", found, err)
	}
	if domains.IsQuizzable {
		t.Fatalf("domains container should not be quizzable")
	}

	// Exactly one tld fact per referenced country.
	tldDef, found, err := store.GetFactDefByKey(ctx, tld.FactKeyTLD)
	if err != nil || !found {
		t.Fatalf("get tld fact def: found=%v err=%v", found, err)
	}
	facts, err := store.ListCountryFactsByDefKey(ctx, tld.FactKeyTLD)
	if err != nil {
		t.Fatalf("list tld facts: %v", err)
	}
	if len(facts) != wantEntries {
		t.Fatalf("len(tld facts) = %d, want %d", len(facts), wantEntries)
	}
	factCountByCountry := make(map[uuid.UUID]int, wantEntries)
	factTLDByCountry := make(map[uuid.UUID]string, wantEntries)
	for _, f := range facts {
		if f.FactDefID != tldDef.ID {
			t.Fatalf("fact %+v has unexpected fact_def_id", f)
		}
		if f.ValText == nil {
			t.Fatalf("tld fact for country %v has nil val_text", f.CountryID)
		}
		factCountByCountry[f.CountryID]++
		factTLDByCountry[f.CountryID] = *f.ValText
	}
	for cid, n := range factCountByCountry {
		if n != 1 {
			t.Fatalf("country %v has %d tld facts, want exactly 1", cid, n)
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
	t2cCountrySet := assertTopicItems(t, ctx, store, t2c, wantEntries, countryByID, factTLDByCountry)
	c2tCountrySet := assertTopicItems(t, ctx, store, c2t, wantEntries, countryByID, factTLDByCountry)

	// Direction parity: identical country coverage in both topics (design §8).
	if len(t2cCountrySet) != len(c2tCountrySet) {
		t.Fatalf("direction parity: %d countries under tld-to-country, %d under country-to-tld", len(t2cCountrySet), len(c2tCountrySet))
	}
	for cid := range t2cCountrySet {
		if !c2tCountrySet[cid] {
			t.Fatalf("country %v present under tld-to-country but not country-to-tld", cid)
		}
	}

	// Spot checks on the fact value, including the .uk (not .gb) edge case and
	// the famous .tv / .io.
	spotTLD := map[string]string{"US": ".us", "GB": ".uk", "DE": ".de", "BR": ".br", "TV": ".tv", "IO": ".io"}
	for iso, wantTLD := range spotTLD {
		c, found, err := store.GetCountryByISO(ctx, iso)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", iso, found, err)
		}
		if got := factTLDByCountry[c.ID]; got != wantTLD {
			t.Fatalf("tld fact for %s = %q, want %q", iso, got, wantTLD)
		}
	}
	// Tier rubric sanity: the universally-known set is tier 0 regardless of
	// flags (per-item tiers are already validated against countrytier above).
	if countrytier.Tier("US", true, true) != 0 || countrytier.Tier("GB", true, true) != 0 {
		t.Fatalf("countrytier rubric sanity: US/GB should be tier 0")
	}
}

// assertTopicItems checks a direction-topic's items: count, key = iso2,
// country_id set, payload internally consistent and matching the tld fact, and
// tier = countrytier rubric. Returns the set of country_ids seen (for the
// direction-parity check).
func assertTopicItems(t *testing.T, ctx context.Context, store *storage.Store, topic storage.Topic, want int, countryByID map[uuid.UUID]storage.Country, factTLD map[uuid.UUID]string) map[uuid.UUID]bool {
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
			Name  string `json:"name"`
			ISOA2 string `json:"iso_a2"`
			ISOA3 string `json:"iso_a3"`
			TLD   string `json:"tld"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("%s item %s: unmarshal payload: %v", topic.Slug, it.Key, err)
		}
		if p.ISOA2 != country.ISOA2 || p.Name != country.Name || p.ISOA3 != country.ISOA3 {
			t.Fatalf("%s item %s payload {%s,%s,%s} disagrees with country {%s,%s,%s}",
				topic.Slug, it.Key, p.ISOA2, p.Name, p.ISOA3, country.ISOA2, country.Name, country.ISOA3)
		}
		if p.TLD != factTLD[*it.CountryID] {
			t.Fatalf("%s item %s payload.tld = %q, but fact says %q", topic.Slug, it.Key, p.TLD, factTLD[*it.CountryID])
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
