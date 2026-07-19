package engine

// Integration test against a real PostgreSQL, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise), mirroring the safety fuse
// used by every topic package's seed test: the target database name must
// contain "test" — freshSchema drops and re-applies every migration, so this
// must never point at a live database.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run engine integration tests")
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

func TestSeedIntegration(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	d := Descriptor{
		QuizKind: "engine_it",
		Topic: []TopicNode{
			{Slug: "engine-it-root", Name: "Engine IT"},
			{Slug: "engine-it-topic", Name: "Engine IT topic", BaseTier: 2, QuizKind: "engine_it", ExerciseModes: []string{"single", "text"}, IsQuizzable: true},
		},
	}
	countrySeeds := []CountrySeed{
		{ISOA2: "GB", ISOA3: "GBR", Name: "United Kingdom", FlagEmoji: "🇬🇧", UNMember: true, GGCoverage: true},
		{ISOA2: "GB-ENG", Name: "England", Parent: "GB", Subdivision: true, GGCoverage: true},
	}
	side := "left"
	def := FactDef{Key: "engine_it_fact", Label: "Engine IT fact", ValueType: "text", Cardinality: "single", Dataset: "test"}
	tier3 := int16(3)
	type payload struct {
		X string `json:"x"`
	}

	seedAll := func() {
		byISO, err := SeedCountries(ctx, st, countrySeeds)
		if err != nil {
			t.Fatalf("SeedCountries: %v", err)
		}
		if err := SeedCountryFacts(ctx, st, def, []FactValue{{CountryISO: "GB", Text: &side, Source: "test"}}, byISO); err != nil {
			t.Fatalf("SeedCountryFacts: %v", err)
		}
		data := SeedData{
			Items: []ItemSeed{
				{Key: "GB", Label: "United Kingdom", Payload: payload{X: "gb"}, CountryISO: "GB"},
				{Key: "plain", Label: "Plain", Tier: &tier3, Payload: payload{X: "p"}},
			},
			Countries:       byISO,
			TierFromCountry: true,
		}
		if err := Seed(ctx, st, d, data); err != nil {
			t.Fatalf("Seed: %v", err)
		}
	}

	seedAll()
	seedAll() // idempotency: re-seeding must converge, not duplicate

	topic, found, err := st.GetTopicByPath(ctx, "engine-it-root/engine-it-topic")
	if err != nil || !found {
		t.Fatalf("get topic by path: found=%v err=%v", found, err)
	}
	if topic.QuizKind != "engine_it" || topic.BaseTier != 2 || !topic.IsQuizzable {
		t.Fatalf("leaf topic = %+v", topic)
	}
	if len(topic.ExerciseModes) != 2 || topic.ExerciseModes[0] != "single" || topic.ExerciseModes[1] != "text" {
		t.Fatalf("exercise_modes = %v", topic.ExerciseModes)
	}
	root, found, err := st.GetTopicByPath(ctx, "engine-it-root")
	if err != nil || !found {
		t.Fatalf("get root: found=%v err=%v", found, err)
	}
	if root.QuizKind != "container" || root.IsQuizzable {
		t.Fatalf("root should default to a non-quizzable container: %+v", root)
	}

	items, err := st.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2 (re-seed must not duplicate)", len(items))
	}
	byKey := map[string]storage.Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}

	gb, found, err := st.GetCountryByISO(ctx, "GB")
	if err != nil || !found {
		t.Fatalf("get GB: found=%v err=%v", found, err)
	}
	gbItem := byKey["GB"]
	if gbItem.CountryID == nil || *gbItem.CountryID != gb.ID {
		t.Fatalf("GB item country_id = %v, want %v", gbItem.CountryID, gb.ID)
	}
	if gbItem.Tier == nil || *gbItem.Tier != 0 {
		t.Fatalf("GB item tier = %v, want 0 (countrytier rubric)", gbItem.Tier)
	}
	var p payload
	if err := json.Unmarshal(gbItem.Payload, &p); err != nil || p.X != "gb" {
		t.Fatalf("GB payload = %s err=%v", gbItem.Payload, err)
	}
	plain := byKey["plain"]
	if plain.Tier == nil || *plain.Tier != 3 || plain.CountryID != nil {
		t.Fatalf("plain item = %+v, want explicit tier 3 and no country", plain)
	}

	// Subdivision parent link survived the two-pass upsert.
	eng, found, err := st.GetCountryByISO(ctx, "GB-ENG")
	if err != nil || !found {
		t.Fatalf("get GB-ENG: found=%v err=%v", found, err)
	}
	if eng.ParentCountryID == nil || *eng.ParentCountryID != gb.ID || !eng.IsSubdivision {
		t.Fatalf("GB-ENG = %+v, want parented to GB", eng)
	}

	// Exactly one fact row for GB after the double seed (delete-then-insert
	// idempotency).
	facts, err := st.ListCountryFactsByDefKey(ctx, def.Key)
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("fact rows = %d, want exactly 1", len(facts))
	}
	if facts[0].CountryID != gb.ID || facts[0].ValText == nil || *facts[0].ValText != "left" {
		t.Fatalf("fact = %+v", facts[0])
	}
}
