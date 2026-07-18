package study

import (
	"context"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/guesslang"
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
	reg := fakeRegistry{
		"road_side":     fakeGenerator{kind: "road_side"},
		guesslang.Kind:  fakeTippedGenerator{fakeGenerator{kind: guesslang.Kind}},
		"char_language": fakeGenerator{kind: "char_language"},
	}
	svc := &Service{reg: reg}

	roadTopic := storage.Topic{ID: uuid.New(), QuizKind: "road_side", IsQuizzable: true}
	if svc.hasTips(roadTopic, topicTree{}) {
		t.Fatalf("a generator with no TipProvider capability must not report HasTips")
	}

	// language_id (guesslang.Kind) is special-cased: tips.DeckHasTips gates
	// it per-slug (only the curated "romance" deck has tips today).
	romance := storage.Topic{ID: uuid.New(), Slug: "romance", QuizKind: guesslang.Kind, IsQuizzable: true}
	if !svc.hasTips(romance, topicTree{}) {
		t.Fatalf("romance deck is curated with tips and should report HasTips=true")
	}
	cjk := storage.Topic{ID: uuid.New(), Slug: "cjk", QuizKind: guesslang.Kind, IsQuizzable: true}
	if svc.hasTips(cjk, topicTree{}) {
		t.Fatalf("cjk deck has no curated tips and should report HasTips=false")
	}

	// A container topic aggregates: true if ANY descendant has tips.
	containerID := uuid.New()
	container := storage.Topic{ID: containerID, QuizKind: "container", IsQuizzable: false}
	tree := buildTopicTree([]storage.Topic{container, romance, cjk})
	// buildTopicTree keys by parent_id; romance/cjk need a ParentID pointing
	// at containerID to be found as its children.
	romanceChild := romance
	romanceChild.ParentID = &containerID
	cjkChild := cjk
	cjkChild.ParentID = &containerID
	tree = buildTopicTree([]storage.Topic{container, romanceChild, cjkChild})

	if !svc.hasTips(container, tree) {
		t.Fatalf("container with a tipped descendant (romance) should report HasTips=true")
	}

	onlyCJKTree := buildTopicTree([]storage.Topic{container, cjkChild})
	if svc.hasTips(container, onlyCJKTree) {
		t.Fatalf("container with only non-tipped descendants should report HasTips=false")
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
