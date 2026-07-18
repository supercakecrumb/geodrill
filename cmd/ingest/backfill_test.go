package main

// Integration test against a real PostgreSQL 18, mirroring the safety fuse
// used across every other package's integration test (testDSN's guard,
// freshSchema's up->down->up harness):
//
//	GEODRILL_TEST_DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill_test?sslmode=disable' \
//	  go test ./cmd/ingest/...

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/content"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/guesslang"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run cmd/ingest integration tests")
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

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBackfillV2Integration exercises the full -backfill-v2 pipeline on a
// fresh schema: legacy decks/skills/user_skills/exercises/reviews inserted
// via the existing legacy Store methods (mirroring how the real app writes
// them), v2 topics/items seeded independently via guesslang.Seed (mirroring
// -seed-topics), then runBackfillV2 maps one onto the other. Asserts the
// mapping is correct and that a second run changes nothing (idempotency).
func TestBackfillV2Integration(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	logger := silentLogger()

	// Legacy seed: decks + skills, exactly as the normal ingest flow does.
	seeds, err := content.LoadSeeds("../../seeds/decks.yaml")
	if err != nil {
		t.Fatalf("load seeds/decks.yaml: %v", err)
	}
	if err := seedDecksAndSkills(ctx, logger, store, seeds); err != nil {
		t.Fatalf("seed decks/skills: %v", err)
	}

	// v2 seed: topics + items, independently derived from the same
	// decks.yaml (mirrors -seed-topics).
	if err := guesslang.Seed(ctx, store); err != nil {
		t.Fatalf("guesslang.Seed: %v", err)
	}

	// Fixture: one user with two legacy skill cards in different FSRS
	// states (one graduated/Review, one fresh/New) plus an exercise+review
	// on the graduated one — via the existing legacy Store methods, the
	// same write path the live bot uses.
	user, err := store.UpsertUser(ctx, 555000111, "aurora")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	romanceDeck, found, err := store.GetDeckBySlug(ctx, "romance")
	if err != nil || !found {
		t.Fatalf("get deck romance: found=%v err=%v", found, err)
	}
	skills, err := store.ListSkillsByDeck(ctx, romanceDeck.ID)
	if err != nil {
		t.Fatalf("list skills for romance: %v", err)
	}
	var spa, por storage.Skill
	for _, sk := range skills {
		switch sk.Key {
		case "spa":
			spa = sk
		case "por":
			por = sk
		}
	}
	if spa.ID == uuid.Nil || por.ID == uuid.Nil {
		t.Fatalf("expected romance deck to contain spa and por skills, got %+v", skills)
	}

	now := time.Now().UTC().Truncate(time.Second)
	lastReview := now.Add(-24 * time.Hour)

	// spa: graduated (State=Review=2) -> lifecycle should become reviewing(2).
	spaCard := storage.CardFields{Due: now.Add(48 * time.Hour), Stability: 20.5, Difficulty: 5.1, Reps: 4, Lapses: 0, State: 2, LastReview: lastReview}
	if err := store.PutCard(ctx, user.ID, spa.ID, spaCard); err != nil {
		t.Fatalf("put card spa: %v", err)
	}

	// por: fresh (State=New=0, never reviewed) -> lifecycle should become introduced(1).
	porCard := storage.CardFields{Due: now, Stability: 0, Difficulty: 0, Reps: 0, Lapses: 0, State: 0, LastReview: time.Time{}}
	if err := store.PutCard(ctx, user.ID, por.ID, porCard); err != nil {
		t.Fatalf("put card por: %v", err)
	}

	if err := store.InsertContent(ctx, "sentence", "spa", "El coche es rojo.", "test", 16); err != nil {
		t.Fatalf("insert content: %v", err)
	}
	contentRow, found, err := store.SampleContentAny(ctx, "spa")
	if err != nil || !found {
		t.Fatalf("sample content spa: found=%v err=%v", found, err)
	}

	optionsJSON := []byte(`[{"key":"spa","label":"Spanish"},{"key":"por","label":"Portuguese"}]`)
	exerciseID, err := store.InsertExercise(ctx, user.ID, spa.ID, contentRow.ID, optionsJSON)
	if err != nil {
		t.Fatalf("insert exercise: %v", err)
	}
	if _, err := store.MarkExerciseAnswered(ctx, exerciseID, now); err != nil {
		t.Fatalf("mark exercise answered: %v", err)
	}

	if err := store.InsertReview(ctx, storage.ReviewInsert{
		UserID: user.ID, SkillID: spa.ID, ExerciseID: &exerciseID, ContentID: &contentRow.ID,
		ChosenKey: "spa", CorrectKey: "spa", Correct: true, Rating: 3,
		StabilityBefore: 0, DifficultyBefore: 0, StabilityAfter: 20.5, DifficultyAfter: 5.1,
		StateBefore: 0, ScheduledDays: 2, ElapsedDays: 0, ReviewedAt: lastReview,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	// Run the backfill.
	res, err := runBackfillV2(ctx, logger, store)
	if err != nil {
		t.Fatalf("runBackfillV2: %v", err)
	}
	if res.SkillsMapped == 0 {
		t.Fatal("SkillsMapped = 0, want every legacy skill mapped to an item")
	}
	if res.UserItemsMigrated != 2 {
		t.Fatalf("UserItemsMigrated = %d, want 2", res.UserItemsMigrated)
	}
	if res.UserItemsSkippedUnmapped != 0 {
		t.Fatalf("UserItemsSkippedUnmapped = %d, want 0", res.UserItemsSkippedUnmapped)
	}
	if res.IntroductionsSynthesized != 2 {
		t.Fatalf("IntroductionsSynthesized = %d, want 2", res.IntroductionsSynthesized)
	}
	if res.ExercisesRowsUpdated != 1 {
		t.Fatalf("ExercisesRowsUpdated = %d, want 1", res.ExercisesRowsUpdated)
	}
	if res.ReviewsRowsUpdated != 1 {
		t.Fatalf("ReviewsRowsUpdated = %d, want 1", res.ReviewsRowsUpdated)
	}

	groupTopic, found, err := store.GetTopicByPath(ctx, "languages/guess-the-language/romance")
	if err != nil || !found {
		t.Fatalf("get topic languages/guess-the-language/romance: found=%v err=%v", found, err)
	}
	items, err := store.ListItemsByTopic(ctx, groupTopic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	var spaItemID, porItemID uuid.UUID
	for _, it := range items {
		switch it.Key {
		case "spa":
			spaItemID = it.ID
		case "por":
			porItemID = it.ID
		}
	}
	if spaItemID == uuid.Nil || porItemID == uuid.Nil {
		t.Fatalf("expected romance group topic to contain spa and por items, got %+v", items)
	}

	// user_items: lifecycle + FSRS columns.
	spaUI, found, err := store.GetUserItem(ctx, user.ID, spaItemID)
	if err != nil || !found {
		t.Fatalf("get user_item spa: found=%v err=%v", found, err)
	}
	if spaUI.Lifecycle != 2 {
		t.Fatalf("spa user_item lifecycle = %d, want 2 (reviewing)", spaUI.Lifecycle)
	}
	if spaUI.Card.Stability != 20.5 || spaUI.Card.Reps != 4 {
		t.Fatalf("spa user_item card = %+v, want stability=20.5 reps=4 (copied 1:1 from user_skills)", spaUI.Card)
	}
	if !spaUI.IntroducedAt.Equal(lastReview) {
		t.Fatalf("spa introduced_at = %v, want %v (COALESCE(last_review, now))", spaUI.IntroducedAt, lastReview)
	}

	porUI, found, err := store.GetUserItem(ctx, user.ID, porItemID)
	if err != nil || !found {
		t.Fatalf("get user_item por: found=%v err=%v", found, err)
	}
	if porUI.Lifecycle != 1 {
		t.Fatalf("por user_item lifecycle = %d, want 1 (introduced)", porUI.Lifecycle)
	}
	if porUI.IntroducedAt.IsZero() {
		t.Fatal("por introduced_at is zero, want COALESCE(last_review, now) = now (last_review was never set)")
	}

	// introductions: synthesized first-exposure row per migrated user_item.
	hasSpaIntro, err := store.HasIntroductionForItem(ctx, user.ID, spaItemID)
	if err != nil || !hasSpaIntro {
		t.Fatalf("expected a synthesized introduction for spa: found=%v err=%v", hasSpaIntro, err)
	}
	hasPorIntro, err := store.HasIntroductionForItem(ctx, user.ID, porItemID)
	if err != nil || !hasPorIntro {
		t.Fatalf("expected a synthesized introduction for por: found=%v err=%v", hasPorIntro, err)
	}

	// exercises/reviews: item_id/mode/correct_answer(+chosen) attached.
	var exMode int16
	var exCorrect string
	if err := store.Pool().QueryRow(ctx, `SELECT mode, correct_answer FROM exercises WHERE skill_id = $1`, spa.ID).Scan(&exMode, &exCorrect); err != nil {
		t.Fatalf("query backfilled exercise: %v", err)
	}
	if exMode != 0 || exCorrect != "spa" {
		t.Fatalf("exercise mode/correct_answer = %d/%q, want 0/\"spa\"", exMode, exCorrect)
	}

	var revMode int16
	var revChosen, revCorrect string
	if err := store.Pool().QueryRow(ctx, `SELECT mode, chosen, correct_answer FROM reviews WHERE skill_id = $1`, spa.ID).Scan(&revMode, &revChosen, &revCorrect); err != nil {
		t.Fatalf("query backfilled review: %v", err)
	}
	if revMode != 0 || revChosen != "spa" || revCorrect != "spa" {
		t.Fatalf("review mode/chosen/correct_answer = %d/%q/%q, want 0/\"spa\"/\"spa\"", revMode, revChosen, revCorrect)
	}

	// Idempotency: re-running must migrate/update nothing further.
	res2, err := runBackfillV2(ctx, logger, store)
	if err != nil {
		t.Fatalf("runBackfillV2 (second run): %v", err)
	}
	if res2.UserItemsMigrated != 0 {
		t.Fatalf("second run UserItemsMigrated = %d, want 0", res2.UserItemsMigrated)
	}
	if res2.IntroductionsSynthesized != 0 {
		t.Fatalf("second run IntroductionsSynthesized = %d, want 0", res2.IntroductionsSynthesized)
	}
	if res2.ExercisesRowsUpdated != 0 {
		t.Fatalf("second run ExercisesRowsUpdated = %d, want 0", res2.ExercisesRowsUpdated)
	}
	if res2.ReviewsRowsUpdated != 0 {
		t.Fatalf("second run ReviewsRowsUpdated = %d, want 0", res2.ReviewsRowsUpdated)
	}
}
