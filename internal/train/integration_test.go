package train_test

// End-to-end train-loop test against a real PostgreSQL 18 + the real engram
// engine. Skipped unless GEODRILL_TEST_DATABASE_URL is set. When running the
// integration tests across packages, use `go test -p 1 ./...` so this and the
// storage integration tests don't reset the shared schema concurrently.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/train"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run the train integration test")
	}
	return dsn
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
		t.Fatalf("migrate up again: %v", err)
	}
}

func TestTrainLoopEndToEnd(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	svc := train.NewService(st, engram.NewScheduler(), 42, func() time.Time { return now })

	u, err := st.UpsertUser(ctx, 5150, "e2e")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	deck, err := st.UpsertDeck(ctx, "romance", "Romance")
	if err != nil {
		t.Fatalf("deck: %v", err)
	}
	spa, err := st.UpsertSkill(ctx, deck.ID, "spa", "Spanish")
	if err != nil {
		t.Fatalf("skill spa: %v", err)
	}
	por, err := st.UpsertSkill(ctx, deck.ID, "por", "Portuguese")
	if err != nil {
		t.Fatalf("skill por: %v", err)
	}
	if err := st.SetUserDeckEnabled(ctx, u.ID, deck.ID, true); err != nil {
		t.Fatalf("enable deck: %v", err)
	}
	for _, key := range []string{"spa", "por"} {
		for _, p := range []string{key + " frase uno larga", key + " frase dos larga", key + " frase tres larga"} {
			if err := st.InsertContent(ctx, "sentence", key, p, "tatoeba#x", len([]rune(p))); err != nil {
				t.Fatalf("insert content: %v", err)
			}
		}
	}
	skillByKey := map[string]storage.Skill{"spa": spa, "por": por}

	// 1. next exercise
	res, err := svc.NextExercise(ctx, u, now)
	if err != nil {
		t.Fatalf("NextExercise: %v", err)
	}
	if res.Kind != train.KindExercise || res.Prompt == nil {
		t.Fatalf("expected an exercise, got kind=%v", res.Kind)
	}
	if len(res.Prompt.Buttons) != 2 {
		t.Fatalf("romance(2 skills) should give 2 buttons, got %d", len(res.Prompt.Buttons))
	}
	// telegram would record the message id after sending
	if err := st.SetExerciseMessageID(ctx, res.Prompt.ExerciseID, 111); err != nil {
		t.Fatalf("set message id: %v", err)
	}

	// 2. answer with the first button
	cb, ok := train.ParseCallback(res.Prompt.Buttons[0].CallbackData)
	if !ok {
		t.Fatalf("button 0 callback %q didn't parse", res.Prompt.Buttons[0].CallbackData)
	}
	ar, err := svc.Answer(ctx, cb, now)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if ar.Stale {
		t.Fatalf("first answer must not be stale")
	}
	if ar.Correct != (cb.Key == ar.CorrectKey) {
		t.Fatalf("Correct=%v but chosen=%q correct=%q", ar.Correct, cb.Key, ar.CorrectKey)
	}
	if !ar.HasMessage || ar.MessageID != 111 {
		t.Fatalf("answer should carry the stored message id, got hasMsg=%v id=%d", ar.HasMessage, ar.MessageID)
	}
	if len(ar.Buttons) != 2 {
		t.Fatalf("graded buttons = %d, want 2", len(ar.Buttons))
	}
	corrects := 0
	for _, b := range ar.Buttons {
		if b.Mark == train.MarkCorrect {
			corrects++
		}
	}
	if corrects != 1 {
		t.Fatalf("exactly one button must be ✅, got %d", corrects)
	}

	// 3. single-use guard: a second tap is stale
	ar2, err := svc.Answer(ctx, cb, now)
	if err != nil {
		t.Fatalf("second Answer: %v", err)
	}
	if !ar2.Stale {
		t.Fatalf("second answer must be stale (single-use guard)")
	}

	// 4. the review was persisted and the card moved into the future (FSRS)
	recs, err := st.ListReviewsSince(ctx, u.ID, now.Add(-time.Hour))
	if err != nil || len(recs) != 1 {
		t.Fatalf("reviews = %d, %v", len(recs), err)
	}
	target := skillByKey[ar.CorrectKey]
	card, found, err := st.GetCard(ctx, u.ID, target.ID)
	if err != nil || !found {
		t.Fatalf("card after review: found=%v err=%v", found, err)
	}
	if !card.Due.After(now) {
		t.Fatalf("card due %v should be after now %v", card.Due, now)
	}

	// 5. /stats sees exactly one review today
	stats, err := svc.Stats(ctx, u, now)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ReviewsToday != 1 {
		t.Fatalf("stats reviews today = %d, want 1", stats.ReviewsToday)
	}

	// 6. /practice generates an exercise but answering it records NO review
	pres, err := svc.NextPractice(ctx, u, now)
	if err != nil || pres.Kind != train.KindExercise {
		t.Fatalf("NextPractice kind=%v err=%v", pres.Kind, err)
	}
	pcb, ok := train.ParseCallback(pres.Prompt.Buttons[0].CallbackData)
	if !ok || !pcb.Practice {
		t.Fatalf("practice button should parse as a practice callback: %+v ok=%v", pcb, ok)
	}
	if err := st.SetExerciseMessageID(ctx, pres.Prompt.ExerciseID, 222); err != nil {
		t.Fatalf("set practice message id: %v", err)
	}
	par, err := svc.Answer(ctx, pcb, now)
	if err != nil || par.Stale {
		t.Fatalf("practice answer: stale=%v err=%v", par.Stale, err)
	}
	recs2, err := st.ListReviewsSince(ctx, u.ID, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("reviews after practice: %v", err)
	}
	if len(recs2) != 1 {
		t.Fatalf("practice must not append a review; reviews = %d, want 1", len(recs2))
	}
}
