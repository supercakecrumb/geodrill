package roadside

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

// mkItem builds a storage.Item carrying a well-formed road-side payload,
// keyed the way Seed keys items (iso_a2).
func mkItem(iso, name, flag, side string, unMember, ggCoverage bool) storage.Item {
	raw, err := json.Marshal(itemPayload{
		Side:       side,
		Flag:       flag,
		Name:       name,
		UNMember:   unMember,
		GGCoverage: ggCoverage,
	})
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: iso, Label: name, Payload: raw}
}

func TestKind(t *testing.T) {
	if got := New().Kind(); got != "road_side" {
		t.Fatalf("Kind() = %q, want %q", got, "road_side")
	}
}

func TestBuildExercise_LeftDrivingCountry(t *testing.T) {
	gen := New()
	item := mkItem("GB", "United Kingdom", "🇬🇧", "L", true, true)

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{Item: item})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeSingle {
		t.Fatalf("Mode = %v, want ModeSingle", ex.Mode)
	}
	wantPrompt := "🇬🇧 United Kingdom — which side of the road do they drive on?"
	if ex.Prompt != wantPrompt {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, wantPrompt)
	}
	wantOptions := []topics.Option{
		{Key: "left", Label: "⬅️ Left"},
		{Key: "right", Label: "➡️ Right"},
	}
	if len(ex.Options) != len(wantOptions) {
		t.Fatalf("Options = %+v, want %+v", ex.Options, wantOptions)
	}
	for i := range wantOptions {
		if ex.Options[i] != wantOptions[i] {
			t.Fatalf("Options[%d] = %+v, want %+v (fixed order, never shuffled)", i, ex.Options[i], wantOptions[i])
		}
	}
	if ex.CorrectAnswer != "left" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "left")
	}
}

func TestBuildExercise_RightDrivingCountry(t *testing.T) {
	gen := New()
	item := mkItem("US", "United States", "🇺🇸", "R", true, true)

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)), topics.ExerciseRequest{Item: item})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != "right" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "right")
	}
}

// TestBuildExercise_OptionOrderNeverShuffles is the road-side-specific
// invariant (task spec): unlike specialchars/words, the two options are
// ALWAYS in the same Left-then-Right order, regardless of rng state or
// which item is being quizzed.
func TestBuildExercise_OptionOrderNeverShuffles(t *testing.T) {
	gen := New()
	items := []storage.Item{
		mkItem("GB", "United Kingdom", "🇬🇧", "L", true, true),
		mkItem("US", "United States", "🇺🇸", "R", true, true),
	}
	seeds := []int64{1, 2, 3, 42, 12345}

	for _, item := range items {
		var first []topics.Option
		for i, seed := range seeds {
			ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(seed)), topics.ExerciseRequest{Item: item})
			if err != nil {
				t.Fatalf("BuildExercise: %v", err)
			}
			if i == 0 {
				first = ex.Options
				continue
			}
			if len(ex.Options) != len(first) {
				t.Fatalf("option count changed across rng seeds: %+v vs %+v", ex.Options, first)
			}
			for j := range first {
				if ex.Options[j] != first[j] {
					t.Fatalf("option order changed across rng seeds at index %d: %+v vs %+v", j, ex.Options, first)
				}
			}
		}
		if first[0].Key != "left" || first[1].Key != "right" {
			t.Fatalf("options = %+v, want [left, right] in that order", first)
		}
	}
}

func TestBuildIntro_PlainCountry(t *testing.T) {
	gen := New()
	item := mkItem("GB", "United Kingdom", "🇬🇧", "L", true, true)

	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "🚗 🇬🇧 United Kingdom drives on the LEFT."
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
	if card.MediaPath != "" {
		t.Fatalf("road-side items are text-only, got MediaPath %q", card.MediaPath)
	}
}

func TestBuildIntro_RightDrivingCountry(t *testing.T) {
	gen := New()
	item := mkItem("US", "United States", "🇺🇸", "R", true, true)

	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "🚗 🇺🇸 United States drives on the RIGHT."
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
}

func TestBuildIntro_NonUNMemberNote(t *testing.T) {
	gen := New()
	item := mkItem("AI", "Anguilla", "🇦🇮", "L", false, true)

	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "🚗 🇦🇮 Anguilla drives on the LEFT. (not a UN member state)"
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
}

func TestBuildIntro_NoCoverageNote(t *testing.T) {
	gen := New()
	item := mkItem("AF", "Afghanistan", "🇦🇫", "R", true, false)

	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "🚗 🇦🇫 Afghanistan drives on the RIGHT. (no official Street View coverage)"
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
}

func TestBuildIntro_BothNotes(t *testing.T) {
	gen := New()
	item := mkItem("AQ", "Antarctica", "🇦🇶", "R", false, false)

	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	want := "🚗 🇦🇶 Antarctica drives on the RIGHT. (not a UN member state; no official Street View coverage)"
	if card.Text != want {
		t.Fatalf("BuildIntro text = %q, want %q", card.Text, want)
	}
}

func TestMalformedPayload(t *testing.T) {
	gen := New()

	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"invalid json", []byte(`{"side":`)},
		{"missing name", []byte(`{"side":"L","flag":"🇬🇧"}`)},
		{"invalid side", []byte(`{"side":"X","name":"Nowhere"}`)},
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
