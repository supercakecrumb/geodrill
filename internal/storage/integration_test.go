package storage_test

// Integration tests against a real PostgreSQL 18. They are skipped unless
// GEODRILL_TEST_DATABASE_URL is set (so `go test ./...` stays green without
// docker). Example:
//
//	GEODRILL_TEST_DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill?sslmode=disable' \
//	  go test ./internal/storage/...

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/storage/engramstore"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run storage integration tests")
	}
	return dsn
}

// freshSchema drops and re-applies the schema so each test starts clean, and
// exercises the down migrations in the process (up -> down -> up).
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

func TestMigrateUpDownUp(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Schema is usable: a trivial upsert works.
	if _, err := st.UpsertUser(ctx, 1, "smoke"); err != nil {
		t.Fatalf("upsert after migrate: %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// user upsert is idempotent on telegram_id
	u, err := st.UpsertUser(ctx, 4242, "aurora")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	u2, err := st.UpsertUser(ctx, 4242, "aurora2")
	if err != nil || u2.ID != u.ID {
		t.Fatalf("upsert user not idempotent: %v id1=%s id2=%s", err, u.ID, u2.ID)
	}

	// deck + skills
	deck, err := st.UpsertDeck(ctx, "romance", "Romance languages")
	if err != nil {
		t.Fatalf("upsert deck: %v", err)
	}
	spa, err := st.UpsertSkill(ctx, deck.ID, "spa", "Spanish")
	if err != nil {
		t.Fatalf("upsert skill spa: %v", err)
	}
	por, err := st.UpsertSkill(ctx, deck.ID, "por", "Portuguese")
	if err != nil || por.Key != "por" {
		t.Fatalf("upsert skill por: %v", err)
	}

	// enable deck for the user, verify enabled-skill listing
	if err := st.SetUserDeckEnabled(ctx, u.ID, deck.ID, true); err != nil {
		t.Fatalf("enable deck: %v", err)
	}
	n, err := st.CountEnabledDecks(ctx, u.ID)
	if err != nil || n != 1 {
		t.Fatalf("count enabled decks = %d, %v", n, err)
	}
	skills, err := st.ListEnabledSkills(ctx, u.ID)
	if err != nil || len(skills) != 2 {
		t.Fatalf("enabled skills = %d, %v", len(skills), err)
	}

	// card round trip
	now := time.Now().UTC().Truncate(time.Second)
	card := storage.CardFields{Due: now.Add(24 * time.Hour), Stability: 3.5, Difficulty: 5.0, Reps: 1, Lapses: 0, State: 2, LastReview: now}
	if err := st.PutCard(ctx, u.ID, spa.ID, card); err != nil {
		t.Fatalf("put card: %v", err)
	}
	got, found, err := st.GetCard(ctx, u.ID, spa.ID)
	if err != nil || !found {
		t.Fatalf("get card: found=%v err=%v", found, err)
	}
	if !got.Due.Equal(card.Due) || got.Stability != card.Stability || got.State != card.State || got.Reps != 1 {
		t.Fatalf("card round trip mismatch: %+v vs %+v", got, card)
	}

	// SkillCards should surface the persisted card for spa, none for por
	scs, err := st.ListEnabledSkillCards(ctx, u.ID)
	if err != nil {
		t.Fatalf("list skill cards: %v", err)
	}
	for _, sc := range scs {
		switch sc.Key {
		case "spa":
			if !sc.HasCard {
				t.Fatalf("spa should have a card")
			}
		case "por":
			if sc.HasCard {
				t.Fatalf("por should not have a card yet")
			}
		}
	}

	// content + exclusion sampling
	for i, payload := range []string{"Hola mundo uno", "Hola mundo dos", "Hola mundo tres"} {
		if err := st.InsertContent(ctx, "sentence", "spa", payload, "tatoeba#"+itoa(i), len([]rune(payload))); err != nil {
			t.Fatalf("insert content: %v", err)
		}
	}
	// idempotent insert
	if err := st.InsertContent(ctx, "sentence", "spa", "Hola mundo uno", "tatoeba#0", 13); err != nil {
		t.Fatalf("idempotent insert: %v", err)
	}
	cnt, err := st.CountContentByKey(ctx, "sentence", "spa")
	if err != nil || cnt != 3 {
		t.Fatalf("content count = %d, %v (idempotence broken)", cnt, err)
	}
	c, ok, err := st.SampleContent(ctx, u.ID, "spa")
	if err != nil || !ok {
		t.Fatalf("sample content: ok=%v err=%v", ok, err)
	}

	// exercise single-use guard
	exID, err := st.InsertExercise(ctx, u.ID, spa.ID, c.ID, []byte(`[{"key":"spa","label":"Spanish"},{"key":"por","label":"Portuguese"}]`))
	if err != nil {
		t.Fatalf("insert exercise: %v", err)
	}
	if err := st.SetExerciseMessageID(ctx, exID, 999); err != nil {
		t.Fatalf("set message id: %v", err)
	}
	owned1, err := st.MarkExerciseAnswered(ctx, exID, now)
	if err != nil || !owned1 {
		t.Fatalf("first answer should be owned: owned=%v err=%v", owned1, err)
	}
	owned2, err := st.MarkExerciseAnswered(ctx, exID, now.Add(time.Second))
	if err != nil || owned2 {
		t.Fatalf("second answer must be rejected (stale): owned=%v err=%v", owned2, err)
	}

	// review append + read back
	ms := 1200
	if err := st.InsertReview(ctx, storage.ReviewInsert{
		UserID: u.ID, SkillID: spa.ID, ExerciseID: &exID, ContentID: &c.ID,
		ChosenKey: "por", CorrectKey: "spa", Correct: false, Rating: 1, ResponseMS: &ms,
		StabilityBefore: 1, DifficultyBefore: 5, StabilityAfter: 0.5, DifficultyAfter: 5.2,
		StateBefore: 0, ScheduledDays: 0, ElapsedDays: 0, ReviewedAt: now,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}
	recs, err := st.ListReviewsSince(ctx, u.ID, now.Add(-time.Hour))
	if err != nil || len(recs) != 1 {
		t.Fatalf("list reviews = %d, %v", len(recs), err)
	}
	if recs[0].CorrectKey != "spa" || recs[0].ChosenKey != "por" || recs[0].Correct {
		t.Fatalf("review round trip mismatch: %+v", recs[0])
	}
	atts, err := st.ListAttemptsSince(ctx, u.ID, now.Add(-time.Hour))
	if err != nil || len(atts) != 1 || atts[0].ResponseMS != 1200 {
		t.Fatalf("attempts = %+v, %v", atts, err)
	}

	// por content is empty -> exclusion still returns the por pool as empty
	_, ok, err = st.SampleContentAny(ctx, "por")
	if err != nil {
		t.Fatalf("sample any por: %v", err)
	}
	if ok {
		t.Fatalf("por pool should be empty")
	}
}

func TestEngramAdapterRoundTrip(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	u, err := st.UpsertUser(ctx, 7, "adapter")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	deck, err := st.UpsertDeck(ctx, "cjk", "CJK")
	if err != nil {
		t.Fatalf("deck: %v", err)
	}
	jpn, err := st.UpsertSkill(ctx, deck.ID, "jpn", "Japanese")
	if err != nil {
		t.Fatalf("skill: %v", err)
	}

	adapter := engramstore.New(st, u.ID)

	// SkillStore.Skills
	got, err := adapter.Skills(ctx, engram.DeckID(deck.ID.String()))
	if err != nil || len(got) != 1 || got[0].Key != "jpn" {
		t.Fatalf("adapter Skills = %+v, %v", got, err)
	}

	// Card: unseen -> false
	sid := engram.SkillID(jpn.ID.String())
	_, seen, err := adapter.Card(ctx, sid)
	if err != nil || seen {
		t.Fatalf("unseen card: seen=%v err=%v", seen, err)
	}

	// PutCard then Card
	now := time.Now().UTC().Truncate(time.Second)
	cs := engram.CardState{Due: now.Add(48 * time.Hour), Stability: 10, Difficulty: 6, Reps: 3, Lapses: 1, State: engram.StateReview, LastReview: now}
	if err := adapter.PutCard(ctx, sid, cs); err != nil {
		t.Fatalf("adapter PutCard: %v", err)
	}
	back, seen, err := adapter.Card(ctx, sid)
	if err != nil || !seen {
		t.Fatalf("adapter Card after put: seen=%v err=%v", seen, err)
	}
	if !back.Due.Equal(cs.Due) || back.Stability != 10 || back.State != engram.StateReview || back.Reps != 3 {
		t.Fatalf("adapter card mismatch: %+v vs %+v", back, cs)
	}

	// ReviewStore.Append + Log
	rev := engram.Review{
		SkillID: sid, Rating: engram.Good, ReviewedAt: now,
		StabilityBefore: 5, DifficultyBefore: 6, StabilityAfter: 10, DifficultyAfter: 6,
		StateBefore: engram.StateLearning, ScheduledDays: 2, ElapsedDays: 1,
	}
	if err := adapter.Append(ctx, rev); err != nil {
		t.Fatalf("adapter Append: %v", err)
	}
	log, err := adapter.Log(ctx, now.Add(-time.Hour))
	if err != nil || len(log) != 1 {
		t.Fatalf("adapter Log = %d, %v", len(log), err)
	}
	if log[0].Rating != engram.Good || log[0].StabilityAfter != 10 || log[0].ScheduledDays != 2 {
		t.Fatalf("adapter log mismatch: %+v", log[0])
	}
}

// itoa avoids importing strconv for a single use.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
