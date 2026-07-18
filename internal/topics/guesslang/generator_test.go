package guesslang

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// fakeSampler is a deterministic, in-memory ContentSampler double: byKey
// backs SampleContent, anyByKey backs SampleContentAny. Calls are recorded so
// tests can assert which method(s) a code path actually used.
type fakeSampler struct {
	byKey    map[string]storage.Content
	anyByKey map[string]storage.Content

	sampleCalls []string
	anyCalls    []string
}

func (f *fakeSampler) SampleContent(_ context.Context, _ uuid.UUID, key string) (storage.Content, bool, error) {
	f.sampleCalls = append(f.sampleCalls, key)
	c, ok := f.byKey[key]
	return c, ok, nil
}

func (f *fakeSampler) SampleContentAny(_ context.Context, key string) (storage.Content, bool, error) {
	f.anyCalls = append(f.anyCalls, key)
	c, ok := f.anyByKey[key]
	return c, ok, nil
}

func mkItem(key, label string) storage.Item {
	return storage.Item{ID: uuid.New(), TopicID: uuid.New(), Key: key, Label: label}
}

func TestKind(t *testing.T) {
	if got := New(&fakeSampler{}).Kind(); got != "language_id" {
		t.Fatalf("Kind() = %q, want %q", got, "language_id")
	}
}

func TestGenerator_ImplementsTipProvider(t *testing.T) {
	var _ topics.TipProvider = New(&fakeSampler{})
}

func TestBuildExercise_PrefersSampleContentOverAny(t *testing.T) {
	contentID := uuid.New()
	sampler := &fakeSampler{
		byKey: map[string]storage.Content{
			"spa": {ID: contentID, Kind: "sentence", Key: "spa", Payload: "El coche es rojo.", Source: "tatoeba#1"},
		},
	}
	gen := New(sampler)
	target := mkItem("spa", "Spanish")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{
		Item: target,
		User: storage.User{ID: uuid.New()},
	})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if len(sampler.anyCalls) != 0 {
		t.Fatalf("SampleContentAny called %d times, want 0 (SampleContent already found content)", len(sampler.anyCalls))
	}
	if len(sampler.sampleCalls) != 1 || sampler.sampleCalls[0] != "spa" {
		t.Fatalf("SampleContent calls = %v, want [\"spa\"]", sampler.sampleCalls)
	}
	if ex.Prompt != "El coche es rojo." {
		t.Fatalf("Prompt = %q, want the sampled sentence", ex.Prompt)
	}
	if ex.ContentID == nil || *ex.ContentID != contentID {
		t.Fatalf("ContentID = %v, want %v", ex.ContentID, contentID)
	}
	if ex.Source != "tatoeba#1" {
		t.Fatalf("Source = %q, want %q", ex.Source, "tatoeba#1")
	}
}

func TestBuildExercise_FallsBackToSampleContentAny(t *testing.T) {
	contentID := uuid.New()
	sampler := &fakeSampler{
		byKey: map[string]storage.Content{}, // exclude-recent pool empty
		anyByKey: map[string]storage.Content{
			"spa": {ID: contentID, Kind: "sentence", Key: "spa", Payload: "Yo tengo un perro.", Source: "tatoeba#2"},
		},
	}
	gen := New(sampler)

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{
		Item: mkItem("spa", "Spanish"),
		User: storage.User{ID: uuid.New()},
	})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if len(sampler.sampleCalls) != 1 || len(sampler.anyCalls) != 1 {
		t.Fatalf("expected exactly one SampleContent call then one SampleContentAny fallback, got sampleCalls=%v anyCalls=%v", sampler.sampleCalls, sampler.anyCalls)
	}
	if ex.Prompt != "Yo tengo un perro." {
		t.Fatalf("Prompt = %q, want the fallback-sampled sentence", ex.Prompt)
	}
}

func TestBuildExercise_NoContentAnywhere_ReturnsErrNoContent(t *testing.T) {
	gen := New(&fakeSampler{})
	_, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{
		Item: mkItem("spa", "Spanish"),
		User: storage.User{ID: uuid.New()},
	})
	if !errors.Is(err, topics.ErrNoContent) {
		t.Fatalf("err = %v, want wrapping topics.ErrNoContent", err)
	}
}

func TestBuildExercise_OptionComposition(t *testing.T) {
	sampler := &fakeSampler{
		byKey: map[string]storage.Content{
			"spa": {ID: uuid.New(), Payload: "El coche es rojo.", Source: "tatoeba#1"},
		},
	}
	gen := New(sampler)
	target := mkItem("spa", "Spanish")
	siblings := []storage.Item{mkItem("por", "Portuguese"), mkItem("ita", "Italian"), mkItem("fra", "French")}

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(7)), topics.ExerciseRequest{
		Item:     target,
		Siblings: siblings,
		User:     storage.User{ID: uuid.New()},
	})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeSingle {
		t.Fatalf("Mode = %v, want ModeSingle", ex.Mode)
	}
	if ex.CorrectAnswer != "spa" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "spa")
	}
	wantLabels := map[string]string{"spa": "Spanish", "por": "Portuguese", "ita": "Italian", "fra": "French"}
	if len(ex.Options) != len(wantLabels) {
		t.Fatalf("Options = %+v, want %d options (target + %d siblings)", ex.Options, len(wantLabels), len(siblings))
	}
	seen := map[string]bool{}
	for _, o := range ex.Options {
		wantLabel, ok := wantLabels[o.Key]
		if !ok {
			t.Fatalf("unexpected option key %q in %+v", o.Key, ex.Options)
		}
		if o.Label != wantLabel {
			t.Fatalf("option %q label = %q, want %q", o.Key, o.Label, wantLabel)
		}
		seen[o.Key] = true
	}
	if len(seen) != len(wantLabels) {
		t.Fatalf("duplicate option keys in %+v", ex.Options)
	}
}

func TestBuildExercise_DeterministicGivenSameSeed(t *testing.T) {
	sampler := &fakeSampler{
		byKey: map[string]storage.Content{
			"spa": {ID: uuid.New(), Payload: "El coche es rojo.", Source: "tatoeba#1"},
		},
	}
	gen := New(sampler)
	target := mkItem("spa", "Spanish")
	siblings := []storage.Item{mkItem("por", "Portuguese"), mkItem("ita", "Italian"), mkItem("fra", "French"), mkItem("cat", "Catalan"), mkItem("ron", "Romanian")}
	req := topics.ExerciseRequest{Item: target, Siblings: siblings, User: storage.User{ID: uuid.New()}}

	ex1, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(99)), req)
	if err != nil {
		t.Fatalf("BuildExercise (1): %v", err)
	}
	ex2, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(99)), req)
	if err != nil {
		t.Fatalf("BuildExercise (2): %v", err)
	}
	if len(ex1.Options) != len(ex2.Options) {
		t.Fatalf("option count differs across identical seeds: %d vs %d", len(ex1.Options), len(ex2.Options))
	}
	for i := range ex1.Options {
		if ex1.Options[i] != ex2.Options[i] {
			t.Fatalf("option order differs across identical seeds at index %d: %+v vs %+v", i, ex1.Options, ex2.Options)
		}
	}
}

func TestBuildIntro_WithSample(t *testing.T) {
	sampler := &fakeSampler{
		anyByKey: map[string]storage.Content{
			"spa": {ID: uuid.New(), Payload: "El coche es rojo."},
		},
	}
	gen := New(sampler)

	card, err := gen.BuildIntro(context.Background(), mkItem("spa", "Spanish"))
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "🗣 Spanish — you'll see sentences like: “El coche es rojo.”"
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
	if card.MediaPath != "" {
		t.Fatalf("guesslang items are text-only, got MediaPath %q", card.MediaPath)
	}
}

func TestBuildIntro_TruncatesLongSample(t *testing.T) {
	long := strings.Repeat("a", maxSampleRunes+40)
	sampler := &fakeSampler{
		anyByKey: map[string]storage.Content{
			"spa": {ID: uuid.New(), Payload: long},
		},
	}
	gen := New(sampler)

	card, err := gen.BuildIntro(context.Background(), mkItem("spa", "Spanish"))
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if !strings.HasSuffix(card.Text, "…”") {
		t.Fatalf("BuildIntro text = %q, want it to end with an ellipsis before the closing quote", card.Text)
	}
	quoted := strings.TrimSuffix(strings.SplitN(card.Text, "“", 2)[1], "”")
	quoted = strings.TrimSuffix(quoted, "…")
	if len([]rune(quoted)) != maxSampleRunes {
		t.Fatalf("truncated sample has %d runes, want %d", len([]rune(quoted)), maxSampleRunes)
	}
}

func TestBuildIntro_NoContent_Fallback(t *testing.T) {
	gen := New(&fakeSampler{})

	card, err := gen.BuildIntro(context.Background(), mkItem("spa", "Spanish"))
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "🗣 Spanish — you'll see sentences in this language to guess."
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
}

func TestTips_UsesRealTellProvider(t *testing.T) {
	gen := New(&fakeSampler{})
	tp := gen.Tips()
	if tp == nil {
		t.Fatal("Tips() returned nil")
	}
	got := tp.Tip(quiz.TipRequest{
		ContentPayload: "Yo tengo un perro pero no tengo un gato.",
		CorrectKey:     "spa",
		CorrectLabel:   "Spanish",
		ChosenKey:      "spa",
		Correct:        true,
	})
	if got == "" {
		t.Fatal("Tip() returned empty string for a sentence containing a known Spanish tell (\"yo\")")
	}
	if !strings.HasPrefix(got, "Spanish:") {
		t.Fatalf("Tip() = %q, want it prefixed with the language label", got)
	}
}
