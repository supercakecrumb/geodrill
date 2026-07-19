package guesslang

// Integration test against a real PostgreSQL 18, mirroring the safety fuse
// used across every other topic package's seed test (testDSN's guard,
// freshSchema's up->down->up harness):
//
//	GEODRILL_TEST_DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill_test?sslmode=disable' \
//	  go test ./internal/topics/guesslang/...

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/supercakecrumb/geodrill/internal/content"
	"github.com/supercakecrumb/geodrill/internal/storage"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run guesslang integration tests")
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

	sf, err := content.LoadSeeds(seedsPath())
	if err != nil {
		t.Fatalf("load seeds/decks.yaml directly: %v", err)
	}
	if len(sf.Decks) == 0 {
		t.Fatal("seeds/decks.yaml has no decks")
	}

	if err := Seed(ctx, st); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// Topic tree: "languages" (container) -> "languages/guess-the-language"
	// (container) -> one quiz-bearing child per deck.
	root, found, err := st.GetTopicByPath(ctx, RootSlug)
	if err != nil || !found {
		t.Fatalf("get root topic %q: found=%v err=%v", RootSlug, found, err)
	}
	if root.QuizKind != "container" || root.IsQuizzable {
		t.Fatalf("root topic should be a non-quizzable container: %+v", root)
	}

	containerPath := RootSlug + "/" + ContainerSlug
	container, found, err := st.GetTopicByPath(ctx, containerPath)
	if err != nil || !found {
		t.Fatalf("get container topic %q: found=%v err=%v", containerPath, found, err)
	}
	if container.QuizKind != "container" || container.IsQuizzable {
		t.Fatalf("guess-the-language topic should be a non-quizzable container: %+v", container)
	}

	children, err := st.ListChildTopics(ctx, container.ID)
	if err != nil {
		t.Fatalf("list children of %q: %v", containerPath, err)
	}
	if len(children) != len(sf.Decks) {
		t.Fatalf("child group topic count = %d, want %d (decks.yaml entries)", len(children), len(sf.Decks))
	}

	totalLanguages := 0
	for _, d := range sf.Decks {
		groupPath := containerPath + "/" + d.Slug
		group, found, err := st.GetTopicByPath(ctx, groupPath)
		if err != nil || !found {
			t.Fatalf("get group topic %q: found=%v err=%v", groupPath, found, err)
		}
		if group.QuizKind != Kind {
			t.Fatalf("group topic %q quiz_kind = %q, want %q", d.Slug, group.QuizKind, Kind)
		}
		if group.BaseTier != BaseTier {
			t.Fatalf("group topic %q base_tier = %d, want %d", d.Slug, group.BaseTier, BaseTier)
		}
		if group.IsQuizzable {
			t.Fatalf("group topic %q should be is_quizzable=false (guess-the-language moved into the game zone)", d.Slug)
		}
		if len(group.ExerciseModes) != 1 || group.ExerciseModes[0] != "single" {
			t.Fatalf("group topic %q exercise_modes = %v, want [single]", d.Slug, group.ExerciseModes)
		}

		items, err := st.ListItemsByTopic(ctx, group.ID)
		if err != nil {
			t.Fatalf("list items for %q: %v", groupPath, err)
		}
		if len(items) != len(d.Languages) {
			t.Fatalf("group topic %q item count = %d, want %d (decks.yaml languages)", d.Slug, len(items), len(d.Languages))
		}
		totalLanguages += len(items)

		for _, it := range items {
			wantLabel, ok := d.Languages[it.Key]
			if !ok {
				t.Fatalf("group topic %q has unexpected item key %q", d.Slug, it.Key)
			}
			if it.Label != wantLabel {
				t.Fatalf("item %s/%s label = %q, want %q", d.Slug, it.Key, it.Label, wantLabel)
			}
			if it.Tier != nil {
				t.Fatalf("item %s/%s tier = %v, want nil (inherits group base_tier)", d.Slug, it.Key, *it.Tier)
			}
			var p itemPayload
			if err := json.Unmarshal(it.Payload, &p); err != nil {
				t.Fatalf("item %s/%s payload not valid JSON: %v", d.Slug, it.Key, err)
			}
			if p.Language != it.Key {
				t.Fatalf("item %s/%s payload.language = %q, want %q", d.Slug, it.Key, p.Language, it.Key)
			}
		}
	}
	t.Logf("seeded %d group topics, %d language items total", len(sf.Decks), totalLanguages)

	// Idempotency: re-seeding must not duplicate rows.
	if err := Seed(ctx, st); err != nil {
		t.Fatalf("Seed (second run): %v", err)
	}
	childrenAgain, err := st.ListChildTopics(ctx, container.ID)
	if err != nil {
		t.Fatalf("list children after re-seed: %v", err)
	}
	if len(childrenAgain) != len(children) {
		t.Fatalf("child topic count after re-seed = %d, want %d (Seed must be idempotent)", len(childrenAgain), len(children))
	}
}
