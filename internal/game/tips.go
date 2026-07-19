// tips.go builds post-answer recognition tips for the game's language
// decks: curated per-language "tells" (characteristic letters, digraphs,
// words, scripts) matched against the actual sentence shown, plus contrast
// notes for classic confusion pairs. Moved here from the retired
// internal/tips package (guess-the-language left the study pipeline for
// the game zone — vibe/design-game-zone.md — and its tips content moved
// with it, no parallel copy left behind). Provider() still plugs into
// engram's quiz.TipProvider for any code that wants the generic interface;
// TipFor is the game's own direct entry point (design doc "Language
// Roulette": "the language tip for the missed language when one exists").
package game

import (
	"strings"
	"unicode"

	"github.com/supercakecrumb/engram/quiz"
)

// TellKind selects the matching algorithm for one Tell.
type TellKind int

const (
	// KindLetters matches when any rune of Pattern occurs in the sentence.
	KindLetters TellKind = iota
	// KindSubstring matches a case-insensitive substring (digraphs, clusters).
	KindSubstring
	// KindWord matches a whole word (Unicode-aware boundaries).
	KindWord
	// KindSuffix matches a word ending (the bare suffix as a word doesn't count).
	KindSuffix
	// KindScript matches when any letter rune belongs to one of Scripts.
	KindScript
)

// Tell is one curated recognition cue for a language. Text is a SHORT cue —
// the concrete letter/word/script feature, e.g. `“è”`, `ã/õ`, `-ção`,
// `Han, no kana` — not a sentence. Tips render as one line, so keep it terse.
type Tell struct {
	Kind    TellKind
	Pattern string                // lowercase; unused for KindScript
	Scripts []*unicode.RangeTable // KindScript only
	Text    string                // SHORT cue shown in the tip
}

// Provider returns the language-tell TipProvider for geodrill's decks.
func Provider() quiz.TipFunc {
	return build
}

// TipFor returns the recognition tip for a missed Language Roulette round
// (design doc "Language Roulette": "the language tip for the missed
// language when one exists") — the correct language's tell, if the shown
// sentence actually contains one. "" means no tip for this sentence.
func TipFor(correctKey, correctLabel, sentence string) string {
	return build(quiz.TipRequest{
		ContentPayload: sentence,
		CorrectKey:     correctKey,
		CorrectLabel:   correctLabel,
		Correct:        false,
	})
}

// build assembles a one-line tip (no 💡 prefix — the telegram layer adds it).
// A tip only ever cites evidence actually present in THIS sentence — when no
// cue matches, there is NO tip (empty string) rather than a generic list of
// symbols the sentence doesn't contain. The header is just the language name.
//
//	Italian: “è” means “is”
//	Portuguese: “um” means “a”
func build(req quiz.TipRequest) string {
	name := displayName(req.CorrectLabel, req.CorrectKey)

	cue, ok := matched(req.CorrectKey, req.ContentPayload)
	if !ok {
		return "" // nothing in this sentence to point at — stay silent
	}
	return name + ": " + cue
}

// matched returns the cue of the strongest tell for key that is present in
// sentence (dataset order = strongest first). ok=false means nothing matched.
func matched(key, sentence string) (string, bool) {
	ts := tells[key]
	if len(ts) == 0 || sentence == "" {
		return "", false
	}
	low := strings.ToLower(sentence)
	words := splitWords(low)
	for _, t := range ts {
		if t.matches(low, words) {
			return t.Text, true
		}
	}
	return "", false
}

// splitWords splits lowercased text into words on any rune that is neither a
// letter nor a combining mark, so punctuation-adjacent words ("Af,") still
// match. Combining marks must stay attached: Indic vowel signs (matras) are
// category Mn/Mc, so splitting on them would tear words like है apart.
func splitWords(low string) []string {
	return strings.FieldsFunc(low, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Mc, r)
	})
}

// matches reports whether the tell is present in the (lowercased) sentence.
func (t Tell) matches(low string, words []string) bool {
	switch t.Kind {
	case KindLetters:
		for _, r := range t.Pattern {
			if strings.ContainsRune(low, r) {
				return true
			}
		}
	case KindSubstring:
		return strings.Contains(low, t.Pattern)
	case KindWord:
		for _, w := range words {
			if w == t.Pattern {
				return true
			}
		}
	case KindSuffix:
		for _, w := range words {
			if len(w) > len(t.Pattern) && strings.HasSuffix(w, t.Pattern) {
				return true
			}
		}
	case KindScript:
		for _, r := range low {
			if unicode.IsLetter(r) && unicode.In(r, t.Scripts...) {
				return true
			}
		}
	}
	return false
}

// displayName prefers the label the user actually saw, falling back to the
// raw key when the label is empty.
func displayName(label, key string) string {
	if label != "" {
		return label
	}
	return key
}
