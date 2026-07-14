package train

import (
	"testing"

	"github.com/google/uuid"
)

func TestParseCallbackRoundTrip(t *testing.T) {
	id := uuid.New()

	ans := AnswerData(id, "por")
	if len(ans) > 64 {
		t.Fatalf("answer callback data %q exceeds Telegram's 64-byte limit", ans)
	}
	cb, ok := ParseCallback(ans)
	if !ok || cb.Kind != PrefixAnswer || cb.Practice || cb.ExerciseID != id || cb.Key != "por" {
		t.Fatalf("ParseCallback(%q) = %+v, %v", ans, cb, ok)
	}

	prac := PracticeData(id, "cmn")
	cb, ok = ParseCallback(prac)
	if !ok || cb.Kind != PrefixPractice || !cb.Practice || cb.ExerciseID != id || cb.Key != "cmn" {
		t.Fatalf("ParseCallback(%q) = %+v, %v", prac, cb, ok)
	}
}

func TestParseCallbackRejects(t *testing.T) {
	id := uuid.New().String()
	cases := []string{
		DataNoop,
		"",
		"ans:" + id,          // missing key
		"ans:not-a-uuid:por", // bad uuid
		"ans:" + id + ":",    // empty key
		"foo:" + id + ":por", // unknown prefix
		"deck:" + id,         // app callback, not an answer
	}
	for _, c := range cases {
		if _, ok := ParseCallback(c); ok {
			t.Fatalf("ParseCallback(%q) should be rejected", c)
		}
	}
}

func TestMarkAndDecorate(t *testing.T) {
	// chosen wrong (spa), correct is por.
	if got := markFor("por", "spa", "por"); got != MarkCorrect {
		t.Fatalf("correct option should be MarkCorrect, got %v", got)
	}
	if got := markFor("spa", "spa", "por"); got != MarkWrong {
		t.Fatalf("wrongly-tapped option should be MarkWrong, got %v", got)
	}
	if got := markFor("ita", "spa", "por"); got != MarkNone {
		t.Fatalf("untouched option should be MarkNone, got %v", got)
	}
	// when the tap is correct, only the correct mark applies.
	if got := markFor("por", "por", "por"); got != MarkCorrect {
		t.Fatalf("correct tap should be MarkCorrect, got %v", got)
	}

	if got := DecorateLabel("Portuguese", MarkCorrect); got != "✅ Portuguese" {
		t.Fatalf("decorate correct = %q", got)
	}
	if got := DecorateLabel("Spanish", MarkWrong); got != "❌ Spanish" {
		t.Fatalf("decorate wrong = %q", got)
	}
	if got := DecorateLabel("Italian", MarkNone); got != "Italian" {
		t.Fatalf("decorate none = %q", got)
	}
}

func TestGradedButtons(t *testing.T) {
	opts := []byte(`[{"key":"spa","label":"Spanish"},{"key":"por","label":"Portuguese"},{"key":"ita","label":"Italian"}]`)
	// user tapped spa, correct was por
	got, err := gradedButtons(opts, "spa", "por")
	if err != nil {
		t.Fatalf("gradedButtons: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 buttons, got %d", len(got))
	}
	corrects, wrongs := 0, 0
	for _, b := range got {
		switch b.Mark {
		case MarkCorrect:
			corrects++
			if b.Label != "✅ Portuguese" {
				t.Fatalf("correct label = %q", b.Label)
			}
		case MarkWrong:
			wrongs++
			if b.Label != "❌ Spanish" {
				t.Fatalf("wrong label = %q", b.Label)
			}
		}
	}
	if corrects != 1 || wrongs != 1 {
		t.Fatalf("want exactly one ✅ and one ❌, got %d/%d", corrects, wrongs)
	}
}
