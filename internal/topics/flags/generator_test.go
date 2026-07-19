package flags

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

const testMediaRoot = "testdata/flags"

// mkSingle builds a storage.Item carrying a well-formed single-item flags
// payload, keyed the way Seed keys single items (the country's iso2).
func mkSingle(iso2, name, image, flag, iso3 string, subdivision bool) storage.Item {
	raw, err := json.Marshal(itemPayload{
		FlagEmoji:     flag,
		Image:         image,
		Name:          name,
		ISOA2:         iso2,
		ISOA3:         iso3,
		IsSubdivision: subdivision,
	})
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: iso2, Label: name, Payload: raw}
}

// mkGroup builds a storage.Item carrying a well-formed confusable-group
// payload, keyed the way Seed keys group items (sorted iso list).
func mkGroup(countries, images []string, label string) storage.Item {
	raw, err := json.Marshal(itemPayload{Countries: countries, Images: images, Label: label})
	if err != nil {
		panic(err)
	}
	key := strings.Join(quiz.CanonSet(countries...), ",")
	return storage.Item{Key: key, Label: label, Payload: raw}
}

func TestKind(t *testing.T) {
	if got := NewWithMediaRoot(testMediaRoot).Kind(); got != Kind {
		t.Fatalf("Kind() = %q, want %q", got, Kind)
	}
}

// TestTopicMode pins the topic's exercise-mode config: both "autocomplete"
// (single items' main mode) and "set" (confusable-group items' only mode)
// must be configured, or one shape can never build (generator.go's package
// doc explains why both are needed on the SAME topic).
func TestTopicMode(t *testing.T) {
	ft := flagsTopic()
	leaf := ft[len(ft)-1]
	if leaf.Slug != LeafSlug {
		t.Fatalf("leaf slug = %q, want %q", leaf.Slug, LeafSlug)
	}
	want := map[string]bool{"autocomplete": true, "set": true}
	if len(leaf.ExerciseModes) != len(want) {
		t.Fatalf("exercise_modes = %v, want %v", leaf.ExerciseModes, want)
	}
	for _, m := range leaf.ExerciseModes {
		if !want[m] {
			t.Fatalf("unexpected exercise mode %q in %v", m, leaf.ExerciseModes)
		}
	}
	if !leaf.IsQuizzable {
		t.Fatalf("leaf should be quizzable")
	}
	root := ft[0]
	if root.Slug != RootSlug || root.QuizKind != "" {
		t.Fatalf("root = %+v, want a plain container named %q", root, RootSlug)
	}
}

// TestSingleWithImage_Autocomplete: a single item with an ingested image
// builds a ModeText exercise with MediaPath set to the joined media-root
// path, a flat "Which country is this?" prompt, and accepts the country's
// name/iso2/iso3.
func TestSingleWithImage_Autocomplete(t *testing.T) {
	gen := NewWithMediaRoot(testMediaRoot)
	item := mkSingle("FR", "France", "fr.png", "🇫🇷", "FRA", false)

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if want := "Which country is this?"; ex.Prompt != want {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, want)
	}
	if want := filepath.Join(testMediaRoot, "fr.png"); ex.MediaPath != want {
		t.Fatalf("MediaPath = %q, want %q", ex.MediaPath, want)
	}
	if ex.CorrectAnswer != "France" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "France")
	}
	assertAccepts(t, ex.Accept, "France", "FR", "FRA")

	if ok, _ := (quiz.TextMatcher{Accept: ex.Accept, MaxEdits: 2}).Match("france"); !ok {
		t.Fatalf("typed %q should match accepted spellings %v", "france", ex.Accept)
	}
}

// TestSingleWithoutImage_EmojiFallback: a single item with no ingested
// image yet builds the same ModeText exercise but with no MediaPath and an
// emoji-prefixed prompt (design §6's content-availability fallback).
func TestSingleWithoutImage_EmojiFallback(t *testing.T) {
	gen := NewWithMediaRoot(testMediaRoot)
	item := mkSingle("FR", "France", "", "🇫🇷", "FRA", false)

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.MediaPath != "" {
		t.Fatalf("MediaPath = %q, want empty (no ingested image)", ex.MediaPath)
	}
	if want := "🇫🇷 — which country is this?"; ex.Prompt != want {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, want)
	}
}

// TestBuildIntro_Single covers both the photo and emoji-fallback intro
// cases.
func TestBuildIntro_Single(t *testing.T) {
	gen := NewWithMediaRoot(testMediaRoot)

	withImage := mkSingle("FR", "France", "fr.png", "🇫🇷", "FRA", false)
	card, err := gen.BuildIntro(context.Background(), withImage)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if want := filepath.Join(testMediaRoot, "fr.png"); card.MediaPath != want {
		t.Fatalf("MediaPath = %q, want %q", card.MediaPath, want)
	}
	if want := "🇫🇷 This is France's flag."; card.Text != want {
		t.Fatalf("Text = %q, want %q", card.Text, want)
	}

	withoutImage := mkSingle("FR", "France", "", "🇫🇷", "FRA", false)
	card, err = gen.BuildIntro(context.Background(), withoutImage)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if card.MediaPath != "" {
		t.Fatalf("MediaPath = %q, want empty", card.MediaPath)
	}
}

// TestConfusableSetChoice: a confusable-group item builds a ModeSet
// exercise whose MediaPath is one of the group's own images, whose target
// OptionSet is the exact member set with the authored label, and whose
// distractor sets are same-size swapped-member alternatives.
func TestConfusableSetChoice(t *testing.T) {
	gen := NewWithMediaRoot(testMediaRoot)
	target := mkGroup([]string{"TD", "RO"}, []string{"td.png", "ro.png"}, "Chad / Romania")
	siblings := []storage.Item{
		mkGroup([]string{"ID", "MC", "PL"}, []string{"id.png", "mc.png", "pl.png"}, "Indonesia / Monaco / Poland"),
		mkSingle("FR", "France", "fr.png", "🇫🇷", "FRA", false),
		mkSingle("DE", "Germany", "de.png", "🇩🇪", "DEU", false),
	}

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(3)),
		topics.ExerciseRequest{Item: target, Siblings: siblings, Mode: quiz.ModeSet})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeSet {
		t.Fatalf("Mode = %v, want ModeSet", ex.Mode)
	}
	if ex.MediaPath != filepath.Join(testMediaRoot, "td.png") && ex.MediaPath != filepath.Join(testMediaRoot, "ro.png") {
		t.Fatalf("MediaPath = %q, want td.png or ro.png under the media root", ex.MediaPath)
	}

	wantTarget := strings.Join(quiz.CanonSet("TD", "RO"), ",")
	var foundTarget bool
	for _, optSet := range ex.OptionSets {
		if strings.Join(quiz.CanonSet(optSet.Keys...), ",") == wantTarget {
			foundTarget = true
			if optSet.Label != "Chad / Romania" {
				t.Fatalf("target option label = %q, want %q", optSet.Label, "Chad / Romania")
			}
		}
	}
	if !foundTarget {
		t.Fatalf("target set {TD,RO} missing from OptionSets %+v", ex.OptionSets)
	}
	if len(ex.OptionSets) < 2 {
		t.Fatalf("expected at least one distractor set alongside the target, got %d option sets", len(ex.OptionSets))
	}
	for _, optSet := range ex.OptionSets {
		if len(optSet.Keys) != 2 {
			t.Fatalf("option set %+v has %d keys, want 2 (same size as target)", optSet, len(optSet.Keys))
		}
	}
	if ex.CorrectAnswer != "RO,TD" {
		t.Fatalf("CorrectAnswer = %q, want %q (canon sorted)", ex.CorrectAnswer, "RO,TD")
	}
}

// TestConfusableSetChoice_ModeTextFails: a confusable-group item can never
// build under ModeText — set-choice-only (design §6) — so modeRotationOrder
// (internal/study) can rely on it always failing there and falling through
// to "set".
func TestConfusableSetChoice_ModeTextFails(t *testing.T) {
	gen := NewWithMediaRoot(testMediaRoot)
	item := mkGroup([]string{"TD", "RO"}, []string{"td.png", "ro.png"}, "Chad / Romania")
	if _, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText}); err == nil {
		t.Fatalf("expected an error building a group item under ModeText")
	}
}

// TestSingleItem_ModeSetFails: a single item can never build under ModeSet
// — the mirror image of the above.
func TestSingleItem_ModeSetFails(t *testing.T) {
	gen := NewWithMediaRoot(testMediaRoot)
	item := mkSingle("FR", "France", "fr.png", "🇫🇷", "FRA", false)
	if _, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeSet}); err == nil {
		t.Fatalf("expected an error building a single item under ModeSet")
	}
}

func TestMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"invalid json", []byte(`{"iso_a2":`)},
		{"single missing name", []byte(`{"iso_a2":"FR"}`)},
		{"single missing iso2", []byte(`{"name":"France"}`)},
		{"group image/country mismatch", []byte(`{"countries":["TD","RO"],"images":["td.png"],"label":"x"}`)},
		{"group missing label", []byte(`{"countries":["TD","RO"],"images":["td.png","ro.png"]}`)},
	}
	gen := NewWithMediaRoot(testMediaRoot)
	for _, tc := range cases {
		item := storage.Item{Key: "bad", Payload: tc.payload}
		if _, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
			topics.ExerciseRequest{Item: item, Mode: quiz.ModeText}); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("%s: BuildExercise err = %v, want ErrMalformedPayload", tc.name, err)
		}
		if _, err := gen.BuildIntro(context.Background(), item); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("%s: BuildIntro err = %v, want ErrMalformedPayload", tc.name, err)
		}
	}
}

// TestDeterminism spot-checks that the same rng seed produces the same
// confusable set-choice exercise across repeated calls (mirrors
// cities.TestSingleFallbackDeterministic / specialchars' determinism
// convention).
func TestDeterminism(t *testing.T) {
	gen := NewWithMediaRoot(testMediaRoot)
	target := mkGroup([]string{"TD", "RO"}, []string{"td.png", "ro.png"}, "Chad / Romania")
	siblings := []storage.Item{
		mkGroup([]string{"ID", "MC", "PL"}, []string{"id.png", "mc.png", "pl.png"}, "Indonesia / Monaco / Poland"),
		mkGroup([]string{"NL", "LU"}, []string{"nl.png", "lu.png"}, "Netherlands / Luxembourg"),
		mkSingle("FR", "France", "fr.png", "🇫🇷", "FRA", false),
		mkSingle("DE", "Germany", "de.png", "🇩🇪", "DEU", false),
	}

	var first topics.Exercise
	for i := 0; i < 3; i++ {
		ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(42)),
			topics.ExerciseRequest{Item: target, Siblings: siblings, Mode: quiz.ModeSet})
		if err != nil {
			t.Fatalf("BuildExercise: %v", err)
		}
		if i == 0 {
			first = ex
			continue
		}
		if ex.MediaPath != first.MediaPath {
			t.Fatalf("MediaPath not deterministic: %q vs %q", ex.MediaPath, first.MediaPath)
		}
		if len(ex.OptionSets) != len(first.OptionSets) {
			t.Fatalf("option set count not deterministic: %d vs %d", len(ex.OptionSets), len(first.OptionSets))
		}
		for j := range first.OptionSets {
			if ex.OptionSets[j].Label != first.OptionSets[j].Label {
				t.Fatalf("option set order not deterministic at %d across same rng seed", j)
			}
		}
	}
}

func assertAccepts(t *testing.T, accept []string, want ...string) {
	t.Helper()
	have := make(map[string]bool, len(accept))
	for _, a := range accept {
		have[a] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Fatalf("Accept %v missing %q", accept, w)
		}
	}
}
