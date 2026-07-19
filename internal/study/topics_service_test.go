package study

import (
	"context"
	"math/rand"
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
