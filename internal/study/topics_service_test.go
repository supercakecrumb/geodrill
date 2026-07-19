package study

import (
	"context"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

func TestLowestLockedTier(t *testing.T) {
	allowed := map[int16]bool{0: true, 1: true}

	anyLocked, locked := lowestLockedTier([]int16{0, 1}, allowed)
	if anyLocked {
		t.Fatalf("all tiers allowed: expected anyLocked=false, got locked=%d", locked)
	}

	anyLocked2, locked2 := lowestLockedTier([]int16{0, 2, 3}, allowed)
	if !anyLocked2 || locked2 != 2 {
		t.Fatalf("expected anyLocked=true lockedTier=2, got anyLocked=%v lockedTier=%d", anyLocked2, locked2)
	}

	anyLocked3, locked3 := lowestLockedTier(nil, allowed)
	if anyLocked3 {
		t.Fatalf("no tiers used: expected anyLocked=false, got locked=%d", locked3)
	}
}

// fakeGenerator is a minimal topics.Generator for hasTips tests — it does
// NOT implement topics.TipProvider (no Tips() method), unlike
// fakeTippedGenerator below. Interface satisfaction in Go is structural, so
// "capable of tips" has to be a distinct type, not a field/flag.
type fakeGenerator struct{ kind string }

func (g fakeGenerator) Kind() string { return g.kind }
func (g fakeGenerator) BuildExercise(context.Context, *rand.Rand, topics.ExerciseRequest) (topics.Exercise, error) {
	return topics.Exercise{}, nil
}
func (g fakeGenerator) BuildIntro(context.Context, storage.Item) (topics.IntroCard, error) {
	return topics.IntroCard{}, nil
}

var _ topics.Generator = fakeGenerator{}

// fakeTippedGenerator additionally implements topics.TipProvider.
type fakeTippedGenerator struct{ fakeGenerator }

func (g fakeTippedGenerator) Tips() quiz.TipProvider { return quiz.StaticTips{} }

var _ topics.Generator = fakeTippedGenerator{}
var _ topics.TipProvider = fakeTippedGenerator{}

// fakeRegistry is an in-memory Registry for unit tests, so hasTips can be
// exercised without mutating internal/topics' process-wide registry.
type fakeRegistry map[string]topics.Generator

func (r fakeRegistry) Get(kind string) (topics.Generator, bool) {
	g, ok := r[kind]
	return g, ok
}

func TestHasTips(t *testing.T) {
	// hasTips is now a plain, generic check with no slug/quiz_kind
	// special-casing (guess-the-language no longer registers a Generator
	// at all — its exercise, and the tips content that gated per-deck,
	// moved into the game zone, internal/game, vibe/design-game-zone.md):
	// HasTips is true exactly when the registered Generator for a topic's
	// quiz_kind implements topics.TipProvider.
	reg := fakeRegistry{
		"road_side":     fakeGenerator{kind: "road_side"},
		"tipped_kind":   fakeTippedGenerator{fakeGenerator{kind: "tipped_kind"}},
		"char_language": fakeGenerator{kind: "char_language"},
	}
	svc := &Service{reg: reg}

	roadTopic := storage.Topic{ID: uuid.New(), QuizKind: "road_side", IsQuizzable: true}
	if svc.hasTips(roadTopic, topicTree{}) {
		t.Fatalf("a generator with no TipProvider capability must not report HasTips")
	}

	tipped := storage.Topic{ID: uuid.New(), Slug: "tipped", QuizKind: "tipped_kind", IsQuizzable: true}
	if !svc.hasTips(tipped, topicTree{}) {
		t.Fatalf("a generator implementing TipProvider should report HasTips=true")
	}
	untipped := storage.Topic{ID: uuid.New(), Slug: "untipped", QuizKind: "char_language", IsQuizzable: true}
	if svc.hasTips(untipped, topicTree{}) {
		t.Fatalf("a generator without TipProvider should report HasTips=false")
	}

	// A container topic aggregates: true if ANY descendant has tips.
	// buildTopicTree keys by parent_id; tipped/untipped need a ParentID
	// pointing at containerID to be found as its children.
	containerID := uuid.New()
	container := storage.Topic{ID: containerID, QuizKind: "container", IsQuizzable: false}
	tippedChild := tipped
	tippedChild.ParentID = &containerID
	untippedChild := untipped
	untippedChild.ParentID = &containerID
	tree := buildTopicTree([]storage.Topic{container, tippedChild, untippedChild})

	if !svc.hasTips(container, tree) {
		t.Fatalf("container with a tipped descendant should report HasTips=true")
	}

	onlyUntippedTree := buildTopicTree([]storage.Topic{container, untippedChild})
	if svc.hasTips(container, onlyUntippedTree) {
		t.Fatalf("container with only non-tipped descendants should report HasTips=false")
	}
}

// ── filterVisibleTopics / hasQuizzableDescendant ────────────────────────
//
// The /topics browser's generic "hide subtrees with no quizzable
// descendants" rule (vibe/design-game-zone.md) — no slug switches: a
// container whose entire subtree is is_quizzable=false (e.g.
// languages/guess-the-language once its exercise moved into the game zone)
// must disappear from a topic listing.

func TestHasQuizzableDescendant(t *testing.T) {
	quizzable := storage.Topic{ID: uuid.New(), IsQuizzable: true}
	if !hasQuizzableDescendant(quizzable, topicTree{}) {
		t.Fatalf("a quizzable topic itself should count, regardless of children")
	}

	emptyContainer := storage.Topic{ID: uuid.New(), IsQuizzable: false}
	if hasQuizzableDescendant(emptyContainer, topicTree{}) {
		t.Fatalf("a non-quizzable topic with no children should report false")
	}

	containerID := uuid.New()
	container := storage.Topic{ID: containerID, IsQuizzable: false}
	nonQuizzableChild := storage.Topic{ID: uuid.New(), ParentID: &containerID, IsQuizzable: false}
	tree := buildTopicTree([]storage.Topic{container, nonQuizzableChild})
	if hasQuizzableDescendant(container, tree) {
		t.Fatalf("a container whose only child is non-quizzable should report false")
	}

	quizzableChild := storage.Topic{ID: uuid.New(), ParentID: &containerID, IsQuizzable: true}
	treeWithQuizzableChild := buildTopicTree([]storage.Topic{container, nonQuizzableChild, quizzableChild})
	if !hasQuizzableDescendant(container, treeWithQuizzableChild) {
		t.Fatalf("a container with at least one quizzable descendant should report true")
	}
}

func TestFilterVisibleTopics_HidesSubtreesWithNoQuizzableDescendants(t *testing.T) {
	languagesID := uuid.New()
	languages := storage.Topic{ID: languagesID, Name: "Languages", IsQuizzable: false}

	specialCharsID := uuid.New()
	specialChars := storage.Topic{ID: specialCharsID, Name: "Special characters", ParentID: &languagesID, IsQuizzable: true}

	guessLangID := uuid.New()
	guessLang := storage.Topic{ID: guessLangID, Name: "Guess the language", ParentID: &languagesID, IsQuizzable: false}
	// Every group topic under guess-the-language is also is_quizzable=false
	// now (design doc): its whole subtree has no quizzable descendant at all.
	romanceGroup := storage.Topic{ID: uuid.New(), Name: "Romance", ParentID: &guessLangID, IsQuizzable: false}

	tree := buildTopicTree([]storage.Topic{languages, specialChars, guessLang, romanceGroup})

	visibleRoots := filterVisibleTopics([]storage.Topic{languages}, tree)
	if len(visibleRoots) != 1 {
		t.Fatalf("expected Languages to stay visible (it has a quizzable descendant), got %+v", visibleRoots)
	}

	visibleChildren := filterVisibleTopics(tree.children(languagesID), tree)
	if len(visibleChildren) != 1 || visibleChildren[0].ID != specialCharsID {
		t.Fatalf("expected only special-characters visible under Languages (guess-the-language hidden), got %+v", visibleChildren)
	}
}

// ── SetSubtreeEnabled / group counts (task 2026-07-19: group-level
// enable/disable toggle on container /topics views) ─────────────────────
//
// DB-backed: skipped unless GEODRILL_TEST_DATABASE_URL is set, mirroring
// integration_test.go's testDSN/freshSchema pattern (its own helpers live in
// the separate study_test package and aren't reachable from here, hence the
// small local duplicate). Its database name MUST contain "test" — freshSchema
// drops every table before the test runs.

// subtreeTestDSN is this file's own copy of integration_test.go's testDSN
// safety fuse (unreachable across the study/study_test package split).
func subtreeTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run the SetSubtreeEnabled integration test")
	}
	s := dsn
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	name := ""
	if i := strings.IndexByte(s, '/'); i >= 0 {
		name = strings.Trim(s[i+1:], "/")
	}
	if !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("refusing to run destructive integration tests against database %q: "+
			"GEODRILL_TEST_DATABASE_URL must point at a disposable database whose name contains \"test\"", name)
	}
	return dsn
}

func freshSubtreeSchema(t *testing.T, dsn string) {
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

// TestSetSubtreeEnabled_MixedOffOnTransitionsIdempotent builds a small tree
// (a root container "Group", a nested container "Sub" under it, and three
// quizzable leaves — two directly under Group, one under Sub) with no items
// at all: SetSubtreeEnabled/GetSubtreeQuizzableTopicCounts only touch
// topics/topic_paths/user_topics, so no seeding via internal/topics is
// needed. Exercises: default all-on, group-off, a manual single-topic
// re-enable producing a mixed state, group-on again, and idempotency (a
// repeated call with the same value changes nothing).
func TestSetSubtreeEnabled_MixedOffOnTransitionsIdempotent(t *testing.T) {
	dsn := subtreeTestDSN(t)
	freshSubtreeSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	group, err := store.UpsertTopic(ctx, nil, "group", "Group", 0, 0, "container", nil, false, []byte("{}"))
	if err != nil {
		t.Fatalf("upsert Group: %v", err)
	}
	leaf1, err := store.UpsertTopic(ctx, &group.ID, "leaf1", "Leaf1", 0, 0, "single_choice", nil, true, []byte("{}"))
	if err != nil {
		t.Fatalf("upsert Leaf1: %v", err)
	}
	leaf2, err := store.UpsertTopic(ctx, &group.ID, "leaf2", "Leaf2", 1, 0, "single_choice", nil, true, []byte("{}"))
	if err != nil {
		t.Fatalf("upsert Leaf2: %v", err)
	}
	sub, err := store.UpsertTopic(ctx, &group.ID, "sub", "Sub", 2, 0, "container", nil, false, []byte("{}"))
	if err != nil {
		t.Fatalf("upsert Sub: %v", err)
	}
	leaf3, err := store.UpsertTopic(ctx, &sub.ID, "leaf3", "Leaf3", 0, 0, "single_choice", nil, true, []byte("{}"))
	if err != nil {
		t.Fatalf("upsert Leaf3 (nested under Sub): %v", err)
	}

	user, err := store.UpsertUser(ctx, 990001, "subtree-tester")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	svc := New(store, nil, GlobalRegistry, nil, 1)

	// Default: every topic starts enabled (architecture §2.10), so the
	// group's view should report all 3 leaves enabled (including the one
	// nested two levels down under Sub).
	view, err := svc.Children(ctx, user.ID, group.ID)
	if err != nil {
		t.Fatalf("Children (Group, initial): %v", err)
	}
	if view.GroupTotalLeaves != 3 {
		t.Fatalf("expected 3 quizzable leaves in Group's subtree (leaf1, leaf2, leaf3), got %d", view.GroupTotalLeaves)
	}
	if view.GroupEnabledLeaves != 3 {
		t.Fatalf("expected all 3 leaves enabled by default, got %d", view.GroupEnabledLeaves)
	}

	// Turn the whole group off.
	if err := svc.SetSubtreeEnabled(ctx, user.ID, group.ID, false); err != nil {
		t.Fatalf("SetSubtreeEnabled(off): %v", err)
	}
	view, err = svc.Children(ctx, user.ID, group.ID)
	if err != nil {
		t.Fatalf("Children (Group, after group-off): %v", err)
	}
	if view.GroupEnabledLeaves != 0 {
		t.Fatalf("expected 0 leaves enabled after group-off, got %d", view.GroupEnabledLeaves)
	}
	for _, id := range []uuid.UUID{leaf1.ID, leaf2.ID, leaf3.ID} {
		enabled, err := store.GetUserTopicEnabled(ctx, user.ID, id)
		if err != nil {
			t.Fatalf("GetUserTopicEnabled(%s): %v", id, err)
		}
		if enabled {
			t.Fatalf("expected leaf %s disabled after group-off", id)
		}
	}
	// The nested Sub container itself must be untouched (only quizzable
	// topics are ever written by SetSubtreeEnabled) — it stays default-on.
	subEnabled, err := store.GetUserTopicEnabled(ctx, user.ID, sub.ID)
	if err != nil {
		t.Fatalf("GetUserTopicEnabled(Sub): %v", err)
	}
	if !subEnabled {
		t.Fatalf("a container topic must never be written by SetSubtreeEnabled (cosmetic-only per TopicRow.Enabled's doc)")
	}

	// Manually re-enable one leaf: the group's aggregate must now read
	// mixed (1 of 3 enabled).
	if err := svc.SetTopicEnabled(ctx, user.ID, leaf1.ID, true); err != nil {
		t.Fatalf("SetTopicEnabled(leaf1, true): %v", err)
	}
	view, err = svc.Children(ctx, user.ID, group.ID)
	if err != nil {
		t.Fatalf("Children (Group, mixed): %v", err)
	}
	if view.GroupTotalLeaves != 3 || view.GroupEnabledLeaves != 1 {
		t.Fatalf("expected a mixed state (1/3 enabled), got %d/%d", view.GroupEnabledLeaves, view.GroupTotalLeaves)
	}

	// Turn the whole group back on.
	if err := svc.SetSubtreeEnabled(ctx, user.ID, group.ID, true); err != nil {
		t.Fatalf("SetSubtreeEnabled(on): %v", err)
	}
	view, err = svc.Children(ctx, user.ID, group.ID)
	if err != nil {
		t.Fatalf("Children (Group, after group-on): %v", err)
	}
	if view.GroupEnabledLeaves != 3 {
		t.Fatalf("expected all 3 leaves enabled after group-on, got %d", view.GroupEnabledLeaves)
	}

	// Idempotency: repeating group-on with the same value must be a no-op
	// (pure upsert, ON CONFLICT DO UPDATE) — no error, same resulting state.
	if err := svc.SetSubtreeEnabled(ctx, user.ID, group.ID, true); err != nil {
		t.Fatalf("SetSubtreeEnabled(on, repeated): %v", err)
	}
	view, err = svc.Children(ctx, user.ID, group.ID)
	if err != nil {
		t.Fatalf("Children (Group, after repeated group-on): %v", err)
	}
	if view.GroupTotalLeaves != 3 || view.GroupEnabledLeaves != 3 {
		t.Fatalf("expected the repeated group-on to be idempotent (3/3), got %d/%d", view.GroupEnabledLeaves, view.GroupTotalLeaves)
	}
}

func TestBuildTopicTreeAndBreadcrumbWalk(t *testing.T) {
	root := storage.Topic{ID: uuid.New(), Name: "Languages"}
	rootID := root.ID
	child := storage.Topic{ID: uuid.New(), Name: "Special characters", ParentID: &rootID}

	tree := buildTopicTree([]storage.Topic{root, child})
	kids := tree.children(root.ID)
	if len(kids) != 1 || kids[0].ID != child.ID {
		t.Fatalf("expected root to have exactly child as its one child, got %+v", kids)
	}
	if len(tree.children(child.ID)) != 0 {
		t.Fatalf("leaf topic should have no children")
	}
}
