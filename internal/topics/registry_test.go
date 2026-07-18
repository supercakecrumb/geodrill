package topics

import (
	"context"
	"math/rand"
	"testing"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// fakeGenerator is a minimal Generator for registry tests.
type fakeGenerator struct {
	kind string
}

func (f fakeGenerator) Kind() string { return f.kind }

func (f fakeGenerator) BuildExercise(ctx context.Context, rng *rand.Rand, req ExerciseRequest) (Exercise, error) {
	return Exercise{}, nil
}

func (f fakeGenerator) BuildIntro(ctx context.Context, item storage.Item) (IntroCard, error) {
	return IntroCard{}, nil
}

// fakeTippedGenerator additionally implements TipProvider, to check the
// optional-interface pattern documented on Generator.
type fakeTippedGenerator struct {
	fakeGenerator
}

func (f fakeTippedGenerator) Tips() quiz.TipProvider {
	return quiz.StaticTips{"x": "tip"}
}

// withCleanRegistry swaps in an empty registry for the duration of the test
// and restores the original afterward, so tests don't leak state into each
// other (Register panics on duplicate kinds across the whole package).
func withCleanRegistry(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	saved := registry
	registry = make(map[string]Generator)
	registryMu.Unlock()

	t.Cleanup(func() {
		registryMu.Lock()
		registry = saved
		registryMu.Unlock()
	})
}

func TestRegisterAndGet(t *testing.T) {
	withCleanRegistry(t)

	Register(fakeGenerator{kind: "char_language"})

	got, ok := Get("char_language")
	if !ok {
		t.Fatal("Get(\"char_language\") ok = false, want true")
	}
	if got.Kind() != "char_language" {
		t.Fatalf("Get(\"char_language\").Kind() = %q, want %q", got.Kind(), "char_language")
	}
}

func TestGetUnknownKind(t *testing.T) {
	withCleanRegistry(t)

	if _, ok := Get("nonexistent"); ok {
		t.Fatal("Get(\"nonexistent\") ok = true, want false")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	withCleanRegistry(t)

	Register(fakeGenerator{kind: "road_side"})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register with a duplicate kind did not panic")
		}
	}()
	Register(fakeGenerator{kind: "road_side"})
}

func TestKindsSortedAndComplete(t *testing.T) {
	withCleanRegistry(t)

	Register(fakeGenerator{kind: "word_language"})
	Register(fakeGenerator{kind: "char_language"})
	Register(fakeGenerator{kind: "language_id"})

	want := []string{"char_language", "language_id", "word_language"}
	got := Kinds()
	if len(got) != len(want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Kinds() = %v, want %v", got, want)
		}
	}
}

func TestKindsEmptyRegistry(t *testing.T) {
	withCleanRegistry(t)

	got := Kinds()
	if len(got) != 0 {
		t.Fatalf("Kinds() on empty registry = %v, want empty", got)
	}
}

func TestOptionalTipProviderInterface(t *testing.T) {
	withCleanRegistry(t)

	Register(fakeTippedGenerator{fakeGenerator{kind: "tipped"}})

	gen, ok := Get("tipped")
	if !ok {
		t.Fatal("Get(\"tipped\") ok = false, want true")
	}
	tp, ok := gen.(TipProvider)
	if !ok {
		t.Fatal("registered generator should satisfy TipProvider via type assertion")
	}
	if got := tp.Tips().Tip(quiz.TipRequest{CorrectKey: "x"}); got != "tip" {
		t.Fatalf("Tips().Tip() = %q, want %q", got, "tip")
	}
}
