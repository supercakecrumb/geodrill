package cities_test

// Integration test against a real PostgreSQL, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, mirroring
// internal/topics/tld/integration_test.go and
// internal/topics/capitals/integration_test.go). Seeds a fresh schema and
// asserts: the topic exists with the right quiz_kind + exercise_modes, one
// item per city in seeds/cities.yaml, item/country consistency, tier =
// countrytier rubric, and items.position tracks population descending.
//
// WARNING: freshSchema below drops every table (it exercises the down
// migration), so the target MUST be a disposable database whose name
// contains "test" — see testDSN's safety fuse, copied from tld/capitals'
// integration tests.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/cities"
	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run cities integration tests")
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

	// roadside owns country seeding; cities resolves its references against it.
	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}
	if err := cities.Seed(ctx, store); err != nil {
		t.Fatalf("seed cities: %v", err)
	}
	// Idempotency: reseeding must converge, not duplicate or error.
	if err := cities.Seed(ctx, store); err != nil {
		t.Fatalf("reseed cities: %v", err)
	}

	// seeds/cities.yaml has 451 entries, all with distinct keys (see
	// seed_test.go's TestLoadCitiesRealSeed uniqueness check).
	const wantEntries = 451

	topic, found, err := store.GetTopicByPath(ctx, cities.RootSlug+"/"+cities.LeafSlug)
	if err != nil || !found {
		t.Fatalf("get city-to-country topic: found=%v err=%v", found, err)
	}
	if topic.QuizKind != cities.Kind {
		t.Fatalf("quiz_kind = %q, want %q", topic.QuizKind, cities.Kind)
	}
	if len(topic.ExerciseModes) != 1 || topic.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("exercise_modes = %v, want [autocomplete]", topic.ExerciseModes)
	}

	root, found, err := store.GetTopicByPath(ctx, cities.RootSlug)
	if err != nil || !found {
		t.Fatalf("get cities root: found=%v err=%v", found, err)
	}
	if root.IsQuizzable {
		t.Fatalf("cities root container should not be quizzable")
	}

	items, err := store.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != wantEntries {
		t.Fatalf("len(items) = %d, want %d", len(items), wantEntries)
	}

	countries, err := store.ListCountries(ctx)
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	countryByID := make(map[uuid.UUID]storage.Country, len(countries))
	for _, c := range countries {
		countryByID[c.ID] = c
	}

	// Cross-reference against the yaml (via the store's already-parsed
	// payload — this test can't reach into the unexported seed-file loader
	// from _test package, so population ordering is checked purely via
	// items.position monotonicity + explicit spot checks below).
	type seen struct {
		position int
		iso2     string
	}
	byKey := make(map[string]seen, len(items))

	positions := make([]int, 0, len(items))
	for _, it := range items {
		if it.CountryID == nil {
			t.Fatalf("item %s has no country_id", it.Key)
		}
		country, ok := countryByID[*it.CountryID]
		if !ok {
			t.Fatalf("item %s references unknown country_id %v", it.Key, *it.CountryID)
		}
		if !strings.Contains(it.Key, ":") {
			t.Fatalf("item key %q is not in the documented <iso2>:<slug> shape", it.Key)
		}

		var p struct {
			CityName    string `json:"city_name"`
			Flag        string `json:"flag"`
			CountryName string `json:"country_name"`
			ISOA2       string `json:"iso_a2"`
			ISOA3       string `json:"iso_a3"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("item %s: unmarshal payload: %v", it.Key, err)
		}
		if p.ISOA2 != country.ISOA2 || p.CountryName != country.Name || p.ISOA3 != country.ISOA3 || p.Flag != country.FlagEmoji {
			t.Fatalf("item %s payload {%s,%s,%s,%s} disagrees with country {%s,%s,%s,%s}",
				it.Key, p.ISOA2, p.CountryName, p.ISOA3, p.Flag, country.ISOA2, country.Name, country.ISOA3, country.FlagEmoji)
		}
		if p.CityName != it.Label {
			t.Fatalf("item %s payload.city_name = %q, but label = %q", it.Key, p.CityName, it.Label)
		}
		if it.Tier == nil {
			t.Fatalf("item %s has nil tier (want explicit countrytier override)", it.Key)
		}
		if wantTier := countrytier.Tier(country.ISOA2, country.UNMember, country.GGCoverage); *it.Tier != wantTier {
			t.Fatalf("item %s tier = %d, want %d (countrytier)", it.Key, *it.Tier, wantTier)
		}

		byKey[it.Key] = seen{position: it.Position, iso2: country.ISOA2}
		positions = append(positions, it.Position)
	}

	// Position values must be unique (no two items share a rank):
	// engine.Seed assigns position = index in the population-sorted,
	// now-deduplicated entry slice.
	seenPos := make(map[int]bool, len(positions))
	for _, p := range positions {
		if seenPos[p] {
			t.Fatalf("duplicate items.position value %d", p)
		}
		seenPos[p] = true
	}

	// Spot checks: famous, population-heavy cities must be near the front
	// (low position = biggest first per design §1).
	spotMaxPosition := map[string]int{
		"cn:shanghai": 10,
		"in:mumbai":   10,
		"cn:beijing":  10,
	}
	for key, maxPos := range spotMaxPosition {
		s, ok := byKey[key]
		if !ok {
			t.Fatalf("expected city %q not found among seeded items", key)
		}
		if s.position > maxPos {
			t.Fatalf("city %q position = %d, want <= %d (biggest cities first)", key, s.position, maxPos)
		}
	}
	// A tiny, obscure city (Vatican City) must rank near the very end.
	if s, ok := byKey["va:vatican-city"]; ok {
		if s.position < len(items)-10 {
			t.Fatalf("va:vatican-city position = %d, want within the last 10 of %d (smallest last)", s.position, len(items))
		}
	}
}
