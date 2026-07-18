package study_test

// End-to-end test of the FULL loop (architecture §1, §4, §5) against a
// real PostgreSQL 18: seed every topic package, then drive
// intro -> answer -> exercise -> answer -> topic browser through
// internal/study.Service exactly as cmd/bot wires it, asserting the
// database rows and returned view models it produces.
//
// Skipped unless GEODRILL_TEST_DATABASE_URL is set. Its database name MUST
// contain "test" (testDSN's safety fuse) since freshSchema drops every
// table (up -> down -> up) before the test runs. When running integration
// tests across packages, use `go test -p 1 ./...` so this and the storage
// package's own integration tests never reset the shared schema
// concurrently (architecture contract, "verified baselines").
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/study"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/guesslang"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
	"github.com/supercakecrumb/geodrill/internal/topics/specialchars"
	"github.com/supercakecrumb/geodrill/internal/topics/words"
)

// testDSN mirrors internal/storage/integration_test.go's safety fuse
// (database name must contain "test") — this test drops every table via
// freshSchema, so it must never be pointed at a live DB.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run the study integration test")
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

// registerGeneratorsOnce mirrors cmd/bot's startup wiring exactly (the four
// topic packages, keyed by quiz_kind). Guarded so re-running tests in the
// same process (e.g. `go test -count=2`) never hits topics.Register's
// duplicate-registration panic.
var registerOnce sync.Once

func registerGenerators(store *storage.Store) {
	registerOnce.Do(func() {
		topics.Register(guesslang.New(store))
		topics.Register(specialchars.New())
		topics.Register(roadside.New())
		topics.Register(words.New())
	})
}

// repoPath resolves a path relative to the repo root, from THIS test file's
// own location (so it doesn't depend on `go test`'s cwd, which is this
// package's directory, not the repo root) — needed only for
// specialchars.Seed, whose OWN default path is cwd-relative; the other
// three packages' Seed already resolves paths relative to their own source
// file via runtime.Caller(0).
func repoPath(rel string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", rel)
}

func seedAllTopics(t *testing.T, ctx context.Context, store *storage.Store) {
	t.Helper()
	if err := specialchars.SeedFromFile(ctx, store, repoPath("seeds/special_chars.yaml")); err != nil {
		t.Fatalf("seed specialchars: %v", err)
	}
	if err := guesslang.Seed(ctx, store); err != nil {
		t.Fatalf("seed guesslang: %v", err)
	}
	if err := words.Seed(ctx, store); err != nil {
		t.Fatalf("seed words: %v", err)
	}
	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside: %v", err)
	}
}

// charsPayload mirrors specialchars' unexported item payload shape (just
// the fields this test needs) so it can pick a known single-language item
// without reaching into that package's internals.
type charsPayload struct {
	Languages []string `json:"languages"`
}

func findSingleLanguageItem(items []storage.Item) (storage.Item, bool) {
	for _, it := range items {
		var p charsPayload
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			continue
		}
		if len(p.Languages) == 1 {
			return it, true
		}
	}
	return storage.Item{}, false
}

// correctOptionIndex finds ex's correct answer's position among its
// persisted options (mirrors internal/study's own serialization: ModeSingle
// options are {key,label}; ModeSet options are {keys,label} compared via
// the canonical sorted-join form).
func correctOptionIndex(t *testing.T, ex storage.Exercise) int {
	t.Helper()
	switch quiz.Mode(ex.Mode) {
	case quiz.ModeSet:
		var opts []struct {
			Keys  []string `json:"keys"`
			Label string   `json:"label"`
		}
		if err := json.Unmarshal(ex.Options, &opts); err != nil {
			t.Fatalf("unmarshal set options: %v", err)
		}
		for i, o := range opts {
			if canonJoin(o.Keys) == ex.CorrectAnswer {
				return i
			}
		}
	default:
		var opts []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		}
		if err := json.Unmarshal(ex.Options, &opts); err != nil {
			t.Fatalf("unmarshal single options: %v", err)
		}
		for i, o := range opts {
			if o.Key == ex.CorrectAnswer {
				return i
			}
		}
	}
	t.Fatalf("could not find correct_answer %q among options %s", ex.CorrectAnswer, ex.Options)
	return -1
}

func canonJoin(keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func seedDueItem(t *testing.T, ctx context.Context, store *storage.Store, userID, itemID uuid.UUID, due time.Time, reps int) {
	t.Helper()
	if err := store.PutUserItem(ctx, userID, itemID, int16(engram.LifecycleIntroduced),
		storage.CardFields{Due: due, State: int16(engram.StateNew), Reps: reps}, due, time.Time{}); err != nil {
		t.Fatalf("seed due user_item: %v", err)
	}
}

func equalIntSets(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := append([]int(nil), a...), append([]int(nil), b...)
	sort.Ints(sa)
	sort.Ints(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func TestFullLoop(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	registerGenerators(store)
	seedAllTopics(t, ctx, store)

	sched := engram.NewScheduler()
	svc := study.New(store, sched, study.GlobalRegistry, nil, 42)

	user, err := store.UpsertUser(ctx, 900001, "integration")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// ── IntroSummary before anything is introduced ──────────────────────
	available, budgetLeft, err := svc.IntroSummary(ctx, user.ID)
	if err != nil {
		t.Fatalf("IntroSummary: %v", err)
	}
	if available == 0 {
		t.Fatalf("expected available intro candidates from the seeded topics, got 0")
	}
	if budgetLeft != 10 {
		t.Fatalf("expected the default daily_intro_cap=10 as budgetLeft, got %d", budgetLeft)
	}

	// ── NextIntro / AnswerIntro: the three outcomes, on three different
	// items (architecture §5.1) ─────────────────────────────────────────
	outcomes := []engram.IntroOutcome{engram.IntroGotIt, engram.IntroKnown, engram.IntroTestMe}
	introItems := make([]uuid.UUID, 0, len(outcomes))
	firstIntroID := uuid.Nil

	for i, outcome := range outcomes {
		card, err := svc.NextIntro(ctx, user.ID)
		if err != nil {
			t.Fatalf("NextIntro[%d]: %v", i, err)
		}
		if card.Reason != telegram.IntroOK {
			t.Fatalf("NextIntro[%d]: expected IntroOK, got reason=%v", i, card.Reason)
		}
		for _, seen := range introItems {
			if seen == card.ItemID {
				t.Fatalf("NextIntro[%d] returned an already-introduced item %s", i, card.ItemID)
			}
		}
		introItems = append(introItems, card.ItemID)
		if i == 0 {
			firstIntroID = card.IntroID
		}

		ack, err := svc.AnswerIntro(ctx, user.ID, card.IntroID, outcome)
		if err != nil {
			t.Fatalf("AnswerIntro[%d]: %v", i, err)
		}
		if ack.Text == "" {
			t.Fatalf("AnswerIntro[%d]: expected a non-empty confirmation", i)
		}

		ui, found, err := store.GetUserItem(ctx, user.ID, card.ItemID)
		if err != nil || !found {
			t.Fatalf("GetUserItem[%d]: found=%v err=%v", i, found, err)
		}
		switch outcome {
		case engram.IntroGotIt:
			if ui.Lifecycle != int16(engram.LifecycleIntroduced) {
				t.Fatalf("got_it[%d]: expected lifecycle=Introduced, got %d", i, ui.Lifecycle)
			}
			if ui.Card.State != int16(engram.StateNew) {
				t.Fatalf("got_it[%d]: expected a fresh StateNew card, got state=%d", i, ui.Card.State)
			}
			if ui.IntroducedAt.IsZero() {
				t.Fatalf("got_it[%d]: introduced_at should be set", i)
			}
		case engram.IntroKnown:
			if ui.Lifecycle != int16(engram.LifecycleKnown) {
				t.Fatalf("known[%d]: expected lifecycle=Known, got %d", i, ui.Lifecycle)
			}
			if ui.KnownAt.IsZero() {
				t.Fatalf("known[%d]: known_at should be set", i)
			}
		case engram.IntroTestMe:
			if ui.Lifecycle != int16(engram.LifecycleReviewing) {
				t.Fatalf("test_me[%d]: expected lifecycle=Reviewing, got %d", i, ui.Lifecycle)
			}
			if ui.Card.State != int16(engram.StateReview) {
				t.Fatalf("test_me[%d]: expected a seeded StateReview card, got state=%d", i, ui.Card.State)
			}
			if ui.Card.Stability < 10 {
				t.Fatalf("test_me[%d]: expected a high seeded stability (~15.69d), got %v", i, ui.Card.Stability)
			}
		}

		intro, found, err := store.GetIntroductionByID(ctx, card.IntroID)
		if err != nil || !found {
			t.Fatalf("GetIntroductionByID[%d]: found=%v err=%v", i, found, err)
		}
		if intro.Outcome == nil || *intro.Outcome != int16(outcome) {
			t.Fatalf("introductions[%d] outcome mismatch: %+v", i, intro)
		}
		if intro.AnsweredAt.IsZero() {
			t.Fatalf("introductions[%d]: answered_at should be set", i)
		}

		tier, err := store.GetItemEffectiveTier(ctx, card.ItemID)
		if err != nil {
			t.Fatalf("GetItemEffectiveTier[%d]: %v", i, err)
		}
		progressRows, err := store.ListTierProgressForUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("ListTierProgressForUser[%d]: %v", i, err)
		}
		foundTierRow := false
		for _, p := range progressRows {
			if p.Tier == tier {
				foundTierRow = true
			}
		}
		if !foundTierRow {
			t.Fatalf("expected a user_tier_progress row for tier %d after intro[%d]", tier, i)
		}
	}

	// A stale second tap on the FIRST intro card (already answered got_it)
	// must not double-apply the lifecycle transition.
	staleAck, err := svc.AnswerIntro(ctx, user.ID, firstIntroID, engram.IntroKnown)
	if err != nil {
		t.Fatalf("AnswerIntro (stale second tap): %v", err)
	}
	if staleAck.Text == "" {
		t.Fatalf("stale AnswerIntro should still return a non-empty ack")
	}
	uiAfterStale, found, err := store.GetUserItem(ctx, user.ID, introItems[0])
	if err != nil || !found {
		t.Fatalf("GetUserItem after stale re-tap: found=%v err=%v", found, err)
	}
	if uiAfterStale.Lifecycle != int16(engram.LifecycleIntroduced) {
		t.Fatalf("stale re-tap must not change lifecycle: still expected Introduced, got %d", uiAfterStale.Lifecycle)
	}

	// ── Trainer: NextExercise / Answer (correct, wrong, stale) ─────
	// Roadside items always have exactly two fixed options (Left/Right), so
	// picking any two avoids the "not enough distractors" edge case a
	// sparsely-seeded specialchars item could hit.
	roadsideTopic, found, err := store.GetTopicByPath(ctx, "roads/which-side")
	if err != nil || !found {
		t.Fatalf("get roads/which-side topic: found=%v err=%v", found, err)
	}
	roadsideItems, err := store.ListItemsWithTierByTopic(ctx, roadsideTopic.ID)
	if err != nil {
		t.Fatalf("list roadside items: %v", err)
	}
	if len(roadsideItems) < 2 {
		t.Fatalf("expected at least 2 roadside items in the seed data, got %d", len(roadsideItems))
	}

	gradeUser, err := store.UpsertUser(ctx, 900002, "grade-tester")
	if err != nil {
		t.Fatalf("create grade user: %v", err)
	}
	now := time.Now().UTC()
	itemA, itemB := roadsideItems[0].Item, roadsideItems[1].Item
	seedDueItem(t, ctx, store, gradeUser.ID, itemA.ID, now.Add(-2*time.Minute), 0)
	seedDueItem(t, ctx, store, gradeUser.ID, itemB.ID, now.Add(-1*time.Minute), 0)

	if due, err := svc.DueCount(ctx, gradeUser.ID); err != nil || due != 2 {
		t.Fatalf("DueCount before answering = %d, want 2 (err=%v)", due, err)
	}

	promptA, err := svc.NextExercise(ctx, gradeUser.ID)
	if err != nil {
		t.Fatalf("NextExercise (A): %v", err)
	}
	if promptA.Kind != telegram.PromptKindExercise {
		t.Fatalf("expected an exercise for the earliest-due item, got kind=%v", promptA.Kind)
	}
	if len(promptA.Options) != 2 {
		t.Fatalf("expected 2 options (Left/Right) for a roadside exercise, got %d", len(promptA.Options))
	}
	exA, found, err := store.GetExerciseByID(ctx, promptA.ExerciseID)
	if err != nil || !found {
		t.Fatalf("GetExerciseByID (A): found=%v err=%v", found, err)
	}
	if exA.ItemID != itemA.ID {
		t.Fatalf("expected item A (earliest due) to be picked first, got exercise item %v", exA.ItemID)
	}
	correctA := correctOptionIndex(t, exA)
	wrongA := (correctA + 1) % len(promptA.Options)

	wrongRes, err := svc.Answer(ctx, gradeUser.ID, promptA.ExerciseID, wrongA)
	if err != nil {
		t.Fatalf("Answer (wrong): %v", err)
	}
	if wrongRes.Stale || wrongRes.Correct {
		t.Fatalf("expected a fresh, incorrect answer, got %+v", wrongRes)
	}
	foundWrongMark := false
	for _, o := range wrongRes.Options {
		if o.Mark == telegram.MarkWrong {
			foundWrongMark = true
		}
	}
	if !foundWrongMark {
		t.Fatalf("expected the tapped wrong option to be marked, got %+v", wrongRes.Options)
	}

	// Stale second tap on the SAME exercise must not double-record.
	staleRes, err := svc.Answer(ctx, gradeUser.ID, promptA.ExerciseID, correctA)
	if err != nil {
		t.Fatalf("Answer (stale): %v", err)
	}
	if !staleRes.Stale {
		t.Fatalf("second tap on an already-answered exercise should be Stale")
	}

	reviewsA, err := store.GetReviewsByItem(ctx, itemA.ID)
	if err != nil {
		t.Fatalf("GetReviewsByItem (A): %v", err)
	}
	if len(reviewsA) != 1 {
		t.Fatalf("the stale second tap must not add a second review row, got %d", len(reviewsA))
	}
	if reviewsA[0].Correct {
		t.Fatalf("recorded review for A should be incorrect")
	}

	// item A's due date must have moved forward (FSRS movement) — it can no
	// longer be "due now", so the next NextExercise call picks item B.
	promptB, err := svc.NextExercise(ctx, gradeUser.ID)
	if err != nil {
		t.Fatalf("NextExercise (B): %v", err)
	}
	if promptB.Kind != telegram.PromptKindExercise {
		t.Fatalf("expected an exercise for item B, got kind=%v", promptB.Kind)
	}
	exB, found, err := store.GetExerciseByID(ctx, promptB.ExerciseID)
	if err != nil || !found {
		t.Fatalf("GetExerciseByID (B): found=%v err=%v", found, err)
	}
	if exB.ItemID != itemB.ID {
		t.Fatalf("expected item B next (item A rescheduled past due), got exercise item %v", exB.ItemID)
	}
	correctB := correctOptionIndex(t, exB)

	correctRes, err := svc.Answer(ctx, gradeUser.ID, promptB.ExerciseID, correctB)
	if err != nil {
		t.Fatalf("Answer (correct): %v", err)
	}
	if !correctRes.Correct {
		t.Fatalf("expected a correct answer, got %+v", correctRes)
	}
	reviewsB, err := store.GetReviewsByItem(ctx, itemB.ID)
	if err != nil {
		t.Fatalf("GetReviewsByItem (B): %v", err)
	}
	if len(reviewsB) != 1 || !reviewsB[0].Correct {
		t.Fatalf("expected exactly one correct review row for B, got %+v", reviewsB)
	}
	if reviewsB[0].StabilityAfter <= 0 {
		t.Fatalf("expected FSRS stability to move above 0 after a correct answer, got %v", reviewsB[0].StabilityAfter)
	}
	uiB, found, err := store.GetUserItem(ctx, gradeUser.ID, itemB.ID)
	if err != nil || !found {
		t.Fatalf("GetUserItem (B): found=%v err=%v", found, err)
	}
	if !uiB.Card.Due.After(now) {
		t.Fatalf("expected item B's due date to move forward past %v, got %v", now, uiB.Card.Due)
	}

	// ── Service.Stats over reviews/user_items ─────────────────────────
	gradeStats, err := svc.Stats(ctx, gradeUser.ID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if gradeStats.ReviewsToday < 2 {
		t.Fatalf("expected at least the 2 roadside reviews counted today, got %d", gradeStats.ReviewsToday)
	}
	if gradeStats.Accuracy <= 0 || gradeStats.Accuracy >= 1 {
		t.Fatalf("expected a fractional accuracy (1 correct of 2 reviews), got %v", gradeStats.Accuracy)
	}
	if len(gradeStats.DueForecast) != 7 {
		t.Fatalf("expected a 7-day due forecast, got %d entries", len(gradeStats.DueForecast))
	}
	var roadsTotal, roadsCorrect int
	for _, ta := range gradeStats.ByTopic {
		if ta.Name == roadsideTopic.Name {
			roadsTotal, roadsCorrect = ta.Total, ta.Correct
		}
	}
	if roadsTotal != 2 || roadsCorrect != 1 {
		t.Fatalf("expected per-topic stats for %q to be total=2 correct=1, got total=%d correct=%d", roadsideTopic.Name, roadsTotal, roadsCorrect)
	}
	if len(gradeStats.Confusion) == 0 {
		t.Fatalf("expected at least one confusion pair from the wrong roadside answer")
	}
	if gradeStats.Introduced < 2 {
		t.Fatalf("expected Introduced to count at least the 2 seeded roadside items, got %d", gradeStats.Introduced)
	}

	// ── AnswerText: a specialchars single-language item forced into
	// ModeText via the reps-based rotation rule ─────────────────────────
	charsTopic, found, err := store.GetTopicByPath(ctx, "languages/special-characters")
	if err != nil || !found {
		t.Fatalf("get languages/special-characters topic: found=%v err=%v", found, err)
	}
	charsItems, err := store.ListActiveItemsByTopic(ctx, charsTopic.ID)
	if err != nil {
		t.Fatalf("list special-characters items: %v", err)
	}
	singleItem, ok := findSingleLanguageItem(charsItems)
	if !ok {
		t.Fatalf("expected at least one single-language item in seeds/special_chars.yaml")
	}

	textUser, err := store.UpsertUser(ctx, 900003, "text-tester")
	if err != nil {
		t.Fatalf("create text user: %v", err)
	}
	// exercise_modes for special-characters is {single,set,text}; reps=2
	// rotates the FIRST attempt to "text" (modeRotationOrder), which a
	// single-language item's shape also supports, so it succeeds.
	seedDueItem(t, ctx, store, textUser.ID, singleItem.ID, now, 2)

	textPrompt, err := svc.NextExercise(ctx, textUser.ID)
	if err != nil {
		t.Fatalf("NextExercise (text): %v", err)
	}
	if textPrompt.Kind != telegram.PromptKindExercise || textPrompt.Mode != quiz.ModeText {
		t.Fatalf("expected a ModeText exercise, got kind=%v mode=%v", textPrompt.Kind, textPrompt.Mode)
	}
	if len(textPrompt.Options) != 0 {
		t.Fatalf("ModeText should render no button options, got %d", len(textPrompt.Options))
	}

	exText, found, err := store.GetExerciseByID(ctx, textPrompt.ExerciseID)
	if err != nil || !found {
		t.Fatalf("GetExerciseByID (text): found=%v err=%v", found, err)
	}
	textRes, ok, err := svc.AnswerText(ctx, textUser.ID, exText.CorrectAnswer)
	if err != nil {
		t.Fatalf("AnswerText: %v", err)
	}
	if !ok {
		t.Fatalf("AnswerText: expected an open ModeText exercise to be found")
	}
	if !textRes.Correct {
		t.Fatalf("typing the exact persisted correct_answer should grade correct, got %+v", textRes)
	}
	textReviews, err := store.GetReviewsByItem(ctx, singleItem.ID)
	if err != nil {
		t.Fatalf("GetReviewsByItem (text): %v", err)
	}
	if len(textReviews) != 1 || !textReviews[0].Correct {
		t.Fatalf("expected exactly one correct review row for the text-mode item, got %+v", textReviews)
	}

	// stale AnswerText: no open ModeText exercise remains for textUser.
	_, staleOK, err := svc.AnswerText(ctx, textUser.ID, exText.CorrectAnswer)
	if err != nil {
		t.Fatalf("AnswerText (after answered): %v", err)
	}
	if staleOK {
		t.Fatalf("expected ok=false once the open ModeText exercise has been answered")
	}

	// ── NextPractice / Answer (practice=true): counts in stats but
	// applies ZERO FSRS movement (architecture: /practice's contract) ────
	practiceUser, err := store.UpsertUser(ctx, 900010, "practice-tester")
	if err != nil {
		t.Fatalf("create practice user: %v", err)
	}
	practicePrompt, err := svc.NextPractice(ctx, practiceUser.ID)
	if err != nil {
		t.Fatalf("NextPractice: %v", err)
	}
	if practicePrompt.Kind != telegram.PromptKindExercise || !practicePrompt.Practice {
		t.Fatalf("expected a practice exercise (a fresh user has every topic enabled by default), got kind=%v practice=%v", practicePrompt.Kind, practicePrompt.Practice)
	}
	exPractice, found, err := store.GetExerciseByID(ctx, practicePrompt.ExerciseID)
	if err != nil || !found {
		t.Fatalf("GetExerciseByID (practice): found=%v err=%v", found, err)
	}
	if !exPractice.Practice {
		t.Fatalf("expected the persisted exercise row to carry practice=true")
	}
	if exPractice.ItemID == uuid.Nil {
		t.Fatalf("expected the practice exercise to carry an item_id")
	}
	practiceItemID := exPractice.ItemID
	cardBefore, foundBefore, err := store.GetUserItem(ctx, practiceUser.ID, practiceItemID)
	if err != nil {
		t.Fatalf("GetUserItem before practice answer: %v", err)
	}

	practiceCorrectIdx := correctOptionIndex(t, exPractice)
	practiceRes, err := svc.Answer(ctx, practiceUser.ID, practicePrompt.ExerciseID, practiceCorrectIdx)
	if err != nil {
		t.Fatalf("Answer (practice): %v", err)
	}
	if !practiceRes.Correct {
		t.Fatalf("expected the correct-option tap to grade correct, got %+v", practiceRes)
	}
	if !practiceRes.Practice {
		t.Fatalf("expected AnswerResult.Practice=true so the caller advances via NextPractice")
	}

	cardAfter, foundAfter, err := store.GetUserItem(ctx, practiceUser.ID, practiceItemID)
	if err != nil {
		t.Fatalf("GetUserItem after practice answer: %v", err)
	}
	if foundAfter != foundBefore {
		t.Fatalf("practice must not create a user_items row: before found=%v after found=%v", foundBefore, foundAfter)
	}
	if foundAfter && cardAfter != cardBefore {
		t.Fatalf("practice must not modify existing user_items state: before=%+v after=%+v", cardBefore, cardAfter)
	}

	practiceReviews, err := store.GetReviewsByItem(ctx, practiceItemID)
	if err != nil {
		t.Fatalf("GetReviewsByItem (practice): %v", err)
	}
	sawPracticeReview := false
	for _, r := range practiceReviews {
		if r.Practice && r.Correct {
			sawPracticeReview = true
		}
	}
	if !sawPracticeReview {
		t.Fatalf("expected a practice=true, correct review row, got %+v", practiceReviews)
	}
	if cnt, err := store.CountReviewsSince(ctx, practiceUser.ID, now.Add(-time.Hour)); err != nil || cnt != 1 {
		t.Fatalf("CountReviewsSince after practice = %d, want 1 (err=%v)", cnt, err)
	}

	// A second tap on the same practice exercise must be stale (single-use
	// guard applies to practice exercises too).
	stalePractice, err := svc.Answer(ctx, practiceUser.ID, practicePrompt.ExerciseID, practiceCorrectIdx)
	if err != nil {
		t.Fatalf("Answer (practice, stale): %v", err)
	}
	if !stalePractice.Stale {
		t.Fatalf("second tap on an already-answered practice exercise should be Stale")
	}

	// ── TopicService.Root/Children shape ─────────────────────────────────
	rootRows, err := svc.Root(ctx, user.ID)
	if err != nil {
		t.Fatalf("TopicService.Root: %v", err)
	}
	var languagesRow *telegram.TopicRow
	for i := range rootRows {
		if rootRows[i].Name == "Languages" {
			languagesRow = &rootRows[i]
		}
	}
	if languagesRow == nil {
		t.Fatalf("expected a 'Languages' root topic row, got %+v", rootRows)
	}
	if languagesRow.Total == 0 {
		t.Fatalf("expected the Languages subtree to roll up items from its descendants")
	}

	languagesView, err := svc.Children(ctx, user.ID, languagesRow.TopicID)
	if err != nil {
		t.Fatalf("TopicService.Children (Languages): %v", err)
	}
	if languagesView.IsQuizzable {
		t.Fatalf("Languages is a container, should not be quizzable")
	}
	if languagesView.ParentID != nil {
		t.Fatalf("Languages is a root topic, ParentID should be nil")
	}
	if len(languagesView.Breadcrumb) != 1 || languagesView.Breadcrumb[0].Name != "Languages" {
		t.Fatalf("unexpected breadcrumb for a root topic: %+v", languagesView.Breadcrumb)
	}
	if len(languagesView.Children) == 0 {
		t.Fatalf("expected child topics under Languages (special-characters, guess-the-language, common-words)")
	}

	var quizzableChild *telegram.TopicRow
	for i := range languagesView.Children {
		if languagesView.Children[i].IsQuizzable && languagesView.Children[i].Total > 0 {
			quizzableChild = &languagesView.Children[i]
			break
		}
	}
	if quizzableChild == nil {
		t.Fatalf("expected at least one directly-quizzable child under Languages, got %+v", languagesView.Children)
	}
	quizView, err := svc.Children(ctx, user.ID, quizzableChild.TopicID)
	if err != nil {
		t.Fatalf("TopicService.Children (quizzable): %v", err)
	}
	if !quizView.IsQuizzable {
		t.Fatalf("expected a quizzable topic view")
	}
	if len(quizView.Tiers) == 0 {
		t.Fatalf("expected per-tier rows for a quizzable topic")
	}
	if len(quizView.Breadcrumb) < 2 {
		t.Fatalf("expected a multi-level breadcrumb under Languages, got %+v", quizView.Breadcrumb)
	}
	if !quizView.Enabled {
		t.Fatalf("a fresh user's topic should default enabled (architecture §2.10)")
	}

	// ── TopicService.SetTopicEnabled: the /decks-retired-onto-/topics
	// toggle, and NextPractice respecting it ───────────────────────────
	if err := svc.SetTopicEnabled(ctx, user.ID, quizzableChild.TopicID, false); err != nil {
		t.Fatalf("SetTopicEnabled(false): %v", err)
	}
	disabledView, err := svc.Children(ctx, user.ID, quizzableChild.TopicID)
	if err != nil {
		t.Fatalf("TopicService.Children after disable: %v", err)
	}
	if disabledView.Enabled {
		t.Fatalf("expected Enabled=false after SetTopicEnabled(false)")
	}
	disabledRootRows, err := svc.Root(ctx, user.ID)
	if err != nil {
		t.Fatalf("TopicService.Root after disable: %v", err)
	}
	for _, r := range disabledRootRows {
		if r.TopicID == quizzableChild.TopicID && r.Enabled {
			t.Fatalf("expected the disabled topic's row to report Enabled=false")
		}
	}
	if err := svc.SetTopicEnabled(ctx, user.ID, quizzableChild.TopicID, true); err != nil {
		t.Fatalf("SetTopicEnabled(true): %v", err)
	}
	reenabledView, err := svc.Children(ctx, user.ID, quizzableChild.TopicID)
	if err != nil {
		t.Fatalf("TopicService.Children after re-enable: %v", err)
	}
	if !reenabledView.Enabled {
		t.Fatalf("expected Enabled=true after SetTopicEnabled(true)")
	}

	// ── Budget exhaustion: cap=1 => the second NextIntro is exhausted ────
	budgetUser, err := store.UpsertUser(ctx, 900004, "budget-tester")
	if err != nil {
		t.Fatalf("create budget user: %v", err)
	}
	if err := store.SetIntroCap(ctx, budgetUser.ID, 1); err != nil {
		t.Fatalf("SetIntroCap: %v", err)
	}
	if cap, err := store.GetIntroCap(ctx, budgetUser.ID); err != nil || cap != 1 {
		t.Fatalf("GetIntroCap after SetIntroCap(1): cap=%d err=%v", cap, err)
	}

	firstBudget, err := svc.NextIntro(ctx, budgetUser.ID)
	if err != nil {
		t.Fatalf("NextIntro (budget, first): %v", err)
	}
	if firstBudget.Reason != telegram.IntroOK {
		t.Fatalf("expected the first intro under cap=1 to succeed, got reason=%v", firstBudget.Reason)
	}
	if _, err := svc.AnswerIntro(ctx, budgetUser.ID, firstBudget.IntroID, engram.IntroGotIt); err != nil {
		t.Fatalf("AnswerIntro (budget): %v", err)
	}

	secondBudget, err := svc.NextIntro(ctx, budgetUser.ID)
	if err != nil {
		t.Fatalf("NextIntro (budget, second): %v", err)
	}
	if secondBudget.Reason != telegram.IntroBudgetExhausted {
		t.Fatalf("expected IntroBudgetExhausted on the second call under cap=1, got reason=%v", secondBudget.Reason)
	}
	if secondBudget.IntroducedToday != 1 {
		t.Fatalf("expected IntroducedToday=1, got %d", secondBudget.IntroducedToday)
	}

	// ── Gating: with only tiers {0,1} unlocked, a tier>=2 item never
	// appears in intro candidates (architecture §4.2) ───────────────────
	gatingUser, err := store.UpsertUser(ctx, 900005, "gating-tester")
	if err != nil {
		t.Fatalf("create gating user: %v", err)
	}
	gating := topics.NewService(store)
	allowed, err := gating.AllowedTiers(ctx, gatingUser.ID)
	if err != nil {
		t.Fatalf("AllowedTiers: %v", err)
	}
	if !equalIntSets(allowed, []int{0, 1}) {
		t.Fatalf("a fresh user should only have tiers {0,1} unlocked, got %v", allowed)
	}
	allowed16 := make([]int16, len(allowed))
	for i, a := range allowed {
		allowed16[i] = int16(a)
	}
	candidates, err := store.ListCandidateIntroItems(ctx, gatingUser.ID, allowed16)
	if err != nil {
		t.Fatalf("ListCandidateIntroItems: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatalf("expected candidates within tiers {0,1}")
	}
	for _, c := range candidates {
		if c.Tier >= 2 {
			t.Fatalf("candidate %s (topic %s) has tier %d, must never surface with only {0,1} unlocked", c.Key, c.TopicID, c.Tier)
		}
	}
	// Sanity: the roadside rubric DOES assign tier>=2 to some countries, so
	// the check above isn't vacuously true.
	hasHighTier := false
	for _, it := range roadsideItems {
		if it.EffectiveTier >= 2 {
			hasHighTier = true
			break
		}
	}
	if !hasHighTier {
		t.Fatalf("expected the roadside seed data to include tier>=2 items (this check would be vacuous otherwise)")
	}
}

// TestKnownOutcomeDoesNotConsumeIntroBudget guards against a regression
// where answering an intro card "I know this" (engram.IntroKnown) consumed
// the daily introduction budget even though the item was never actually
// introduced to the user. With daily_intro_cap=1: answering the first card
// as IntroKnown must leave the budget untouched (the second NextIntro call
// still succeeds), while a genuine IntroGotIt answer DOES spend it (the
// third NextIntro call is budget-exhausted).
func TestKnownOutcomeDoesNotConsumeIntroBudget(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	registerGenerators(store)
	seedAllTopics(t, ctx, store)

	sched := engram.NewScheduler()
	svc := study.New(store, sched, study.GlobalRegistry, nil, 7)

	user, err := store.UpsertUser(ctx, 900020, "known-budget-tester")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := store.SetIntroCap(ctx, user.ID, 1); err != nil {
		t.Fatalf("SetIntroCap: %v", err)
	}

	known, err := svc.NextIntro(ctx, user.ID)
	if err != nil {
		t.Fatalf("NextIntro (first): %v", err)
	}
	if known.Reason != telegram.IntroOK {
		t.Fatalf("expected IntroOK for the first card, got reason=%v", known.Reason)
	}
	if _, err := svc.AnswerIntro(ctx, user.ID, known.IntroID, engram.IntroKnown); err != nil {
		t.Fatalf("AnswerIntro (known): %v", err)
	}

	_, budgetLeftAfterKnown, err := svc.IntroSummary(ctx, user.ID)
	if err != nil {
		t.Fatalf("IntroSummary after known: %v", err)
	}
	if budgetLeftAfterKnown != 1 {
		t.Fatalf("a known-outcome introduction must not consume the daily budget: expected budgetLeft=1, got %d", budgetLeftAfterKnown)
	}

	second, err := svc.NextIntro(ctx, user.ID)
	if err != nil {
		t.Fatalf("NextIntro (second, after known): %v", err)
	}
	if second.Reason != telegram.IntroOK {
		t.Fatalf("expected the second card still available under cap=1 (known must not spend budget), got reason=%v", second.Reason)
	}
	if second.ItemID == known.ItemID {
		t.Fatalf("expected a different item for the second card")
	}

	// A genuine "got it" DOES spend the budget: the next NextIntro call must
	// be budget-exhausted under cap=1.
	if _, err := svc.AnswerIntro(ctx, user.ID, second.IntroID, engram.IntroGotIt); err != nil {
		t.Fatalf("AnswerIntro (got_it): %v", err)
	}
	third, err := svc.NextIntro(ctx, user.ID)
	if err != nil {
		t.Fatalf("NextIntro (third, after got_it): %v", err)
	}
	if third.Reason != telegram.IntroBudgetExhausted {
		t.Fatalf("expected IntroBudgetExhausted after a genuine introduction under cap=1, got reason=%v", third.Reason)
	}
}
