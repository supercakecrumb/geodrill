package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// ── test fixture descriptor ────────────────────────────────────────────────

// errBadPayload is the fixture topic's malformed-payload sentinel, standing
// in for the per-topic sentinels real descriptors wrap (words/roadside's
// ErrMalformedPayload, specialchars' topics.ErrNoContent chain).
var errBadPayload = errors.New("enginetest: bad payload")

type testPayload struct {
	Keys    []string `json:"keys"`
	Group   string   `json:"group"`
	Subject string   `json:"subject"`
}

func testParse(raw []byte) (Card, error) {
	var p testPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Card{}, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	if len(p.Keys) == 0 {
		return Card{}, errBadPayload
	}
	return Card{Keys: p.Keys, Group: p.Group, Subject: p.Subject, Intro: "intro: " + p.Subject}, nil
}

func sampledDescriptor() Descriptor {
	return Descriptor{
		QuizKind:     "test_kind",
		Parse:        testParse,
		Labels:       map[string]string{"apple": "Apple", "pear": "Pear", "plum": "Plum", "fig": "Fig", "kiwi": "Kiwi"},
		PromptSingle: "Which fruit is “%s”?",
		PromptText:   "Type the fruit for “%s”:",
		Distractors:  DistractorPolicy{Max: 3, SameGroup: true},
		Accept:       func(key string) []string { return []string{key, "the " + key} },
	}
}

func mkTestItem(t *testing.T, group, subject string, keys ...string) storage.Item {
	t.Helper()
	raw, err := json.Marshal(testPayload{Keys: keys, Group: group, Subject: subject})
	if err != nil {
		t.Fatalf("marshal test payload: %v", err)
	}
	return storage.Item{Key: subject, Label: subject, Payload: raw}
}

func newRNG(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// ── sampled single-choice ──────────────────────────────────────────────────

func TestBuildExercise_Sampled(t *testing.T) {
	gen := New(sampledDescriptor())
	target := mkTestItem(t, "g1", "apple", "apple")
	siblings := []storage.Item{
		mkTestItem(t, "g1", "pear", "pear"),
		mkTestItem(t, "g1", "plum", "plum"),
		mkTestItem(t, "g1", "fig+kiwi", "fig", "kiwi"), // set-shaped sibling still feeds the pool
		mkTestItem(t, "g1", "apple-dup", "apple"),      // target's own key never becomes a distractor
		mkTestItem(t, "g2", "mango", "mango"),          // other group — excluded by SameGroup
		{Key: "bad", Payload: []byte(`{malformed`)},    // malformed sibling skipped, not fatal
	}

	ex, err := gen.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: target, Siblings: siblings})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeSingle {
		t.Fatalf("Mode = %v, want ModeSingle", ex.Mode)
	}
	if ex.Prompt != "Which fruit is “apple”?" {
		t.Fatalf("Prompt = %q", ex.Prompt)
	}
	if ex.CorrectAnswer != "apple" {
		t.Fatalf("CorrectAnswer = %q, want apple", ex.CorrectAnswer)
	}
	if len(ex.Options) != 4 { // target + Max(3) distractors from {pear,plum,fig,kiwi}
		t.Fatalf("len(Options) = %d, want 4: %+v", len(ex.Options), ex.Options)
	}
	seen := map[string]int{}
	for _, o := range ex.Options {
		seen[o.Key]++
		if o.Key == "mango" {
			t.Fatalf("other-group distractor %q leaked into options: %+v", o.Key, ex.Options)
		}
		if o.Key == "apple" && o.Label != "Apple" {
			t.Fatalf("target label = %q, want Apple", o.Label)
		}
	}
	if seen["apple"] != 1 {
		t.Fatalf("target present %d times, want exactly 1: %+v", seen["apple"], ex.Options)
	}
	for k, n := range seen {
		if n > 1 {
			t.Fatalf("duplicate option key %q: %+v", k, ex.Options)
		}
	}
}

func TestBuildExercise_Sampled_AnyGroupWhenPolicyOff(t *testing.T) {
	d := sampledDescriptor()
	d.Distractors.SameGroup = false
	gen := New(d)
	target := mkTestItem(t, "g1", "apple", "apple")
	siblings := []storage.Item{mkTestItem(t, "g2", "mango", "mango")}

	ex, err := gen.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: target, Siblings: siblings})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	var found bool
	for _, o := range ex.Options {
		if o.Key == "mango" {
			found = true
			if o.Label != "MANGO" {
				t.Fatalf("unlabeled key label = %q, want MANGO (uppercase fallback)", o.Label)
			}
		}
	}
	if !found {
		t.Fatalf("SameGroup=false must admit other-group distractors: %+v", ex.Options)
	}
}

func TestBuildExercise_Sampled_Deterministic(t *testing.T) {
	gen := New(sampledDescriptor())
	req := topics.ExerciseRequest{
		Item: mkTestItem(t, "g1", "apple", "apple"),
		Siblings: []storage.Item{
			mkTestItem(t, "g1", "pear", "pear"),
			mkTestItem(t, "g1", "plum", "plum"),
			mkTestItem(t, "g1", "fig", "fig"),
			mkTestItem(t, "g1", "kiwi", "kiwi"),
			mkTestItem(t, "g1", "lime", "lime"),
		},
	}

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

	// The pool must depend on the SET of siblings, not their incoming order.
	reversed := append([]storage.Item(nil), req.Siblings...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	ex3, err := gen.BuildExercise(context.Background(), newRNG(7), topics.ExerciseRequest{Item: req.Item, Siblings: reversed})
	if err != nil {
		t.Fatalf("reversed build: %v", err)
	}
	if !reflect.DeepEqual(ex1, ex3) {
		t.Fatalf("sibling order changed the exercise:\n%+v\nvs\n%+v", ex1, ex3)
	}
}

// ── fixed options ──────────────────────────────────────────────────────────

func fixedDescriptor() Descriptor {
	d := Descriptor{
		QuizKind:     "test_fixed",
		Parse:        testParse,
		PromptSingle: "Which way for %s?",
		FixedOptions: []topics.Option{
			{Key: "left", Label: "⬅️ Left"},
			{Key: "right", Label: "➡️ Right"},
		},
	}
	return d
}

func TestBuildExercise_FixedOptions_NeverShuffled(t *testing.T) {
	gen := New(fixedDescriptor())
	item := mkTestItem(t, "", "GB", "left")

	for _, seed := range []int64{1, 2, 3, 42, 12345} {
		ex, err := gen.BuildExercise(context.Background(), newRNG(seed), topics.ExerciseRequest{Item: item})
		if err != nil {
			t.Fatalf("BuildExercise: %v", err)
		}
		if ex.Prompt != "Which way for GB?" {
			t.Fatalf("Prompt = %q", ex.Prompt)
		}
		if ex.CorrectAnswer != "left" {
			t.Fatalf("CorrectAnswer = %q, want left", ex.CorrectAnswer)
		}
		if len(ex.Options) != 2 || ex.Options[0].Key != "left" || ex.Options[1].Key != "right" {
			t.Fatalf("options = %+v, want fixed [left, right] order for every rng seed", ex.Options)
		}
	}
}

// ── text mode ──────────────────────────────────────────────────────────────

func TestBuildExercise_Text(t *testing.T) {
	gen := New(sampledDescriptor())
	item := mkTestItem(t, "g1", "apple", "apple")

	ex, err := gen.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if ex.Prompt != "Type the fruit for “apple”:" {
		t.Fatalf("Prompt = %q", ex.Prompt)
	}
	if ex.CorrectAnswer != "Apple" {
		t.Fatalf("CorrectAnswer = %q, want Apple (display label)", ex.CorrectAnswer)
	}
	if !reflect.DeepEqual(ex.Accept, []string{"apple", "the apple"}) {
		t.Fatalf("Accept = %v", ex.Accept)
	}
}

// ── mode/shape dispatch ────────────────────────────────────────────────────

func TestBuildExercise_SetShapedDispatch(t *testing.T) {
	d := sampledDescriptor()
	var gotCard Card
	d.BuildSet = func(_ *rand.Rand, card Card, _ []storage.Item) (topics.Exercise, error) {
		gotCard = card
		return topics.Exercise{Mode: quiz.ModeSet, CorrectAnswer: "fig,kiwi"}, nil
	}
	gen := New(d)
	item := mkTestItem(t, "g1", "fig+kiwi", "fig", "kiwi")

	ex, err := gen.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: item, Mode: quiz.ModeSet})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != "fig,kiwi" {
		t.Fatalf("BuildSet result not returned: %+v", ex)
	}
	if !reflect.DeepEqual(gotCard.Keys, []string{"fig", "kiwi"}) {
		t.Fatalf("BuildSet card keys = %v (declared order expected)", gotCard.Keys)
	}
}

func TestBuildExercise_UnsupportedModes(t *testing.T) {
	d := sampledDescriptor()
	d.BuildSet = func(_ *rand.Rand, _ Card, _ []storage.Item) (topics.Exercise, error) {
		return topics.Exercise{}, nil
	}
	gen := New(d)
	single := mkTestItem(t, "g1", "apple", "apple")
	set := mkTestItem(t, "g1", "fig+kiwi", "fig", "kiwi")

	cases := []struct {
		name string
		item storage.Item
		mode quiz.Mode
	}{
		{"set mode on single-shaped item", single, quiz.ModeSet},
		{"single mode on set-shaped item", set, quiz.ModeSingle},
		{"text mode on set-shaped item", set, quiz.ModeText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := gen.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: c.item, Mode: c.mode})
			if !errors.Is(err, ErrUnsupportedMode) {
				t.Fatalf("err = %v, want ErrUnsupportedMode", err)
			}
			if errors.Is(err, topics.ErrNoContent) {
				t.Fatalf("mode mismatch must not be topics.ErrNoContent: %v", err)
			}
		})
	}

	t.Run("text mode without Accept", func(t *testing.T) {
		d2 := sampledDescriptor()
		d2.Accept = nil
		d2.PromptText = ""
		gen2 := New(d2)
		_, err := gen2.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: single, Mode: quiz.ModeText})
		if !errors.Is(err, ErrUnsupportedMode) {
			t.Fatalf("err = %v, want ErrUnsupportedMode", err)
		}
	})

	t.Run("set mode on set-shaped item without BuildSet", func(t *testing.T) {
		gen2 := New(sampledDescriptor())
		_, err := gen2.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: set, Mode: quiz.ModeSet})
		if !errors.Is(err, ErrUnsupportedMode) {
			t.Fatalf("err = %v, want ErrUnsupportedMode", err)
		}
	})
}

// ── parse errors / intro ───────────────────────────────────────────────────

func TestBuildExercise_ParseErrorPropagates(t *testing.T) {
	gen := New(sampledDescriptor())
	item := storage.Item{Key: "bad", Payload: []byte(`{broken`)}

	_, err := gen.BuildExercise(context.Background(), newRNG(1), topics.ExerciseRequest{Item: item})
	if !errors.Is(err, errBadPayload) {
		t.Fatalf("BuildExercise err = %v, want chain containing the topic's own sentinel", err)
	}
	_, err = gen.BuildIntro(context.Background(), item)
	if !errors.Is(err, errBadPayload) {
		t.Fatalf("BuildIntro err = %v, want chain containing the topic's own sentinel", err)
	}
}

func TestBuildIntro(t *testing.T) {
	gen := New(sampledDescriptor())
	card, err := gen.BuildIntro(context.Background(), mkTestItem(t, "g1", "apple", "apple"))
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if card.Text != "intro: apple" {
		t.Fatalf("intro = %q", card.Text)
	}
	if card.MediaPath != "" {
		t.Fatalf("unexpected MediaPath %q", card.MediaPath)
	}
}

func TestKind(t *testing.T) {
	if got := New(sampledDescriptor()).Kind(); got != "test_kind" {
		t.Fatalf("Kind() = %q, want test_kind", got)
	}
}
