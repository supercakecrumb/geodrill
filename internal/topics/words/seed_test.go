package words

// Integration test against a real PostgreSQL 18, mirroring the safety fuse in
// internal/storage/integration_test.go: skipped unless
// GEODRILL_TEST_DATABASE_URL is set, and refuses to run unless the target
// database name contains "test" (freshSchema drops every table via the down
// migrations).
//
//	GEODRILL_TEST_DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill_test?sslmode=disable' \
//	  go test ./internal/topics/words/...

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
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run words integration tests")
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

	sf, err := loadSeedFile(seedFilePath())
	if err != nil {
		t.Fatalf("load seed file directly: %v", err)
	}
	if len(sf.Words) == 0 {
		t.Fatalf("seeds/common_words.yaml has no words entries")
	}

	if err := Seed(ctx, st); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// Topic tree: "languages" (container) -> "languages/common-words" (quiz).
	root, found, err := st.GetTopicByPath(ctx, RootSlug)
	if err != nil || !found {
		t.Fatalf("get root topic %q: found=%v err=%v", RootSlug, found, err)
	}
	if root.QuizKind != "container" || root.IsQuizzable {
		t.Fatalf("root topic should be a non-quizzable container: %+v", root)
	}

	topic, found, err := st.GetTopicByPath(ctx, RootSlug+"/"+TopicSlug)
	if err != nil || !found {
		t.Fatalf("get topic %q: found=%v err=%v", RootSlug+"/"+TopicSlug, found, err)
	}
	if topic.QuizKind != QuizKind {
		t.Fatalf("topic quiz_kind = %q, want %q", topic.QuizKind, QuizKind)
	}
	if topic.BaseTier != BaseTier {
		t.Fatalf("topic base_tier = %d, want %d", topic.BaseTier, BaseTier)
	}
	if !topic.IsQuizzable {
		t.Fatalf("topic should be quizzable")
	}
	if len(topic.ExerciseModes) != 1 || topic.ExerciseModes[0] != "single" {
		t.Fatalf("topic exercise_modes = %v, want [single]", topic.ExerciseModes)
	}

	// Item count == yaml entry count, and every key is unique.
	items, err := st.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != len(sf.Words) {
		t.Fatalf("item count = %d, want %d (yaml entries)", len(items), len(sf.Words))
	}

	byKey := make(map[string]storage.Item, len(items))
	for _, it := range items {
		if _, dup := byKey[it.Key]; dup {
			t.Fatalf("duplicate item key %q — keys must be unique per topic", it.Key)
		}
		byKey[it.Key] = it
	}

	// Spot-check every entry's round trip: label, tier, and payload fields.
	for _, w := range sf.Words {
		key := w.Language + ":" + w.Word
		it, ok := byKey[key]
		if !ok {
			t.Fatalf("yaml entry %q has no matching item (expected key %q)", w.Word, key)
		}
		if it.Label != w.Word {
			t.Fatalf("item %q label = %q, want %q", key, it.Label, w.Word)
		}
		if it.Tier == nil || *it.Tier != w.Tier {
			t.Fatalf("item %q tier = %v, want %d", key, it.Tier, w.Tier)
		}
		var p itemPayload
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("item %q payload not valid JSON: %v", key, err)
		}
		if p.Word != w.Word || p.Language != w.Language || p.Meaning != w.Meaning {
			t.Fatalf("item %q payload = %+v, want {%s %s %s}", key, p, w.Word, w.Language, w.Meaning)
		}
	}

	// Idempotency: re-seeding must not duplicate rows.
	if err := Seed(ctx, st); err != nil {
		t.Fatalf("Seed (second run): %v", err)
	}
	itemsAgain, err := st.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items after re-seed: %v", err)
	}
	if len(itemsAgain) != len(items) {
		t.Fatalf("item count after re-seed = %d, want %d (Seed must be idempotent)", len(itemsAgain), len(items))
	}
}
