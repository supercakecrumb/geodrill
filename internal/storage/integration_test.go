package storage_test

// Integration tests against a real PostgreSQL 18. They are skipped unless
// GEODRILL_TEST_DATABASE_URL is set (so `go test ./...` stays green without
// docker).
//
// WARNING: these tests DROP EVERY TABLE (they exercise the down migrations), so
// the target MUST be a disposable database — never the one your bot runs on.
// The DSN's database name must contain "test" (see testDSN) as a safety fuse.
// Example:
//
//	GEODRILL_TEST_DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill_test?sslmode=disable' \
//	  go test ./internal/storage/...

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run storage integration tests")
	}
	// Safety fuse: these tests drop every table. Refuse to run against anything
	// that isn't obviously a throwaway test database, so a stray DSN can never
	// wipe the live bot's data. The database name must contain "test".
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

	// topic + items (replaces the legacy deck/skill setup)
	topic, err := st.UpsertTopic(ctx, nil, "romance", "Romance languages", 0, 0, "language_id", []string{"single"}, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert topic: %v", err)
	}
	spa, err := st.UpsertItem(ctx, topic.ID, "spa", "Spanish", nil, []byte(`{}`), nil, 0, true)
	if err != nil {
		t.Fatalf("upsert item spa: %v", err)
	}
	por, err := st.UpsertItem(ctx, topic.ID, "por", "Portuguese", nil, []byte(`{}`), nil, 1, true)
	if err != nil || por.Key != "por" {
		t.Fatalf("upsert item por: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)

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
	exID, _, err := st.InsertExercise(ctx, storage.InsertExerciseParams{
		UserID:    u.ID,
		ItemID:    spa.ID,
		ContentID: &c.ID,
		Options:   []byte(`[{"key":"spa","label":"Spanish"},{"key":"por","label":"Portuguese"}]`),
	})
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
		UserID: u.ID, ItemID: spa.ID, ExerciseID: &exID, ContentID: &c.ID,
		Chosen: "por", CorrectAnswer: "spa", Correct: false, Rating: 1, ResponseMS: &ms,
		StabilityBefore: 1, DifficultyBefore: 5, StabilityAfter: 0.5, DifficultyAfter: 5.2,
		StateBefore: 0, ScheduledDays: 0, ElapsedDays: 0, ReviewedAt: now,
	}); err != nil {
		t.Fatalf("insert review: %v", err)
	}
	recs, err := st.ListReviewsSince(ctx, u.ID, now.Add(-time.Hour))
	if err != nil || len(recs) != 1 {
		t.Fatalf("list reviews = %d, %v", len(recs), err)
	}
	if recs[0].CorrectAnswer != "spa" || recs[0].Chosen != "por" || recs[0].Correct {
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

// ── schema (architecture §2): topics, items, user_items, introductions,
// countries/facts, tier progress ────────────────────────────────────────

func TestTopicTree(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	root, err := st.UpsertTopic(ctx, nil, "languages", "Languages", 0, 0, "container", []string{"single"}, false, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert root topic: %v", err)
	}
	if root.ParentID != nil {
		t.Fatalf("root topic should have nil parent")
	}

	// idempotent upsert (ON CONFLICT ... DO UPDATE)
	root2, err := st.UpsertTopic(ctx, nil, "languages", "Languages Updated", 0, 0, "container", []string{"single"}, false, []byte(`{}`))
	if err != nil || root2.ID != root.ID || root2.Name != "Languages Updated" {
		t.Fatalf("upsert root topic not idempotent: err=%v id1=%s id2=%s name=%s", err, root.ID, root2.ID, root2.Name)
	}

	parentID := root.ID
	child, err := st.UpsertTopic(ctx, &parentID, "special-characters", "Special characters", 0, 1, "char_language", []string{"single", "text"}, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert child topic: %v", err)
	}
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatalf("child topic parent mismatch: %+v", child)
	}

	other, err := st.UpsertTopic(ctx, nil, "roads", "Roads", 1, 0, "container", nil, false, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert other root topic: %v", err)
	}

	children, err := st.ListChildTopics(ctx, root.ID)
	if err != nil || len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("list child topics = %+v, %v", children, err)
	}

	roots, err := st.ListRootTopics(ctx)
	if err != nil || len(roots) != 2 {
		t.Fatalf("list root topics = %d, %v", len(roots), err)
	}

	all, err := st.ListAllTopics(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("list all topics = %d, %v", len(all), err)
	}

	// topic_paths recursive view
	path, depth, found, err := st.GetTopicPath(ctx, child.ID)
	if err != nil || !found {
		t.Fatalf("get topic path: found=%v err=%v", found, err)
	}
	if path != "languages/special-characters" || depth != 1 {
		t.Fatalf("topic path mismatch: path=%q depth=%d", path, depth)
	}

	byPath, found, err := st.GetTopicByPath(ctx, "languages/special-characters")
	if err != nil || !found || byPath.ID != child.ID {
		t.Fatalf("get topic by path: found=%v err=%v id=%s", found, err, byPath.ID)
	}

	// reparent: a single-row UPDATE (architecture §2.1), topic_paths follows.
	otherID := other.ID
	if err := st.ReparentTopic(ctx, child.ID, &otherID); err != nil {
		t.Fatalf("reparent topic: %v", err)
	}
	path2, _, found, err := st.GetTopicPath(ctx, child.ID)
	if err != nil || !found || path2 != "roads/special-characters" {
		t.Fatalf("path after reparent = %q found=%v err=%v", path2, found, err)
	}

	// reparent back to root (nil parent)
	if err := st.ReparentTopic(ctx, child.ID, nil); err != nil {
		t.Fatalf("reparent to root: %v", err)
	}
	reRooted, found, err := st.GetTopicByID(ctx, child.ID)
	if err != nil || !found || reRooted.ParentID != nil {
		t.Fatalf("reparent to root failed: %+v found=%v err=%v", reRooted, found, err)
	}

	// user_topics opt-in/out (default-on when no row exists)
	u, err := st.UpsertUser(ctx, 501, "topictester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	if err := st.SetUserTopicEnabled(ctx, u.ID, root.ID, false); err != nil {
		t.Fatalf("set user topic enabled: %v", err)
	}
	uts, err := st.ListUserTopics(ctx, u.ID)
	if err != nil {
		t.Fatalf("list user topics: %v", err)
	}
	foundDisabled := false
	for _, ut := range uts {
		if ut.ID == root.ID {
			if ut.Enabled {
				t.Fatalf("root topic should be disabled for user")
			}
			foundDisabled = true
		} else if !ut.Enabled {
			t.Fatalf("topic %s should default-enabled with no user_topics row", ut.Slug)
		}
	}
	if !foundDisabled {
		t.Fatalf("did not find root topic in ListUserTopics")
	}
}

func TestItemEffectiveTier(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	topic, err := st.UpsertTopic(ctx, nil, "chars", "Special characters", 0, 2, "char_language", nil, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert topic: %v", err)
	}

	// inherits topics.base_tier (2)
	inherited, err := st.UpsertItem(ctx, topic.ID, "cyr", "Cyrillic script", nil, []byte(`{}`), nil, 0, true)
	if err != nil {
		t.Fatalf("upsert inherited item: %v", err)
	}
	// per-item override to tier 4
	overrideTier := int16(4)
	overridden, err := st.UpsertItem(ctx, topic.ID, "obscure-diacritic", "obscure diacritic", &overrideTier, []byte(`{}`), nil, 1, true)
	if err != nil {
		t.Fatalf("upsert overridden item: %v", err)
	}

	tier, err := st.GetItemEffectiveTier(ctx, inherited.ID)
	if err != nil || tier != 2 {
		t.Fatalf("inherited effective tier = %d, %v (want 2)", tier, err)
	}
	tier2, err := st.GetItemEffectiveTier(ctx, overridden.ID)
	if err != nil || tier2 != 4 {
		t.Fatalf("overridden effective tier = %d, %v (want 4)", tier2, err)
	}

	withTiers, err := st.ListItemsWithTierByTopic(ctx, topic.ID)
	if err != nil || len(withTiers) != 2 {
		t.Fatalf("list items with tier = %d, %v", len(withTiers), err)
	}
	for _, it := range withTiers {
		switch it.Key {
		case "cyr":
			if it.EffectiveTier != 2 || it.Tier != nil {
				t.Fatalf("cyr mismatch: %+v", it)
			}
		case "obscure-diacritic":
			if it.EffectiveTier != 4 || it.Tier == nil || *it.Tier != 4 {
				t.Fatalf("override mismatch: %+v", it)
			}
		}
	}

	active, err := st.ListActiveItemsByTopic(ctx, topic.ID)
	if err != nil || len(active) != 2 {
		t.Fatalf("active items = %d, %v", len(active), err)
	}
}

func TestUserItemLifecycle(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	u, err := st.UpsertUser(ctx, 601, "lifecycle-tester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	topic, err := st.UpsertTopic(ctx, nil, "words", "Common words", 0, 0, "word_language", nil, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert topic: %v", err)
	}
	item, err := st.UpsertItem(ctx, topic.ID, "ulica", "street (pol)", nil, []byte(`{}`), nil, 0, true)
	if err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	// absence of a row == implicitly new (architecture §2.3)
	_, found, err := st.GetUserItem(ctx, u.ID, item.ID)
	if err != nil || found {
		t.Fatalf("expected no user_item row: found=%v err=%v", found, err)
	}

	now := time.Now().UTC().Truncate(time.Second)

	// Introduce(IntroGotIt): new -> introduced, fresh card
	card := storage.CardFields{Due: now, State: 0}
	if err := st.PutUserItem(ctx, u.ID, item.ID, 1, card, now, time.Time{}); err != nil {
		t.Fatalf("put user item (introduced): %v", err)
	}
	ui, found, err := st.GetUserItem(ctx, u.ID, item.ID)
	if err != nil || !found || ui.Lifecycle != 1 {
		t.Fatalf("get user item after introduce: %+v found=%v err=%v", ui, found, err)
	}
	if ui.IntroducedAt.IsZero() {
		t.Fatalf("introduced_at should be set")
	}

	// graduate -> reviewing with a due card
	card2 := storage.CardFields{Due: now.Add(48 * time.Hour), Stability: 25, Difficulty: 5, Reps: 3, State: 2, LastReview: now}
	if err := st.PutUserItem(ctx, u.ID, item.ID, 2, card2, ui.IntroducedAt, time.Time{}); err != nil {
		t.Fatalf("put user item (reviewing): %v", err)
	}

	reviewing, err := st.ListUserItemsByLifecycle(ctx, u.ID, 2)
	if err != nil || len(reviewing) != 1 || reviewing[0].ItemID != item.ID {
		t.Fatalf("list by lifecycle=reviewing: %+v, %v", reviewing, err)
	}

	due, err := st.ListDueUserItems(ctx, u.ID, now.Add(72*time.Hour))
	if err != nil || len(due) != 1 || due[0].Key != "ulica" {
		t.Fatalf("list due user items: %+v, %v", due, err)
	}
	notYetDue, err := st.ListDueUserItems(ctx, u.ID, now)
	if err != nil || len(notYetDue) != 0 {
		t.Fatalf("list due user items too early: %+v, %v", notYetDue, err)
	}

	// candidate intro items: a fresh item in an unlocked tier should show up;
	// the already-introduced item must not (lifecycle no longer new).
	item2, err := st.UpsertItem(ctx, topic.ID, "droga", "road (pol)", nil, []byte(`{}`), nil, 1, true)
	if err != nil {
		t.Fatalf("upsert item2: %v", err)
	}
	candidates, err := st.ListCandidateIntroItems(ctx, u.ID, []int16{0, 1})
	if err != nil {
		t.Fatalf("list candidate intro items: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ItemID != item2.ID {
		t.Fatalf("candidates = %+v, want only item2", candidates)
	}
	// locked tier (2) excludes everything even though item2 is tier 0
	locked, err := st.ListCandidateIntroItems(ctx, u.ID, []int16{2})
	if err != nil || len(locked) != 0 {
		t.Fatalf("candidates for locked tier = %+v, %v", locked, err)
	}

	// Introduce(IntroKnown): mark item known -> terminal, no active card
	if err := st.PutUserItem(ctx, u.ID, item.ID, 3, storage.CardFields{}, ui.IntroducedAt, now); err != nil {
		t.Fatalf("put user item (known): %v", err)
	}
	known, err := st.ListUserItemsByLifecycle(ctx, u.ID, 3)
	if err != nil || len(known) != 1 {
		t.Fatalf("list known: %+v, %v", known, err)
	}
	if known[0].KnownAt.IsZero() {
		t.Fatalf("known_at should be set")
	}
}

func TestIntroductions(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	u, err := st.UpsertUser(ctx, 701, "intro-tester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	topic, err := st.UpsertTopic(ctx, nil, "flags", "Flags", 0, 0, "container", nil, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert topic: %v", err)
	}
	item, err := st.UpsertItem(ctx, topic.ID, "FR", "France", nil, []byte(`{}`), nil, 0, true)
	if err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	seq, err := st.NextIntroSeq(ctx, u.ID, item.ID)
	if err != nil || seq != 1 {
		t.Fatalf("next intro seq = %d, %v (want 1)", seq, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	intro, err := st.InsertIntroduction(ctx, u.ID, item.ID, seq, now)
	if err != nil {
		t.Fatalf("insert introduction: %v", err)
	}
	if intro.Outcome != nil {
		t.Fatalf("fresh introduction should have nil outcome, got %+v", intro)
	}

	open, found, err := st.GetLatestOpenIntroductionForItem(ctx, u.ID, item.ID)
	if err != nil || !found || open.ID != intro.ID {
		t.Fatalf("get latest open introduction for item: found=%v err=%v", found, err)
	}
	openAny, found, err := st.GetLatestOpenIntroduction(ctx, u.ID)
	if err != nil || !found || openAny.ID != intro.ID {
		t.Fatalf("get latest open introduction: found=%v err=%v", found, err)
	}

	answered, err := st.AnswerIntroduction(ctx, intro.ID, 0, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("answer introduction: %v", err)
	}
	if answered.Outcome == nil || *answered.Outcome != 0 {
		t.Fatalf("answered outcome mismatch: %+v", answered)
	}

	// no longer open once answered
	_, found, err = st.GetLatestOpenIntroductionForItem(ctx, u.ID, item.ID)
	if err != nil || found {
		t.Fatalf("introduction should no longer be open: found=%v err=%v", found, err)
	}

	// "introduced today" count over the local-day [from, to) bounds the caller supplies
	dayStart := now.Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)
	cnt, err := st.CountIntroductionsToday(ctx, u.ID, dayStart, dayEnd)
	if err != nil || cnt != 1 {
		t.Fatalf("count introductions today = %d, %v", cnt, err)
	}
	cntTomorrow, err := st.CountIntroductionsToday(ctx, u.ID, dayEnd, dayEnd.Add(24*time.Hour))
	if err != nil || cntTomorrow != 0 {
		t.Fatalf("count introductions tomorrow = %d, %v", cntTomorrow, err)
	}

	// re-view: seq increments
	seq2, err := st.NextIntroSeq(ctx, u.ID, item.ID)
	if err != nil || seq2 != 2 {
		t.Fatalf("next intro seq after answer = %d, %v (want 2)", seq2, err)
	}

	if err := st.SetIntroductionMessageID(ctx, intro.ID, 4242); err != nil {
		t.Fatalf("set introduction message id: %v", err)
	}
}

// TestCountIntroductionsToday_ExcludesKnownOutcome guards against a
// regression where "I know this" (outcome=1, engram.IntroKnown) consumed
// the daily intro budget: that outcome never actually introduces the item,
// so CountIntroductionsToday must not count it, while a genuine
// first-exposure outcome (e.g. "got it") still must.
func TestCountIntroductionsToday_ExcludesKnownOutcome(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	u, err := st.UpsertUser(ctx, 702, "known-budget-tester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	topic, err := st.UpsertTopic(ctx, nil, "flags2", "Flags2", 0, 0, "container", nil, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert topic: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	dayStart := now.Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)

	// Item A: answered "I know this" (outcome=1) — must NOT count toward the
	// daily intro budget.
	itemA, err := st.UpsertItem(ctx, topic.ID, "DE", "Germany", nil, []byte(`{}`), nil, 0, true)
	if err != nil {
		t.Fatalf("upsert item A: %v", err)
	}
	seqA, err := st.NextIntroSeq(ctx, u.ID, itemA.ID)
	if err != nil {
		t.Fatalf("next intro seq A: %v", err)
	}
	introA, err := st.InsertIntroduction(ctx, u.ID, itemA.ID, seqA, now)
	if err != nil {
		t.Fatalf("insert introduction A: %v", err)
	}
	if _, err := st.AnswerIntroduction(ctx, introA.ID, 1, now.Add(time.Minute)); err != nil {
		t.Fatalf("answer introduction A (known): %v", err)
	}
	cntAfterKnown, err := st.CountIntroductionsToday(ctx, u.ID, dayStart, dayEnd)
	if err != nil {
		t.Fatalf("count after known: %v", err)
	}
	if cntAfterKnown != 0 {
		t.Fatalf("a known-outcome introduction must not count toward the daily budget, got %d", cntAfterKnown)
	}

	// Item B: answered "got it" (outcome=0) — a genuine introduction, must count.
	itemB, err := st.UpsertItem(ctx, topic.ID, "IT", "Italy", nil, []byte(`{}`), nil, 1, true)
	if err != nil {
		t.Fatalf("upsert item B: %v", err)
	}
	seqB, err := st.NextIntroSeq(ctx, u.ID, itemB.ID)
	if err != nil {
		t.Fatalf("next intro seq B: %v", err)
	}
	introB, err := st.InsertIntroduction(ctx, u.ID, itemB.ID, seqB, now)
	if err != nil {
		t.Fatalf("insert introduction B: %v", err)
	}
	if _, err := st.AnswerIntroduction(ctx, introB.ID, 0, now.Add(time.Minute)); err != nil {
		t.Fatalf("answer introduction B (got_it): %v", err)
	}
	cntAfterGotIt, err := st.CountIntroductionsToday(ctx, u.ID, dayStart, dayEnd)
	if err != nil {
		t.Fatalf("count after got_it: %v", err)
	}
	if cntAfterGotIt != 1 {
		t.Fatalf("a genuinely-introduced item must count toward the daily budget, got %d", cntAfterGotIt)
	}
}

func TestCountriesFacts(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	moro, err := st.UpsertCountry(ctx, storage.Country{ISOA2: "MA", ISOA3: "MAR", Name: "Morocco", UNMember: true, GGCoverage: true})
	if err != nil {
		t.Fatalf("upsert Morocco: %v", err)
	}
	fra, err := st.UpsertCountry(ctx, storage.Country{ISOA2: "FR", ISOA3: "FRA", Name: "France", UNMember: true, GGCoverage: true})
	if err != nil {
		t.Fatalf("upsert France: %v", err)
	}
	// idempotent upsert on iso_a2
	moro2, err := st.UpsertCountry(ctx, storage.Country{ISOA2: "MA", ISOA3: "MAR", Name: "Kingdom of Morocco", UNMember: true, GGCoverage: true})
	if err != nil || moro2.ID != moro.ID || moro2.Name != "Kingdom of Morocco" {
		t.Fatalf("upsert country not idempotent: %v moro2=%+v", err, moro2)
	}

	byISO, found, err := st.GetCountryByISO(ctx, "FR")
	if err != nil || !found || byISO.ID != fra.ID {
		t.Fatalf("get country by iso: found=%v err=%v", found, err)
	}
	byISOA3, found, err := st.GetCountryByISOA3(ctx, "MAR")
	if err != nil || !found || byISOA3.ID != moro.ID {
		t.Fatalf("get country by iso a3: found=%v err=%v", found, err)
	}

	all, err := st.ListCountries(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list countries = %d, %v", len(all), err)
	}
	byFlags, err := st.ListCountriesByFlags(ctx, true, true)
	if err != nil || len(byFlags) != 2 {
		t.Fatalf("list countries by flags = %d, %v", len(byFlags), err)
	}

	drivesOn, err := st.UpsertFactDef(ctx, "drives_on", "Drives on", "text", "", "single", "baseline")
	if err != nil {
		t.Fatalf("upsert drives_on fact def: %v", err)
	}
	religion, err := st.UpsertFactDef(ctx, "main_religion", "Main religion", "text", "", "single", "baseline")
	if err != nil {
		t.Fatalf("upsert main_religion fact def: %v", err)
	}

	left, right, islam, catholic := "left", "right", "Islam", "Roman Catholicism"
	if _, err := st.InsertCountryFact(ctx, moro.ID, drivesOn.ID, &left, nil, nil, "baseline", time.Time{}); err != nil {
		t.Fatalf("insert morocco drives_on: %v", err)
	}
	if _, err := st.InsertCountryFact(ctx, moro.ID, religion.ID, &islam, nil, nil, "baseline", time.Time{}); err != nil {
		t.Fatalf("insert morocco religion: %v", err)
	}
	if _, err := st.InsertCountryFact(ctx, fra.ID, drivesOn.ID, &right, nil, nil, "baseline", time.Time{}); err != nil {
		t.Fatalf("insert france drives_on: %v", err)
	}
	if _, err := st.InsertCountryFact(ctx, fra.ID, religion.ID, &catholic, nil, nil, "baseline", time.Time{}); err != nil {
		t.Fatalf("insert france religion: %v", err)
	}

	byDef, err := st.ListCountryFactsByDefKey(ctx, "drives_on")
	if err != nil || len(byDef) != 2 {
		t.Fatalf("list facts by def key = %d, %v", len(byDef), err)
	}

	forMorocco, err := st.ListFactsForCountry(ctx, moro.ID)
	if err != nil || len(forMorocco) != 2 {
		t.Fatalf("list facts for morocco = %d, %v", len(forMorocco), err)
	}
	for _, f := range forMorocco {
		if f.FactKey == "drives_on" && (f.ValText == nil || *f.ValText != "left") {
			t.Fatalf("morocco drives_on mismatch: %+v", f)
		}
	}

	// Arbitrary-filter join (architecture §2.7): "drive on the left AND Islam
	// main religion" is plain SQL over countries/country_facts/fact_defs — no
	// dedicated Store method needed, the schema just supports it directly.
	rows, err := st.Pool().Query(ctx, `
		SELECT c.name FROM countries c
		JOIN country_facts fd ON fd.country_id=c.id JOIN fact_defs dd ON dd.id=fd.fact_def_id AND dd.key='drives_on'
		JOIN country_facts fr ON fr.country_id=c.id JOIN fact_defs dr ON dr.id=fr.fact_def_id AND dr.key='main_religion'
		WHERE fd.val_text=$1 AND fr.val_text=$2`, left, islam)
	if err != nil {
		t.Fatalf("arbitrary filter join: %v", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		t.Fatalf("rows err: %v", rowsErr)
	}
	if len(names) != 1 || names[0] != "Kingdom of Morocco" {
		t.Fatalf("arbitrary filter join result = %v, want [Kingdom of Morocco]", names)
	}

	// CHECK constraint: exactly one of val_text/val_num/val_bool must be set.
	if _, err := st.InsertCountryFact(ctx, fra.ID, drivesOn.ID, nil, nil, nil, "baseline", time.Time{}); err == nil {
		t.Fatalf("expected CHECK violation when no typed value is set")
	}

	// wholesale replace of a multi-valued fact
	if err := st.DeleteCountryFactsByDef(ctx, fra.ID, religion.ID); err != nil {
		t.Fatalf("delete country facts by def: %v", err)
	}
	afterDelete, err := st.ListFactsForCountry(ctx, fra.ID)
	if err != nil || len(afterDelete) != 1 {
		t.Fatalf("facts for france after delete = %d, %v", len(afterDelete), err)
	}
}

func TestMediaFiles(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	width, height, byteSize := 800, 600, 123456
	m, err := st.PutMediaFile(ctx, nil, "photos/fr/roundabout1.jpg", "abc123", &width, &height, &byteSize)
	if err != nil {
		t.Fatalf("put media file: %v", err)
	}
	if m.Width != 800 || m.Height != 600 || m.Bytes != 123456 || m.SHA256 != "abc123" {
		t.Fatalf("media dimensions mismatch: %+v", m)
	}
	if m.ContentID != nil {
		t.Fatalf("content id should be nil (unlinked)")
	}

	byPath, found, err := st.GetMediaByLocalPath(ctx, "photos/fr/roundabout1.jpg")
	if err != nil || !found || byPath.ID != m.ID {
		t.Fatalf("get media by local path: found=%v err=%v", found, err)
	}

	if err := st.SetMediaTelegramFileID(ctx, m.ID, "AgACAgQAAx"); err != nil {
		t.Fatalf("set telegram file id: %v", err)
	}
	updated, found, err := st.GetMediaByLocalPath(ctx, "photos/fr/roundabout1.jpg")
	if err != nil || !found || updated.TelegramFileID != "AgACAgQAAx" {
		t.Fatalf("telegram file id not persisted: %+v", updated)
	}

	// idempotent upsert on local_path; telegram_file_id survives (not in SET clause)
	width2 := 900
	m2, err := st.PutMediaFile(ctx, nil, "photos/fr/roundabout1.jpg", "abc123", &width2, &height, &byteSize)
	if err != nil || m2.ID != m.ID || m2.Width != 900 {
		t.Fatalf("put media file not idempotent: %v m2=%+v", err, m2)
	}
	if m2.TelegramFileID != "AgACAgQAAx" {
		t.Fatalf("telegram file id should survive upsert: got %q", m2.TelegramFileID)
	}
}

func TestTierProgress(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	u, err := st.UpsertUser(ctx, 801, "tier-tester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	topic, err := st.UpsertTopic(ctx, nil, "roadside", "Road side", 0, 1, "road_side", nil, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert topic: %v", err)
	}

	// 4 items, all at tier 1 (inherited base_tier)
	items := make([]storage.Item, 0, 4)
	for i, key := range []string{"a", "b", "c", "d"} {
		it, err := st.UpsertItem(ctx, topic.ID, key, key, nil, []byte(`{}`), nil, i, true)
		if err != nil {
			t.Fatalf("upsert item %s: %v", key, err)
		}
		items = append(items, it)
	}

	now := time.Now().UTC().Truncate(time.Second)
	// a: known -> good shape (§4.1: lifecycle=known counts regardless of card)
	if err := st.PutUserItem(ctx, u.ID, items[0].ID, 3, storage.CardFields{}, now, now); err != nil {
		t.Fatalf("put item a: %v", err)
	}
	// b: reviewing, graduated + durable (state=Review, stability>=21d) -> good shape
	if err := st.PutUserItem(ctx, u.ID, items[1].ID, 2, storage.CardFields{Due: now, Stability: 25, State: 2, LastReview: now}, now, time.Time{}); err != nil {
		t.Fatalf("put item b: %v", err)
	}
	// c: reviewing but not durable yet (stability<21) -> introduced, not good shape
	if err := st.PutUserItem(ctx, u.ID, items[2].ID, 2, storage.CardFields{Due: now, Stability: 5, State: 2, LastReview: now}, now, time.Time{}); err != nil {
		t.Fatalf("put item c: %v", err)
	}
	// d: left as new (no user_items row)

	rows, err := st.RecomputeTierProgress(ctx, u.ID)
	if err != nil {
		t.Fatalf("recompute tier progress: %v", err)
	}
	var tier1 *storage.TierProgress
	for i := range rows {
		if rows[i].Tier == 1 {
			tier1 = &rows[i]
		}
	}
	if tier1 == nil {
		t.Fatalf("no tier=1 row in recompute: %+v", rows)
	}
	if tier1.TotalItems != 4 || tier1.IntroducedItems != 3 || tier1.GoodShapeItems != 2 {
		t.Fatalf("tier1 progress mismatch: %+v (want total=4 introduced=3 good=2)", tier1)
	}

	single, found, err := st.RecomputeTierProgressForTier(ctx, u.ID, 1)
	if err != nil || !found {
		t.Fatalf("recompute tier progress for tier: found=%v err=%v", found, err)
	}
	if single.TotalItems != 4 || single.IntroducedItems != 3 || single.GoodShapeItems != 2 {
		t.Fatalf("single-tier recompute mismatch: %+v", single)
	}

	_, found, err = st.RecomputeTierProgressForTier(ctx, u.ID, 9)
	if err != nil {
		t.Fatalf("recompute empty tier: %v", err)
	}
	if found {
		t.Fatalf("tier 9 has no items, should not be found")
	}

	// cache write + read-back (tier-complete policy lives in the caller, §4.1:
	// introduced==total AND good_shape/total >= 80%)
	single.Complete = single.IntroducedItems == single.TotalItems && single.GoodShapeItems*100 >= single.TotalItems*80
	if err := st.UpsertTierProgress(ctx, single); err != nil {
		t.Fatalf("upsert tier progress: %v", err)
	}
	cached, err := st.ListTierProgressForUser(ctx, u.ID)
	if err != nil || len(cached) != 1 || cached[0].Tier != 1 {
		t.Fatalf("list tier progress for user: %+v, %v", cached, err)
	}
	if cached[0].Complete {
		t.Fatalf("tier1 should not be complete (only 3/4 introduced)")
	}
}

func TestWithTxCommitAndRollback(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	u, err := st.UpsertUser(ctx, 901, "tx-tester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	topic, err := st.UpsertTopic(ctx, nil, "tx-topic", "Tx Topic", 0, 0, "container", nil, true, []byte(`{}`))
	if err != nil {
		t.Fatalf("upsert topic: %v", err)
	}
	item, err := st.UpsertItem(ctx, topic.ID, "k", "K", nil, []byte(`{}`), nil, 0, true)
	if err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	putParams := func(lifecycle int16) db.PutUserItemParams {
		return db.PutUserItemParams{
			UserID:       u.ID,
			ItemID:       item.ID,
			Lifecycle:    lifecycle,
			Due:          pgtype.Timestamptz{Time: now, Valid: true},
			IntroducedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}
	}

	// commit path: two writes inside one tx both land together (architecture
	// §5.5: user_items upsert + introductions insert in the same transaction).
	if err := st.WithTx(ctx, func(q *db.Queries) error {
		if err := q.PutUserItem(ctx, putParams(1)); err != nil {
			return err
		}
		_, err := q.InsertIntroduction(ctx, db.InsertIntroductionParams{
			UserID:  u.ID,
			ItemID:  item.ID,
			Seq:     1,
			ShownAt: pgtype.Timestamptz{Time: now, Valid: true},
		})
		return err
	}); err != nil {
		t.Fatalf("WithTx commit path: %v", err)
	}
	ui, found, err := st.GetUserItem(ctx, u.ID, item.ID)
	if err != nil || !found || ui.Lifecycle != 1 {
		t.Fatalf("commit path did not persist: found=%v err=%v ui=%+v", found, err, ui)
	}

	// rollback path: the second write fails deliberately (sentinel error) — the
	// first write inside the SAME transaction must not be visible afterward.
	sentinel := errors.New("boom")
	txErr := st.WithTx(ctx, func(q *db.Queries) error {
		if err := q.PutUserItem(ctx, putParams(3)); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(txErr, sentinel) {
		t.Fatalf("expected sentinel error from WithTx, got %v", txErr)
	}
	uiAfter, found, err := st.GetUserItem(ctx, u.ID, item.ID)
	if err != nil || !found || uiAfter.Lifecycle != 1 {
		t.Fatalf("rollback did not revert: found=%v err=%v ui=%+v (want lifecycle still 1)", found, err, uiAfter)
	}
}

// TestGameStats covers the game zone's aggregate persistence
// (vibe/design-game-zone.md "Persistence"): RecordGameRun upserts one row
// per (user, game) key, best_streak only ever grows, runs increments by
// one per call, and GetGameStats reads it back — plus the "never played"
// not-found case.
func TestGameStats(t *testing.T) {
	dsn := testDSN(t)
	freshSchema(t, dsn)

	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	u, err := st.UpsertUser(ctx, 1001, "game-stats-tester")
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	const gameKey = "language_roulette"

	// Never played: not found.
	_, found, err := st.GetGameStats(ctx, u.ID, gameKey)
	if err != nil {
		t.Fatalf("get game stats (before any run): %v", err)
	}
	if found {
		t.Fatalf("expected no game_stats row before the first run")
	}

	t1 := time.Now().UTC().Truncate(time.Second)
	first, err := st.RecordGameRun(ctx, u.ID, gameKey, 5, t1)
	if err != nil {
		t.Fatalf("record game run (1): %v", err)
	}
	if first.BestStreak != 5 || first.Runs != 1 || !first.LastPlayedAt.Equal(t1) {
		t.Fatalf("first run mismatch: %+v", first)
	}

	// A lower streak on a second run must not lower best_streak, but runs
	// still increments and last_played_at still advances.
	t2 := t1.Add(time.Hour)
	second, err := st.RecordGameRun(ctx, u.ID, gameKey, 3, t2)
	if err != nil {
		t.Fatalf("record game run (2): %v", err)
	}
	if second.BestStreak != 5 {
		t.Fatalf("expected best_streak to stay at 5 after a lower-streak run, got %d", second.BestStreak)
	}
	if second.Runs != 2 {
		t.Fatalf("expected runs=2 after a second run, got %d", second.Runs)
	}
	if !second.LastPlayedAt.Equal(t2) {
		t.Fatalf("expected last_played_at stamped to the second run, got %v", second.LastPlayedAt)
	}

	// A higher streak on a third run DOES raise best_streak.
	t3 := t2.Add(time.Hour)
	third, err := st.RecordGameRun(ctx, u.ID, gameKey, 9, t3)
	if err != nil {
		t.Fatalf("record game run (3): %v", err)
	}
	if third.BestStreak != 9 || third.Runs != 3 {
		t.Fatalf("third run mismatch: %+v", third)
	}

	got, found, err := st.GetGameStats(ctx, u.ID, gameKey)
	if err != nil || !found {
		t.Fatalf("get game stats: found=%v err=%v", found, err)
	}
	if got.BestStreak != 9 || got.Runs != 3 || got.Game != gameKey || got.UserID != u.ID {
		t.Fatalf("get game stats mismatch: %+v", got)
	}

	// A different game key for the same user is a separate row.
	_, found, err = st.GetGameStats(ctx, u.ID, "some_other_game")
	if err != nil {
		t.Fatalf("get game stats (other game): %v", err)
	}
	if found {
		t.Fatalf("expected no row for an unplayed game key")
	}
}
