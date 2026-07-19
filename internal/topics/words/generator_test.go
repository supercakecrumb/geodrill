package words

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"testing"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// mkItem builds a storage.Item carrying a well-formed word payload, keyed the
// way Seed keys items ("<language>:<word>").
func mkItem(word, language, meaning string) storage.Item {
	raw, err := json.Marshal(itemPayload{Word: word, Language: language, Meaning: meaning})
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: language + ":" + word, Label: word, Payload: raw}
}

// cyrillicSiblings + latinSiblings are shared fixtures spanning both scripts
// in the deck, so same-script tests exercise real cross-script contamination
// risk rather than a trivially-separated fixture.
func cyrillicSiblings() []storage.Item {
	return []storage.Item{
		mkItem("улица", "rus", "street"),
		mkItem("дорога", "rus", "road"),
		mkItem("вулиця", "ukr", "street"),
		mkItem("булевард", "bul", "avenue"),
		mkItem("пут", "srp", "road"),
		mkItem("пат", "mkd", "road"),
	}
}

func latinSiblings() []storage.Item {
	return []storage.Item{
		mkItem("ulica", "pol", "street"),
		mkItem("calle", "spa", "street"),
		mkItem("rua", "por", "street"),
		mkItem("via", "ita", "street"),
		mkItem("rue", "fra", "street"),
		mkItem("gata", "swe", "street"),
	}
}

func isCyrillicLang(code string) bool {
	switch code {
	case "rus", "ukr", "bul", "srp", "mkd":
		return true
	}
	return false
}

func TestBuildExercise_SameScriptInvariant_Cyrillic(t *testing.T) {
	gen := New()
	target := mkItem("улица", "rus", "street")
	// Mix in Latin siblings alongside the Cyrillic ones: a Cyrillic target
	// must never surface a Latin distractor.
	siblings := append(cyrillicSiblings(), latinSiblings()...)

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{
		Item:     target,
		Siblings: siblings,
	})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if len(ex.Options) < 2 {
		t.Fatalf("expected at least the correct answer plus one distractor, got %+v", ex.Options)
	}
	for _, opt := range ex.Options {
		if !isCyrillicLang(opt.Key) {
			t.Fatalf("non-Cyrillic distractor %q leaked into a Cyrillic-target exercise: %+v", opt.Key, ex.Options)
		}
	}
}

func TestBuildExercise_SameScriptInvariant_Latin(t *testing.T) {
	gen := New()
	target := mkItem("ulica", "pol", "street")
	siblings := append(latinSiblings(), cyrillicSiblings()...)

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{
		Item:     target,
		Siblings: siblings,
	})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	for _, opt := range ex.Options {
		if isCyrillicLang(opt.Key) {
			t.Fatalf("Cyrillic distractor %q leaked into a Latin-target exercise: %+v", opt.Key, ex.Options)
		}
	}
}

// TestBuildExercise_LanguageGroupCloseness_SlavicLatinNeverOffersFarGroups is
// the closeness regression for the word topic: a slavic-latin target (Polish
// "ulica") must never offer a Romance (Spanish) or Nordic (Swedish)
// distractor, even though all three are "latin" script — the gap
// script-only grouping left open, closed by grouping on the
// guess-the-language language family instead (engine.LanguageGroup).
func TestBuildExercise_LanguageGroupCloseness_SlavicLatinNeverOffersFarGroups(t *testing.T) {
	gen := New()
	target := mkItem("ulica", "pol", "street") // pol => slavic-latin
	siblings := []storage.Item{
		mkItem("silnice", "ces", "highway"), // slavic-latin: close, OK
		mkItem("grad", "hrv", "city"),       // slavic-latin: close, OK
		mkItem("izhod", "slv", "exit"),      // slavic-latin: close, OK
		mkItem("zavreté", "slk", "closed"),  // slavic-latin: close, OK
		mkItem("calle", "spa", "street"),    // romance — far, must never appear
		mkItem("väg", "swe", "road"),        // nordic — far, must never appear
	}
	req := topics.ExerciseRequest{Item: target, Siblings: siblings}

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), req)
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if len(ex.Options) < 2 {
		t.Fatalf("expected at least one distractor drawn from the slavic-latin group, got %+v", ex.Options)
	}
	for _, opt := range ex.Options {
		if opt.Key == "spa" || opt.Key == "swe" {
			t.Fatalf("far-group distractor %q leaked into a slavic-latin-target exercise: %+v", opt.Key, ex.Options)
		}
	}
}

// TestBuildExercise_SmallGroupUnderfillsWithoutCrash covers the "fewer
// siblings than Max" fallback the engine already provides: se-asia is a
// small group in this deck (only Indonesian and Vietnamese words are
// seeded), well under maxDistractors (5), so the exercise must still come
// back with exactly target + the one available distractor rather than
// erroring or padding with an off-group option.
func TestBuildExercise_SmallGroupUnderfillsWithoutCrash(t *testing.T) {
	gen := New()
	target := mkItem("jalan", "ind", "street/road") // ind => se-asia
	siblings := []storage.Item{
		mkItem("đường", "vie", "street/road"), // only other se-asia word in this fixture
		mkItem("calle", "spa", "street"),      // different group, must be excluded
	}
	req := topics.ExerciseRequest{Item: target, Siblings: siblings}

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), req)
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if len(ex.Options) != 2 {
		t.Fatalf("len(Options) = %d, want 2 (target + the 1 available same-group distractor): %+v", len(ex.Options), ex.Options)
	}
}

func TestBuildExercise_Deterministic(t *testing.T) {
	gen := New()
	req := topics.ExerciseRequest{
		Item:     mkItem("улица", "rus", "street"),
		Siblings: cyrillicSiblings(),
	}

	ex1, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(42)), req)
	if err != nil {
		t.Fatalf("BuildExercise (1): %v", err)
	}
	ex2, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(42)), req)
	if err != nil {
		t.Fatalf("BuildExercise (2): %v", err)
	}

	if ex1.Prompt != ex2.Prompt || ex1.CorrectAnswer != ex2.CorrectAnswer {
		t.Fatalf("same seed produced different prompt/answer: %+v vs %+v", ex1, ex2)
	}
	if len(ex1.Options) != len(ex2.Options) {
		t.Fatalf("same seed produced different option counts: %d vs %d", len(ex1.Options), len(ex2.Options))
	}
	for i := range ex1.Options {
		if ex1.Options[i] != ex2.Options[i] {
			t.Fatalf("same seed produced different option order at index %d: %+v vs %+v", i, ex1.Options, ex2.Options)
		}
	}

	// A different seed should (with overwhelming likelihood, given 6
	// candidates) reorder the options — guards against an accidental
	// non-random no-op shuffle silently "passing" the determinism check.
	ex3, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), req)
	if err != nil {
		t.Fatalf("BuildExercise (3): %v", err)
	}
	same := len(ex1.Options) == len(ex3.Options)
	if same {
		for i := range ex1.Options {
			if ex1.Options[i] != ex3.Options[i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Fatalf("different seeds produced identical option order — shuffle looks like a no-op: %+v", ex1.Options)
	}
}

func TestBuildExercise_CorrectAnswer(t *testing.T) {
	gen := New()
	target := mkItem("ulica", "pol", "street")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(7)), topics.ExerciseRequest{
		Item:     target,
		Siblings: latinSiblings(),
	})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != "pol" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "pol")
	}
	if ex.Mode != quiz.ModeSingle {
		t.Fatalf("Mode = %v, want ModeSingle", ex.Mode)
	}

	var matches int
	for _, opt := range ex.Options {
		if opt.Key == ex.CorrectAnswer {
			matches++
			if opt.Label != "Polish" {
				t.Fatalf("correct option label = %q, want %q", opt.Label, "Polish")
			}
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one option matching CorrectAnswer, got %d in %+v", matches, ex.Options)
	}
}

func TestBuildIntro_Text(t *testing.T) {
	gen := New()
	item := mkItem("ulica", "pol", "street")

	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "\U0001F4D6 “ulica” — “street” in Polish."
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
	if card.MediaPath != "" {
		t.Fatalf("word items are text-only, got MediaPath %q", card.MediaPath)
	}
}

func TestMalformedPayload(t *testing.T) {
	gen := New()

	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"invalid json", []byte(`{"word":`)},
		{"missing word", []byte(`{"language":"pol","meaning":"street"}`)},
		{"missing language", []byte(`{"word":"ulica","meaning":"street"}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/BuildExercise", func(t *testing.T) {
			item := storage.Item{Key: "bad", Payload: tc.payload}
			_, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{Item: item})
			if !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("BuildExercise error = %v, want wrapping ErrMalformedPayload", err)
			}
		})
		t.Run(tc.name+"/BuildIntro", func(t *testing.T) {
			item := storage.Item{Key: "bad", Payload: tc.payload}
			_, err := gen.BuildIntro(context.Background(), item)
			if !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("BuildIntro error = %v, want wrapping ErrMalformedPayload", err)
			}
		})
	}
}

func TestKind(t *testing.T) {
	if got := New().Kind(); got != "word_language" {
		t.Fatalf("Kind() = %q, want %q", got, "word_language")
	}
}
