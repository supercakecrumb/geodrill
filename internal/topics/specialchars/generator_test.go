package specialchars

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// mustItem builds a storage.Item with a marshaled char_language payload, for
// test fixtures. key defaults to char.
func mustItem(t *testing.T, char, script string, languages []string, note string) storage.Item {
	t.Helper()
	raw, err := json.Marshal(payload{Char: char, Script: script, Languages: languages, Note: note})
	if err != nil {
		t.Fatalf("marshal test payload: %v", err)
	}
	return storage.Item{Key: char, Label: char, Payload: raw}
}

// siblingFixture is a representative slice of Latin- and Cyrillic-script
// items mirroring seeds/special_chars.yaml, used as req.Siblings across
// tests so distractor pools are realistic without depending on the yaml file
// (unit tests must not touch the filesystem/DB).
func siblingFixture(t *testing.T) []storage.Item {
	t.Helper()
	return []storage.Item{
		mustItem(t, "ą", "latin", []string{"pol"}, "Polish"),
		mustItem(t, "ă", "latin", []string{"ron"}, "Romanian"),
		mustItem(t, "ã", "latin", []string{"por"}, "Portuguese"),
		mustItem(t, "ð", "latin", []string{"isl"}, "Icelandic"),
		mustItem(t, "ě", "latin", []string{"ces"}, "Czech"),
		mustItem(t, "ñ", "latin", []string{"spa"}, "Spanish"),
		mustItem(t, "å", "latin", []string{"swe", "nor", "dan"}, "Nordic"),
		mustItem(t, "ø", "latin", []string{"nor", "dan"}, "Norwegian, Danish"),
		mustItem(t, "č", "latin", []string{"ces", "slk", "hrv", "slv"}, "Czech, Slovak, Croatian, Slovenian"),
		mustItem(t, "ё", "cyrillic", []string{"rus"}, "Russian"),
		mustItem(t, "є", "cyrillic", []string{"ukr"}, "Ukrainian"),
		mustItem(t, "љ", "cyrillic", []string{"srp", "mkd"}, "Serbian, Macedonian"),
	}
}

func newRNG(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// ── ModeSingle ──────────────────────────────────────────────────────────────

func TestBuildExercise_SingleMCQ_ComposesCorrectAnswer(t *testing.T) {
	gen := New()
	item := mustItem(t, "ñ", "latin", []string{"spa"}, "eñe — Spanish")
	req := topics.ExerciseRequest{
		Item:     item,
		Siblings: siblingFixture(t),
		Mode:     quiz.ModeSingle,
	}

	ex, err := gen.BuildExercise(context.Background(), newRNG(1), req)
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeSingle {
		t.Fatalf("mode = %v, want ModeSingle", ex.Mode)
	}
	if ex.CorrectAnswer != "spa" {
		t.Fatalf("CorrectAnswer = %q, want spa", ex.CorrectAnswer)
	}
	if len(ex.Options) < 2 {
		t.Fatalf("expected at least 2 options (target + 1 distractor), got %d", len(ex.Options))
	}
	if len(ex.Options) > maxSingleDistractors+1 {
		t.Fatalf("expected at most %d options, got %d", maxSingleDistractors+1, len(ex.Options))
	}

	var found int
	seen := map[string]bool{}
	for _, o := range ex.Options {
		if seen[o.Key] {
			t.Fatalf("duplicate option key %q", o.Key)
		}
		seen[o.Key] = true
		if o.Key == "spa" {
			found++
			if o.Label != "Spanish" {
				t.Fatalf("target label = %q, want Spanish", o.Label)
			}
		}
	}
	if found != 1 {
		t.Fatalf("target option present %d times, want exactly 1", found)
	}
	if ex.Prompt == "" {
		t.Fatalf("expected a non-empty prompt")
	}
}

func TestBuildExercise_SingleMCQ_Deterministic(t *testing.T) {
	gen := New()
	item := mustItem(t, "ñ", "latin", []string{"spa"}, "")
	req := topics.ExerciseRequest{Item: item, Siblings: siblingFixture(t), Mode: quiz.ModeSingle}

	ex1, err := gen.BuildExercise(context.Background(), newRNG(7), req)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	ex2, err := gen.BuildExercise(context.Background(), newRNG(7), req)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if !reflect.DeepEqual(ex1, ex2) {
		t.Fatalf("same seed produced different exercises:\n%+v\nvs\n%+v", ex1, ex2)
	}
}

// ── ModeText ────────────────────────────────────────────────────────────────

func TestBuildExercise_Text(t *testing.T) {
	gen := New()
	item := mustItem(t, "ñ", "latin", []string{"spa"}, "")
	req := topics.ExerciseRequest{Item: item, Siblings: siblingFixture(t), Mode: quiz.ModeText}

	ex, err := gen.BuildExercise(context.Background(), newRNG(1), req)
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("mode = %v, want ModeText", ex.Mode)
	}
	if ex.CorrectAnswer != "Spanish" {
		t.Fatalf("CorrectAnswer = %q, want Spanish", ex.CorrectAnswer)
	}
	wantAccept := []string{"Spanish", "spa", "español", "espanol"}
	if !reflect.DeepEqual(ex.Accept, wantAccept) {
		t.Fatalf("Accept = %v, want %v", ex.Accept, wantAccept)
	}
}

func TestBuildExercise_Text_UnknownLanguageFallsBackToCode(t *testing.T) {
	gen := New()
	item := mustItem(t, "x", "latin", []string{"xyz"}, "")
	req := topics.ExerciseRequest{Item: item, Mode: quiz.ModeText}

	ex, err := gen.BuildExercise(context.Background(), newRNG(1), req)
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != "XYZ" {
		t.Fatalf("CorrectAnswer = %q, want XYZ (uppercased fallback)", ex.CorrectAnswer)
	}
}

// ── ModeSet ─────────────────────────────────────────────────────────────────

func canonJoin(codes []string) []string {
	out := append([]string(nil), codes...)
	sort.Strings(out)
	return out
}

func setEqual(a, b []string) bool {
	ca, cb := canonJoin(a), canonJoin(b)
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

func TestBuildExercise_SetMCQ_TargetInOptionsAndSetEquality(t *testing.T) {
	gen := New()
	item := mustItem(t, "ø", "latin", []string{"nor", "dan"}, "o with stroke — Norwegian, Danish")
	req := topics.ExerciseRequest{Item: item, Siblings: siblingFixture(t), Mode: quiz.ModeSet}

	ex, err := gen.BuildExercise(context.Background(), newRNG(3), req)
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeSet {
		t.Fatalf("mode = %v, want ModeSet", ex.Mode)
	}
	if ex.CorrectAnswer != "dan,nor" {
		t.Fatalf("CorrectAnswer = %q, want dan,nor (canonical sorted)", ex.CorrectAnswer)
	}
	if len(ex.OptionSets) < 2 {
		t.Fatalf("expected at least 2 option sets (target + 1 distractor), got %d", len(ex.OptionSets))
	}
	if len(ex.OptionSets) > maxSetDistractors+1 {
		t.Fatalf("expected at most %d option sets, got %d", maxSetDistractors+1, len(ex.OptionSets))
	}

	var targetMatches int
	seenSets := make([]([]string), 0, len(ex.OptionSets))
	for _, os := range ex.OptionSets {
		if setEqual(os.Keys, []string{"nor", "dan"}) {
			targetMatches++
			if os.Label != "Norwegian / Danish" {
				t.Fatalf("target label = %q, want %q (declared-order)", os.Label, "Norwegian / Danish")
			}
		}
		for _, prev := range seenSets {
			if setEqual(prev, os.Keys) {
				t.Fatalf("duplicate option set %v", os.Keys)
			}
		}
		seenSets = append(seenSets, os.Keys)
		if len(os.Keys) != 2 {
			t.Fatalf("distractor set %v has size %d, want 2 (same size as target)", os.Keys, len(os.Keys))
		}
	}
	if targetMatches != 1 {
		t.Fatalf("target set present %d times among options, want exactly 1", targetMatches)
	}
}

func TestBuildExercise_SetMCQ_Deterministic(t *testing.T) {
	gen := New()
	item := mustItem(t, "ø", "latin", []string{"nor", "dan"}, "")
	req := topics.ExerciseRequest{Item: item, Siblings: siblingFixture(t), Mode: quiz.ModeSet}

	ex1, err := gen.BuildExercise(context.Background(), newRNG(9), req)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	ex2, err := gen.BuildExercise(context.Background(), newRNG(9), req)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if !reflect.DeepEqual(ex1, ex2) {
		t.Fatalf("same seed produced different exercises:\n%+v\nvs\n%+v", ex1, ex2)
	}
}

// ── error paths ──────────────────────────────────────────────────────────────

func TestBuildExercise_MalformedPayload(t *testing.T) {
	gen := New()
	cases := []struct {
		name    string
		payload []byte
	}{
		{"invalid json", []byte(`{not json`)},
		{"missing char", []byte(`{"script":"latin","languages":["spa"]}`)},
		{"missing languages", []byte(`{"char":"ñ","script":"latin","languages":[]}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			item := storage.Item{Payload: c.payload}
			req := topics.ExerciseRequest{Item: item, Mode: quiz.ModeSingle}
			_, err := gen.BuildExercise(context.Background(), newRNG(1), req)
			if err == nil {
				t.Fatalf("expected error for malformed payload")
			}
			if !errors.Is(err, topics.ErrNoContent) {
				t.Fatalf("expected errors.Is(err, topics.ErrNoContent), got %v", err)
			}
		})
	}
}

func TestBuildExercise_ModeMismatch(t *testing.T) {
	gen := New()

	t.Run("set mode on single-language item", func(t *testing.T) {
		item := mustItem(t, "ñ", "latin", []string{"spa"}, "")
		req := topics.ExerciseRequest{Item: item, Mode: quiz.ModeSet}
		_, err := gen.BuildExercise(context.Background(), newRNG(1), req)
		if err == nil {
			t.Fatalf("expected error for mode mismatch")
		}
		if errors.Is(err, topics.ErrNoContent) {
			t.Fatalf("mode mismatch must NOT be topics.ErrNoContent (that's reserved for malformed payload), got %v", err)
		}
		if !errors.Is(err, engine.ErrUnsupportedMode) {
			t.Fatalf("expected errors.Is(err, engine.ErrUnsupportedMode), got %v", err)
		}
	})

	t.Run("single mode on subgroup item", func(t *testing.T) {
		item := mustItem(t, "ø", "latin", []string{"nor", "dan"}, "")
		req := topics.ExerciseRequest{Item: item, Mode: quiz.ModeSingle}
		_, err := gen.BuildExercise(context.Background(), newRNG(1), req)
		if !errors.Is(err, engine.ErrUnsupportedMode) {
			t.Fatalf("expected errors.Is(err, engine.ErrUnsupportedMode), got %v", err)
		}
	})

	t.Run("text mode on subgroup item", func(t *testing.T) {
		item := mustItem(t, "ø", "latin", []string{"nor", "dan"}, "")
		req := topics.ExerciseRequest{Item: item, Mode: quiz.ModeText}
		_, err := gen.BuildExercise(context.Background(), newRNG(1), req)
		if !errors.Is(err, engine.ErrUnsupportedMode) {
			t.Fatalf("expected errors.Is(err, engine.ErrUnsupportedMode), got %v", err)
		}
	})
}

// ── Kind / BuildIntro ────────────────────────────────────────────────────────

func TestKind(t *testing.T) {
	if got := New().Kind(); got != "char_language" {
		t.Fatalf("Kind() = %q, want char_language", got)
	}
}

func TestBuildIntro(t *testing.T) {
	gen := New()

	t.Run("single language", func(t *testing.T) {
		item := mustItem(t, "ñ", "latin", []string{"spa"}, "eñe — Spanish")
		card, err := gen.BuildIntro(context.Background(), item)
		if err != nil {
			t.Fatalf("BuildIntro: %v", err)
		}
		want := "🔤 “ñ” — used in Spanish. eñe — Spanish"
		if card.Text != want {
			t.Fatalf("Text = %q, want %q", card.Text, want)
		}
		if card.MediaPath != "" {
			t.Fatalf("expected no media path")
		}
	})

	t.Run("two-language subgroup", func(t *testing.T) {
		item := mustItem(t, "ø", "latin", []string{"nor", "dan"}, "o with stroke")
		card, err := gen.BuildIntro(context.Background(), item)
		if err != nil {
			t.Fatalf("BuildIntro: %v", err)
		}
		want := "🔤 “ø” — used in Norwegian and Danish. o with stroke"
		if card.Text != want {
			t.Fatalf("Text = %q, want %q", card.Text, want)
		}
	})

	t.Run("four-language subgroup", func(t *testing.T) {
		item := mustItem(t, "č", "latin", []string{"ces", "slk", "hrv", "slv"}, "")
		card, err := gen.BuildIntro(context.Background(), item)
		if err != nil {
			t.Fatalf("BuildIntro: %v", err)
		}
		want := "🔤 “č” — used in Czech, Slovak, Croatian and Slovenian."
		if card.Text != want {
			t.Fatalf("Text = %q, want %q", card.Text, want)
		}
	})

	t.Run("malformed payload", func(t *testing.T) {
		item := storage.Item{Payload: []byte(`not json`)}
		_, err := gen.BuildIntro(context.Background(), item)
		if !errors.Is(err, topics.ErrNoContent) {
			t.Fatalf("expected errors.Is(err, topics.ErrNoContent), got %v", err)
		}
	})
}
