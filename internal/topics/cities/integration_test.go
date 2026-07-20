package cities_test

// Integration tests against a real PostgreSQL, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, mirroring tld/capitals).
// Covers a fresh seed (topic shape, item count, population ordering) and the
// one-time in-place legacy migration + per-user reset
// (vibe/design-cities-on-map.md §7).
//
// WARNING: freshSchema drops every table (it exercises the down migration),
// so the target MUST be a disposable database whose name contains "test" —
// see testDSN's safety fuse.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/cities"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

// seedCityCount is the number of cities in the committed seeds/cities.yaml at
// the time of the cutover — asserted so an accidental truncation of the seed
// file is caught. Update deliberately when cmd/citygen regenerates the file.
const seedCityCount = 4487

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

	topic, found, err := store.GetTopicByPath(ctx, cities.RootSlug+"/"+cities.LeafSlug)
	if err != nil || !found {
		t.Fatalf("get city-on-map topic: found=%v err=%v", found, err)
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
	if len(items) != seedCityCount {
		t.Fatalf("len(items) = %d, want %d (one per city in seeds/cities.yaml)", len(items), seedCityCount)
	}

	// Idempotency: reseeding must converge to the exact same item count.
	if err := cities.Seed(ctx, store); err != nil {
		t.Fatalf("reseed cities: %v", err)
	}
	reseededItems, err := store.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items after reseed: %v", err)
	}
	if len(reseededItems) != len(items) {
		t.Fatalf("len(items) after reseed = %d, want %d (unchanged)", len(reseededItems), len(items))
	}

	countries, err := store.ListCountries(ctx)
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	countryByID := make(map[uuid.UUID]storage.Country, len(countries))
	for _, c := range countries {
		countryByID[c.ID] = c
	}

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
			Key         string `json:"key"`
			CityName    string `json:"city_name"`
			Flag        string `json:"flag"`
			CountryName string `json:"country_name"`
			ISOA2       string `json:"iso_a2"`
			ISOA3       string `json:"iso_a3"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("item %s: unmarshal payload: %v", it.Key, err)
		}
		if p.Key != it.Key {
			t.Fatalf("item %s payload.key = %q, want the item key echoed", it.Key, p.Key)
		}
		if p.ISOA2 != country.ISOA2 || p.CountryName != country.Name || p.ISOA3 != country.ISOA3 || p.Flag != country.FlagEmoji {
			t.Fatalf("item %s payload {%s,%s,%s,%s} disagrees with country {%s,%s,%s,%s}",
				it.Key, p.ISOA2, p.CountryName, p.ISOA3, p.Flag, country.ISOA2, country.Name, country.ISOA3, country.FlagEmoji)
		}
		if p.CityName != it.Label {
			t.Fatalf("item %s payload.city_name = %q, but label = %q", it.Key, p.CityName, it.Label)
		}
		if it.Tier == nil || *it.Tier < 0 || *it.Tier > 6 {
			t.Fatalf("item %s tier = %v, want an explicit per-city tier in [0,6]", it.Key, it.Tier)
		}

		byKey[it.Key] = seen{position: it.Position, iso2: country.ISOA2}
		positions = append(positions, it.Position)
	}

	seenPos := make(map[int]bool, len(positions))
	for _, p := range positions {
		if seenPos[p] {
			t.Fatalf("duplicate items.position value %d", p)
		}
		seenPos[p] = true
	}

	// Famous, population-heavy cities must be near the front (biggest first).
	for key, maxPos := range map[string]int{"cn:shanghai": 10, "in:mumbai": 10, "cn:beijing": 10} {
		s, ok := byKey[key]
		if !ok {
			t.Fatalf("expected city %q not found among seeded items", key)
		}
		if s.position > maxPos {
			t.Fatalf("city %q position = %d, want <= %d (biggest cities first)", key, s.position, maxPos)
		}
	}
	if s, ok := byKey["va:vatican-city"]; ok && s.position < len(items)-10 {
		t.Fatalf("va:vatican-city position = %d, want within the last 10 of %d (smallest last)", s.position, len(items))
	}
}

// TestLegacyMigration exercises the one-time in-place cutover: an old-shape
// cities/city-to-country topic with a user's progress + exercise/review
// history is renamed to cities/city-on-map, the open exercise + user_items are
// reset, and the answered exercise + review archive survive. A second seed is
// an idempotent no-op.
func TestLegacyMigration(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}
	de, found, err := store.GetCountryByISO(ctx, "DE")
	if err != nil || !found {
		t.Fatalf("get country DE: found=%v err=%v", found, err)
	}

	// Build the OLD-shape topic (cities root + city-to-country leaf) + an item.
	rootTopic, err := store.UpsertTopic(ctx, nil, cities.RootSlug, "Cities", 3, 0, "", []string{"single"}, false, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert legacy root: %v", err)
	}
	legacyLeaf, err := store.UpsertTopic(ctx, &rootTopic.ID, "city-to-country", "City → Country", 0, 0, "city_to_country", []string{"autocomplete"}, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert legacy leaf: %v", err)
	}
	tier := int16(2)
	item, err := store.UpsertItem(ctx, legacyLeaf.ID, "de:munich", "Munich", &tier,
		[]byte(`{"city_name":"Munich","flag":"🇩🇪","country_name":"Germany","iso_a2":"DE","iso_a3":"DEU"}`),
		&de.ID, 0, true)
	if err != nil {
		t.Fatalf("upsert legacy item: %v", err)
	}

	// A user with progress on that item.
	user, err := store.UpsertUser(ctx, 424242, "legacy_tester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	now := time.Now().UTC()
	if err := store.PutUserItem(ctx, user.ID, item.ID, 1, storage.CardFields{Due: now}, now, time.Time{}); err != nil {
		t.Fatalf("put user_item: %v", err)
	}

	// One OPEN exercise (must be deleted) and one ANSWERED exercise + review
	// (must survive as archive).
	openID, _, err := store.InsertExercise(ctx, storage.InsertExerciseParams{
		UserID: user.ID, ItemID: item.ID, Mode: 2, Prompt: "open", Options: []byte(`[]`), CorrectAnswer: "DE",
	})
	if err != nil {
		t.Fatalf("insert open exercise: %v", err)
	}
	answeredID, _, err := store.InsertExercise(ctx, storage.InsertExerciseParams{
		UserID: user.ID, ItemID: item.ID, Mode: 2, Prompt: "answered", Options: []byte(`[]`), CorrectAnswer: "DE",
	})
	if err != nil {
		t.Fatalf("insert answered exercise: %v", err)
	}
	if ok, err := store.MarkExerciseAnswered(ctx, answeredID, now); err != nil || !ok {
		t.Fatalf("mark answered: ok=%v err=%v", ok, err)
	}
	if err := store.InsertReview(ctx, storage.ReviewInsert{
		UserID: user.ID, ItemID: item.ID, ExerciseID: &answeredID, Mode: 2,
		Chosen: "Germany", CorrectAnswer: "Germany", Correct: true, Rating: 3, ReviewedAt: now,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	// Run the cutover.
	if err := cities.Seed(ctx, store); err != nil {
		t.Fatalf("seed cities (migration): %v", err)
	}

	// The leaf topic is renamed in place: same UUID, new slug.
	newTopic, found, err := store.GetTopicByPath(ctx, cities.RootSlug+"/"+cities.LeafSlug)
	if err != nil || !found {
		t.Fatalf("get city-on-map after migration: found=%v err=%v", found, err)
	}
	if newTopic.ID != legacyLeaf.ID {
		t.Fatalf("topic UUID changed: %v -> %v (rename should preserve id)", legacyLeaf.ID, newTopic.ID)
	}
	if newTopic.Slug != cities.LeafSlug {
		t.Fatalf("slug = %q, want %q", newTopic.Slug, cities.LeafSlug)
	}
	if _, found, _ := store.GetTopicByPath(ctx, cities.RootSlug+"/city-to-country"); found {
		t.Fatalf("legacy path cities/city-to-country should no longer resolve")
	}

	// user_items reset.
	if _, found, err := store.GetUserItem(ctx, user.ID, item.ID); err != nil || found {
		t.Fatalf("user_item should be deleted: found=%v err=%v", found, err)
	}
	// Open exercise gone; answered exercise survives.
	if _, found, err := store.GetExerciseByID(ctx, openID); err != nil || found {
		t.Fatalf("open exercise should be deleted: found=%v err=%v", found, err)
	}
	if _, found, err := store.GetExerciseByID(ctx, answeredID); err != nil || !found {
		t.Fatalf("answered exercise should survive: found=%v err=%v", found, err)
	}
	// Review archive survives.
	reviews, err := store.GetReviewsByItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("get reviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("reviews after migration = %d, want 1 (archive kept)", len(reviews))
	}

	// Second seed: idempotent no-op (legacy path already gone, topic stays).
	if err := cities.Seed(ctx, store); err != nil {
		t.Fatalf("second seed should be a no-op, got %v", err)
	}
	again, found, err := store.GetTopicByPath(ctx, cities.RootSlug+"/"+cities.LeafSlug)
	if err != nil || !found {
		t.Fatalf("topic missing after second seed: found=%v err=%v", found, err)
	}
	if again.ID != legacyLeaf.ID {
		t.Fatalf("topic UUID changed on second seed: %v -> %v", legacyLeaf.ID, again.ID)
	}
}
