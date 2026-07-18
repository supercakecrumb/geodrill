package specialchars

// Integration test against a real PostgreSQL 18, gated on
// GEODRILL_TEST_DATABASE_URL (skipped otherwise, so `go test ./...` stays
// green without docker). Copies the testDSN/freshSchema fuse pattern from
// internal/storage/integration_test.go: the target database name must
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
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run specialchars integration tests")
	}
	if name := databaseName(dsn); !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("refusing to run destructive integration tests against database %q: "+
			"GEODRILL_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\" "+
			"(e.g. geodrill_test), never the live database", name)
	}
	return dsn
}

// databaseName extracts the database (path) segment from a postgres DSN,
// tolerating query strings and trailing slashes. Returns "" if none is found.
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

func TestSeed_Integration(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Resolve seeds/special_chars.yaml relative to the repo root: this test
	// file lives at internal/topics/specialchars/, three levels down.
	if err := SeedFromFile(ctx, st, "../../../seeds/special_chars.yaml"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sf, err := LoadSeedFile("../../../seeds/special_chars.yaml")
	if err != nil {
		t.Fatalf("load seed file directly for comparison: %v", err)
	}
	if len(sf.Chars) == 0 {
		t.Fatalf("seed file has no chars — test fixture is broken")
	}

	topic, found, err := st.GetTopicByPath(ctx, "languages/special-characters")
	if err != nil || !found {
		t.Fatalf("get topic by path: found=%v err=%v", found, err)
	}
	if topic.QuizKind != Kind {
		t.Fatalf("quiz_kind = %q, want %q", topic.QuizKind, Kind)
	}
	if topic.BaseTier != 2 {
		t.Fatalf("base_tier = %d, want 2", topic.BaseTier)
	}
	if !topic.IsQuizzable {
		t.Fatalf("expected is_quizzable = true")
	}
	wantModes := []string{"single", "set", "text"}
	if len(topic.ExerciseModes) != len(wantModes) {
		t.Fatalf("exercise_modes = %v, want %v", topic.ExerciseModes, wantModes)
	}
	for i, m := range wantModes {
		if topic.ExerciseModes[i] != m {
			t.Fatalf("exercise_modes = %v, want %v", topic.ExerciseModes, wantModes)
		}
	}

	root, found, err := st.GetTopicByPath(ctx, "languages")
	if err != nil || !found {
		t.Fatalf("get languages root topic: found=%v err=%v", found, err)
	}
	if root.QuizKind != "container" || root.IsQuizzable {
		t.Fatalf("languages root topic should be a non-quizzable container: %+v", root)
	}

	items, err := st.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != len(sf.Chars) {
		t.Fatalf("item count = %d, want %d (len(seed file chars))", len(items), len(sf.Chars))
	}

	byKey := make(map[string]storage.Item, len(items))
	for _, it := range items {
		byKey[it.Key] = it
	}

	// Spot-check a single-language and a subgroup entry's payload/tier.
	spot := sf.Chars[0]
	got, ok := byKey[spot.Char]
	if !ok {
		t.Fatalf("seeded item for %q not found", spot.Char)
	}
	if got.Tier == nil || *got.Tier != spot.Tier {
		t.Fatalf("item %q tier = %v, want %d", spot.Char, got.Tier, spot.Tier)
	}
	if got.Label != spot.Char {
		t.Fatalf("item %q label = %q, want %q", spot.Char, got.Label, spot.Char)
	}
	var gotPayload payload
	if err := json.Unmarshal(got.Payload, &gotPayload); err != nil {
		t.Fatalf("unmarshal seeded payload: %v", err)
	}
	if gotPayload.Char != spot.Char || gotPayload.Script != spot.Script {
		t.Fatalf("payload = %+v, want char=%q script=%q", gotPayload, spot.Char, spot.Script)
	}
	if len(gotPayload.Languages) != len(spot.Languages) {
		t.Fatalf("payload languages = %v, want %v", gotPayload.Languages, spot.Languages)
	}

	// Find a subgroup entry (len(languages) > 1) and spot-check it too.
	var subgroup *SeedChar
	for i := range sf.Chars {
		if len(sf.Chars[i].Languages) > 1 {
			subgroup = &sf.Chars[i]
			break
		}
	}
	if subgroup == nil {
		t.Fatalf("seed file has no subgroup (multi-language) entries — test fixture assumption broken")
	}
	gotSub, ok := byKey[subgroup.Char]
	if !ok {
		t.Fatalf("seeded item for subgroup %q not found", subgroup.Char)
	}
	var gotSubPayload payload
	if err := json.Unmarshal(gotSub.Payload, &gotSubPayload); err != nil {
		t.Fatalf("unmarshal seeded subgroup payload: %v", err)
	}
	if len(gotSubPayload.Languages) != len(subgroup.Languages) {
		t.Fatalf("subgroup payload languages = %v, want %v", gotSubPayload.Languages, subgroup.Languages)
	}

	// Idempotent re-seed: running again must not duplicate items or fail.
	if err := SeedFromFile(ctx, st, "../../../seeds/special_chars.yaml"); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	itemsAgain, err := st.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items after re-seed: %v", err)
	}
	if len(itemsAgain) != len(sf.Chars) {
		t.Fatalf("item count after re-seed = %d, want %d (re-seed must be idempotent)", len(itemsAgain), len(sf.Chars))
	}
}
