package study

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestGradeIndexedSingle(t *testing.T) {
	options := mustJSON(t, []singleOptionJSON{
		{Key: "rus", Label: "Russian"},
		{Key: "ukr", Label: "Ukrainian"},
		{Key: "srp", Label: "Serbian"},
	})
	ex := storage.Exercise{Mode: int16(quiz.ModeSingle), Options: options, CorrectAnswer: "ukr"}

	correct, chosen, graded, err := gradeIndexed(ex, 1)
	if err != nil {
		t.Fatalf("gradeIndexed: %v", err)
	}
	if !correct || chosen != "ukr" {
		t.Fatalf("expected correct=true chosen=ukr, got correct=%v chosen=%q", correct, chosen)
	}
	if len(graded) != 3 {
		t.Fatalf("expected 3 graded options, got %d", len(graded))
	}
	if graded[1].Mark != telegram.MarkCorrect {
		t.Fatalf("tapped correct option should be MarkCorrect, got %v", graded[1].Mark)
	}
	if graded[0].Mark != telegram.MarkNone || graded[2].Mark != telegram.MarkNone {
		t.Fatalf("untapped wrong options should be MarkNone, got %v / %v", graded[0].Mark, graded[2].Mark)
	}

	// wrong tap
	correct2, chosen2, graded2, err := gradeIndexed(ex, 0)
	if err != nil {
		t.Fatalf("gradeIndexed (wrong): %v", err)
	}
	if correct2 || chosen2 != "rus" {
		t.Fatalf("expected correct=false chosen=rus, got correct=%v chosen=%q", correct2, chosen2)
	}
	if graded2[0].Mark != telegram.MarkWrong {
		t.Fatalf("tapped wrong option should be MarkWrong, got %v", graded2[0].Mark)
	}
	if graded2[1].Mark != telegram.MarkCorrect {
		t.Fatalf("correct option should still be marked correct, got %v", graded2[1].Mark)
	}

	// out-of-range index grades wrong, doesn't error
	correct3, chosen3, _, err := gradeIndexed(ex, 99)
	if err != nil {
		t.Fatalf("gradeIndexed (out of range): %v", err)
	}
	if correct3 || chosen3 != "" {
		t.Fatalf("out-of-range index should grade wrong with empty chosen, got correct=%v chosen=%q", correct3, chosen3)
	}
}

func TestGradeIndexedSet(t *testing.T) {
	// The persisted CorrectAnswer is the canonical (sorted) join, matching
	// how serializeExercise/specialchars.buildSetMCQ both persist it.
	options := mustJSON(t, []setOptionJSON{
		{Keys: []string{"pol"}, Label: "Polish only"},
		// deliberately unsorted/duplicated input order — canonicalSetString
		// must still match the canonical "dan,nor" persisted CorrectAnswer.
		{Keys: []string{"dan", "nor", "nor"}, Label: "Norwegian / Danish"},
	})
	ex := storage.Exercise{Mode: int16(quiz.ModeSet), Options: options, CorrectAnswer: "dan,nor"}

	correct, chosen, graded, err := gradeIndexed(ex, 1)
	if err != nil {
		t.Fatalf("gradeIndexed: %v", err)
	}
	if !correct {
		t.Fatalf("expected the canonicalized set to match regardless of input order/dupes")
	}
	if chosen != "dan,nor" {
		t.Fatalf("chosen should be the canonical form, got %q", chosen)
	}
	if graded[1].Mark != telegram.MarkCorrect || graded[0].Mark != telegram.MarkNone {
		t.Fatalf("unexpected marks: %+v", graded)
	}

	correctWrong, _, gradedWrong, err := gradeIndexed(ex, 0)
	if err != nil {
		t.Fatalf("gradeIndexed (wrong set): %v", err)
	}
	if correctWrong {
		t.Fatalf("single-member set should not match the two-member correct answer")
	}
	if gradedWrong[0].Mark != telegram.MarkWrong {
		t.Fatalf("tapped wrong set option should be MarkWrong, got %v", gradedWrong[0].Mark)
	}
}

func TestCanonicalSetString(t *testing.T) {
	a := canonicalSetString([]string{"nor", "dan"})
	b := canonicalSetString([]string{"dan", "nor", "dan"})
	if a != b {
		t.Fatalf("canonicalSetString should be order/dedup-insensitive: %q vs %q", a, b)
	}
	if a != "dan,nor" {
		t.Fatalf("expected sorted \"dan,nor\", got %q", a)
	}
}

func TestSerializeExerciseRoundTrip(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		ex := topicsExerciseSingle()
		optionsJSON, correctAnswer, err := serializeExercise(ex)
		if err != nil {
			t.Fatalf("serialize: %v", err)
		}
		row := storage.Exercise{Mode: int16(quiz.ModeSingle), Options: optionsJSON, CorrectAnswer: correctAnswer}
		correct, chosen, _, err := gradeIndexed(row, 0)
		if err != nil {
			t.Fatalf("gradeIndexed after round trip: %v", err)
		}
		if !correct || chosen != "spa" {
			t.Fatalf("round trip mismatch: correct=%v chosen=%q", correct, chosen)
		}
	})

	t.Run("set", func(t *testing.T) {
		ex := topicsExerciseSet()
		optionsJSON, correctAnswer, err := serializeExercise(ex)
		if err != nil {
			t.Fatalf("serialize: %v", err)
		}
		row := storage.Exercise{Mode: int16(quiz.ModeSet), Options: optionsJSON, CorrectAnswer: correctAnswer}
		correct, _, _, err := gradeIndexed(row, 0)
		if err != nil {
			t.Fatalf("gradeIndexed after round trip: %v", err)
		}
		if !correct {
			t.Fatalf("expected the persisted target set (index 0) to grade correct")
		}
	})

	t.Run("text", func(t *testing.T) {
		ex := topicsExerciseText()
		optionsJSON, correctAnswer, err := serializeExercise(ex)
		if err != nil {
			t.Fatalf("serialize: %v", err)
		}
		if correctAnswer != "Spanish" {
			t.Fatalf("expected correct answer %q, got %q", "Spanish", correctAnswer)
		}
		correct, err := matchTypedText(optionsJSON, "spanish")
		if err != nil {
			t.Fatalf("matchTypedText: %v", err)
		}
		if !correct {
			t.Fatalf("case-insensitive accepted spelling should match")
		}
		wrong, err := matchTypedText(optionsJSON, "portuguese")
		if err != nil {
			t.Fatalf("matchTypedText (wrong): %v", err)
		}
		if wrong {
			t.Fatalf("an unrelated word should not match")
		}
	})
}

func TestMatchTypedTextTolerance(t *testing.T) {
	optionsJSON := mustJSON(t, textOptionsJSON{Accept: []string{"Norwegian", "Norsk"}})
	// one edit away (missing a letter) still matches within MaxEdits=2.
	correct, err := matchTypedText(optionsJSON, "Norwegan")
	if err != nil {
		t.Fatalf("matchTypedText: %v", err)
	}
	if !correct {
		t.Fatalf("expected a near-miss spelling within edit tolerance to match")
	}
	empty, err := matchTypedText(optionsJSON, "")
	if err != nil {
		t.Fatalf("matchTypedText (empty): %v", err)
	}
	if empty {
		t.Fatalf("empty input must never match")
	}
}

func TestAnswerTextRevealsCorrectAnswerOnWrongTextMode(t *testing.T) {
	ex := storage.Exercise{
		Mode:          int16(quiz.ModeText),
		Prompt:        "What is the capital of Australia?",
		CorrectAnswer: "Canberra",
	}

	wrong := answerText(ex, false, "")
	if !strings.Contains(wrong, "✅ Correct answer: Canberra") {
		t.Fatalf("expected a wrong text-mode answer to reveal the correct answer, got %q", wrong)
	}

	right := answerText(ex, true, "")
	if strings.Contains(right, "Correct answer:") {
		t.Fatalf("a correct answer must not show the correct-answer line, got %q", right)
	}

	// A tip (when present) still follows the correct-answer line.
	wrongWithTip := answerText(ex, false, "some tip")
	wantOrder := strings.Index(wrongWithTip, "Correct answer:")
	tipOrder := strings.Index(wrongWithTip, "💡 some tip")
	if wantOrder == -1 || tipOrder == -1 || wantOrder > tipOrder {
		t.Fatalf("expected correct-answer line before the tip, got %q", wrongWithTip)
	}
}

func TestAnswerTextChoiceModeReliesOnOptionMarks(t *testing.T) {
	// Choice-mode (ModeSingle/ModeSet) exercises already highlight the
	// correct option via ✅ marks (gradeIndexed/markFor) — the text-line gap
	// this closes is ModeText-only (no option buttons to highlight), so a
	// wrong choice-mode answer gets no "Correct answer:" line.
	ex := storage.Exercise{Mode: int16(quiz.ModeSingle), Prompt: "Which language uses \"ñ\"?", CorrectAnswer: "spa"}
	wrong := answerText(ex, false, "")
	if strings.Contains(wrong, "Correct answer:") {
		t.Fatalf("choice-mode exercises should rely on the existing ✅ option mark, not the text line, got %q", wrong)
	}
}

func TestModeRotationOrder(t *testing.T) {
	modes := []string{"single", "set", "text"}
	cases := []struct {
		reps int
		want []string
	}{
		{reps: 0, want: []string{"single", "set", "text"}},
		{reps: 1, want: []string{"set", "text", "single"}},
		{reps: 2, want: []string{"text", "single", "set"}},
		{reps: 3, want: []string{"single", "set", "text"}}, // wraps back to reps=0's order
	}
	for _, c := range cases {
		got := modeRotationOrder(modes, c.reps)
		if len(got) != len(c.want) {
			t.Fatalf("reps=%d: length mismatch got=%v want=%v", c.reps, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("reps=%d: got=%v want=%v", c.reps, got, c.want)
			}
		}
	}

	// every configured mode must appear exactly once regardless of rotation,
	// so buildExercise's "try until one succeeds" loop can always reach
	// a mode fitting a single-mode item's shape (e.g. a specialchars
	// subgroup item that only ever builds under ModeSet).
	single := modeRotationOrder([]string{"set"}, 7)
	if len(single) != 1 || single[0] != "set" {
		t.Fatalf("single-mode topic should always yield that one mode, got %v", single)
	}

	// empty/nil modes defensively fall back to {"single"}.
	fallback := modeRotationOrder(nil, 5)
	if len(fallback) != 1 || fallback[0] != "single" {
		t.Fatalf("empty modes should fall back to [single], got %v", fallback)
	}
}

// ── fixtures ─────────────────────────────────────────────────────────────

func topicsExerciseSingle() topics.Exercise {
	return topics.Exercise{
		Mode:          quiz.ModeSingle,
		Prompt:        "Which language uses \"ñ\"?",
		Options:       []topics.Option{{Key: "spa", Label: "Spanish"}, {Key: "por", Label: "Portuguese"}},
		CorrectAnswer: "spa",
	}
}

func topicsExerciseSet() topics.Exercise {
	target := quiz.CanonSet("nor", "dan")
	return topics.Exercise{
		Mode:   quiz.ModeSet,
		Prompt: "Which languages use \"ø\"?",
		OptionSets: []topics.OptionSet{
			{Keys: target, Label: "Norwegian / Danish"},
			{Keys: quiz.CanonSet("pol"), Label: "Polish"},
		},
		CorrectAnswer: canonicalSetString(target),
	}
}

func topicsExerciseText() topics.Exercise {
	return topics.Exercise{
		Mode:          quiz.ModeText,
		Prompt:        "Type the language that uses \"ñ\":",
		Accept:        []string{"Spanish", "Espanol", "spa"},
		CorrectAnswer: "Spanish",
	}
}
