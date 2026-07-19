package flags_test

// Integration test against a real PostgreSQL, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, mirroring
// internal/topics/tld/integration_test.go and
// internal/topics/cities/integration_test.go). Seeds a fresh schema and
// asserts: the topic exists with the right quiz_kind + exercise_modes, item
// counts match seeds/flags.yaml (228 singles + 10 groups = 238 items), every
// single item's country_id/payload/tier are internally consistent, every
// group item has country_id == nil and a tier equal to the MAX of its
// members' tiers, and no country is double-counted across shapes.
//
// WARNING: freshSchema below drops every table (it exercises the down
// migration), so the target MUST be a disposable database whose name
// contains "test" — see testDSN's safety fuse, copied from tld/cities'
// integration tests.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
	"github.com/supercakecrumb/geodrill/internal/topics/flags"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run flags integration tests")
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

	// roadside owns country seeding; flags resolves its references against it.
	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}
	if err := flags.Seed(ctx, store); err != nil {
		t.Fatalf("seed flags: %v", err)
	}
	// Idempotency: reseeding must converge, not duplicate or error.
	if err := flags.Seed(ctx, store); err != nil {
		t.Fatalf("reseed flags: %v", err)
	}

	const (
		wantSingles = 228
		wantGroups  = 10
		wantItems   = wantSingles + wantGroups
	)

	topic, found, err := store.GetTopicByPath(ctx, flags.RootSlug+"/"+flags.LeafSlug)
	if err != nil || !found {
		t.Fatalf("get flags/guess-the-flag topic: found=%v err=%v", found, err)
	}
	if topic.QuizKind != flags.Kind {
		t.Fatalf("quiz_kind = %q, want %q", topic.QuizKind, flags.Kind)
	}
	wantModes := map[string]bool{"autocomplete": true, "set": true}
	if len(topic.ExerciseModes) != len(wantModes) {
		t.Fatalf("exercise_modes = %v, want %v", topic.ExerciseModes, wantModes)
	}
	for _, m := range topic.ExerciseModes {
		if !wantModes[m] {
			t.Fatalf("unexpected exercise mode %q in %v", m, topic.ExerciseModes)
		}
	}

	root, found, err := store.GetTopicByPath(ctx, flags.RootSlug)
	if err != nil || !found {
		t.Fatalf("get flags root: found=%v err=%v", found, err)
	}
	if root.IsQuizzable {
		t.Fatalf("flags root container should not be quizzable")
	}

	items, err := store.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != wantItems {
		t.Fatalf("len(items) = %d, want %d", len(items), wantItems)
	}

	countries, err := store.ListCountries(ctx)
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	countryByID := make(map[uuid.UUID]storage.Country, len(countries))
	for _, c := range countries {
		countryByID[c.ID] = c
	}

	var singleCount, groupCount int
	coveredISO := make(map[string]bool)
	for _, it := range items {
		var p struct {
			FlagEmoji     string   `json:"flag_emoji"`
			Image         string   `json:"image"`
			Name          string   `json:"name"`
			ISOA2         string   `json:"iso_a2"`
			ISOA3         string   `json:"iso_a3"`
			IsSubdivision bool     `json:"is_subdivision"`
			Countries     []string `json:"countries"`
			Images        []string `json:"images"`
			Label         string   `json:"label"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("item %s: unmarshal payload: %v", it.Key, err)
		}

		if len(p.Countries) > 0 {
			// Confusable-group item: no single country_id.
			groupCount++
			if it.CountryID != nil {
				t.Fatalf("group item %s has a country_id, want nil (design §3)", it.Key)
			}
			if len(p.Images) != len(p.Countries) {
				t.Fatalf("group item %s: %d images for %d countries", it.Key, len(p.Images), len(p.Countries))
			}
			if it.Tier == nil {
				t.Fatalf("group item %s has nil tier", it.Key)
			}
			var maxTier int16
			for _, iso := range p.Countries {
				if coveredISO[iso] {
					t.Fatalf("country %q covered by more than one flags item", iso)
				}
				coveredISO[iso] = true
				c, found := lookupISO(countryByID, iso)
				if !found {
					t.Fatalf("group item %s references unknown country %q", it.Key, iso)
				}
				tier := countrytier.Tier(c.ISOA2, c.UNMember, c.GGCoverage)
				if c.IsSubdivision {
					tier = 3
				}
				if tier > maxTier {
					maxTier = tier
				}
			}
			if *it.Tier != maxTier {
				t.Fatalf("group item %s tier = %d, want %d (max of members)", it.Key, *it.Tier, maxTier)
			}
			continue
		}

		// Single item.
		singleCount++
		if it.CountryID == nil {
			t.Fatalf("single item %s has no country_id", it.Key)
		}
		country, ok := countryByID[*it.CountryID]
		if !ok {
			t.Fatalf("single item %s references unknown country_id %v", it.Key, *it.CountryID)
		}
		if it.Key != country.ISOA2 {
			t.Fatalf("single item key = %q, want country iso_a2 %q", it.Key, country.ISOA2)
		}
		if p.ISOA2 != country.ISOA2 || p.Name != country.Name || p.ISOA3 != country.ISOA3 || p.FlagEmoji != country.FlagEmoji {
			t.Fatalf("item %s payload disagrees with country %+v: payload=%+v", it.Key, country, p)
		}
		if p.IsSubdivision != country.IsSubdivision {
			t.Fatalf("item %s payload.is_subdivision = %v, want %v", it.Key, p.IsSubdivision, country.IsSubdivision)
		}
		if coveredISO[country.ISOA2] {
			t.Fatalf("country %q covered by more than one flags item", country.ISOA2)
		}
		coveredISO[country.ISOA2] = true

		if it.Tier == nil {
			t.Fatalf("item %s has nil tier (want explicit countrytier override)", it.Key)
		}
		wantTier := countrytier.Tier(country.ISOA2, country.UNMember, country.GGCoverage)
		if country.IsSubdivision {
			wantTier = 3
		}
		if *it.Tier != wantTier {
			t.Fatalf("item %s tier = %d, want %d", it.Key, *it.Tier, wantTier)
		}
	}

	if singleCount != wantSingles {
		t.Fatalf("single items = %d, want %d", singleCount, wantSingles)
	}
	if groupCount != wantGroups {
		t.Fatalf("group items = %d, want %d", groupCount, wantGroups)
	}
	if len(coveredISO) != len(countries) {
		t.Fatalf("covered countries = %d, want %d (every country.yaml row exactly once)", len(coveredISO), len(countries))
	}

	// Spot check: GB subdivisions are ordinary single items, tier 3.
	for _, code := range []string{"GB-ENG", "GB-SCT", "GB-WLS"} {
		c, found, err := store.GetCountryByISO(ctx, code)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", code, found, err)
		}
		item, found, err := store.GetItemByID(ctx, itemIDForCountry(items, c.ID))
		if err != nil || !found {
			t.Fatalf("get item for %s: found=%v err=%v", code, found, err)
		}
		if item.Tier == nil || *item.Tier != 3 {
			t.Fatalf("subdivision %s tier = %v, want 3", code, item.Tier)
		}
	}
}

func lookupISO(countryByID map[uuid.UUID]storage.Country, iso string) (storage.Country, bool) {
	for _, c := range countryByID {
		if c.ISOA2 == iso {
			return c, true
		}
	}
	return storage.Country{}, false
}

func itemIDForCountry(items []storage.Item, countryID uuid.UUID) uuid.UUID {
	for _, it := range items {
		if it.CountryID != nil && *it.CountryID == countryID {
			return it.ID
		}
	}
	return uuid.Nil
}
