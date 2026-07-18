package tips

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/supercakecrumb/engram/quiz"
)

// deckLanguages mirrors the decks in tipDecks (data.go) — every language in
// a rolled-out deck must have dataset coverage.
var deckLanguages = []string{
	"spa", "por", "ita", "fra", "cat", "ron", // romance
}

func TestTellMatches(t *testing.T) {
	cases := []struct {
		name     string
		tell     Tell
		sentence string
		want     bool
	}{
		{"letters hit", Tell{Kind: KindLetters, Pattern: "ãõ"}, "Ele não sabe.", true},
		{"letters miss", Tell{Kind: KindLetters, Pattern: "ãõ"}, "El no sabe.", false},
		{"letters case-folded", Tell{Kind: KindLetters, Pattern: "ç"}, "Ça va? ÇA VA!", true},
		{"substring hit", Tell{Kind: KindSubstring, Pattern: "sz"}, "Proszę bardzo.", true},
		{"substring miss", Tell{Kind: KindSubstring, Pattern: "sz"}, "Prosím.", false},
		{"word hit with punctuation", Tell{Kind: KindWord, Pattern: "af"}, "Han er søn af kongen.", true},
		{"word hit at comma", Tell{Kind: KindWord, Pattern: "af"}, "Af, sagde han.", true},
		{"word not inside another word", Tell{Kind: KindWord, Pattern: "af"}, "Kafka skrev meget.", false},
		{"word case-insensitive", Tell{Kind: KindWord, Pattern: "och"}, "Och sedan då?", true},
		{"suffix hit", Tell{Kind: KindSuffix, Pattern: "ção"}, "Que informação boa!", true},
		{"suffix needs a stem", Tell{Kind: KindSuffix, Pattern: "ção"}, "ção ção", false},
		{"suffix not mid-word", Tell{Kind: KindSuffix, Pattern: "ción"}, "cancionero", false},
		{"script thai hit", Tell{Kind: KindScript, Scripts: []*unicode.RangeTable{unicode.Thai}}, "ผมหิวข้าว", true},
		{"script thai miss on latin", Tell{Kind: KindScript, Scripts: []*unicode.RangeTable{unicode.Thai}}, "Hello there 123", false},
		{"script kana hit amid han", Tell{Kind: KindScript, Scripts: []*unicode.RangeTable{unicode.Hiragana, unicode.Katakana}}, "私の犬", true},
		{"script kana miss on pure han", Tell{Kind: KindScript, Scripts: []*unicode.RangeTable{unicode.Hiragana, unicode.Katakana}}, "我是学生", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			low := strings.ToLower(tc.sentence)
			if got := tc.tell.matches(low, splitWords(low)); got != tc.want {
				t.Fatalf("matches(%q) = %v, want %v", tc.sentence, got, tc.want)
			}
		})
	}
}

func TestMatched_StrongestFirst(t *testing.T) {
	// "A informação não chegou." hits ã (letters), -ção (suffix) and "não"
	// (word) for Portuguese; the strongest (dataset order) wins.
	cue, ok := matched("por", "A informação não chegou.")
	if !ok {
		t.Fatal("expected a match for Portuguese")
	}
	if cue != "ã/õ — nasal vowels, Portuguese-only" {
		t.Fatalf("cue = %q, want the strongest tell %q", cue, "ã/õ — nasal vowels, Portuguese-only")
	}
}

func TestBuild_CorrectAnswer_ShortAndSingleLine(t *testing.T) {
	tip := build(quiz.TipRequest{
		ContentPayload: "È un'altra questione.",
		CorrectKey:     "ita", CorrectLabel: "Italian",
		ChosenKey: "ita", ChosenLabel: "Italian",
		Correct: true,
	})
	if tip != "Italian: “è” means “is”" {
		t.Fatalf("tip = %q", tip)
	}
	if strings.Contains(tip, "\n") {
		t.Fatalf("tip must be a single line, got %q", tip)
	}
	if utf8.RuneCountInString(tip) > 60 {
		t.Fatalf("tip too long (%d runes): %q", utf8.RuneCountInString(tip), tip)
	}
}

func TestBuild_WrongAnswer_HeaderIsJustTheLanguage(t *testing.T) {
	tip := build(quiz.TipRequest{
		ContentPayload: "È un'altra questione.",
		CorrectKey:     "ita", CorrectLabel: "Italian",
		ChosenKey: "fra", ChosenLabel: "French",
		Correct: false,
	})
	if tip != "Italian: “è” means “is”" {
		t.Fatalf("tip = %q", tip)
	}
	if strings.Contains(tip, "not ") {
		t.Fatalf("tip must not mention the wrong pick, got %q", tip)
	}
}

func TestBuild_NoMatchMeansNoTip(t *testing.T) {
	// No cue from the dataset appears in this sentence — the tip must stay
	// silent rather than list symbols that aren't there.
	tip := build(quiz.TipRequest{
		ContentPayload: "Falta pouco.",
		CorrectKey:     "por", CorrectLabel: "Portuguese",
		ChosenKey: "spa", ChosenLabel: "Spanish",
		Correct: false,
	})
	if tip != "" {
		t.Fatalf("tip = %q, want empty (nothing in the sentence to point at)", tip)
	}
}

func TestBuild_UnknownKey(t *testing.T) {
	if tip := build(quiz.TipRequest{ContentPayload: "hello", CorrectKey: "eng", ChosenKey: "eng"}); tip != "" {
		t.Fatalf("unknown key produced %q, want empty", tip)
	}
}

func TestBuild_EmptyLabelFallsBackToKey(t *testing.T) {
	tip := build(quiz.TipRequest{
		ContentPayload: "Não fales.",
		CorrectKey:     "por",
		ChosenKey:      "por",
		Correct:        true,
	})
	if !strings.HasPrefix(tip, "por: ") {
		t.Fatalf("tip = %q", tip)
	}
}

func TestDeckHasTips(t *testing.T) {
	cases := []struct {
		slug string
		want bool
	}{
		{"romance", true},
		{"nordic", false},
		{"cjk", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := DeckHasTips(tc.slug); got != tc.want {
			t.Errorf("DeckHasTips(%q) = %v, want %v", tc.slug, got, tc.want)
		}
	}
}

func TestDatasetCompleteness(t *testing.T) {
	for _, key := range deckLanguages {
		if len(tells[key]) == 0 {
			t.Errorf("tells missing for %q", key)
		}
	}
}

func TestProvider_ImplementsQuizTipProvider(t *testing.T) {
	var p quiz.TipProvider = Provider()
	if tip := p.Tip(quiz.TipRequest{}); tip != "" {
		t.Fatalf("zero request produced %q, want empty", tip)
	}
}
