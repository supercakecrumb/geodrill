// Package words is the common-words topic worker (architecture §6.3,
// quiz_kind "word_language", topic languages/common-words): a word→language
// single-choice quiz over sign/street vocabulary ("ulica", "avenida",
// "проспект", ...). Distractor languages are drawn from sibling items that
// share the target word's script (Latin vs Cyrillic — derived from the
// word's own characters, never from a static per-language table), so a
// Cyrillic word is never quizzed against Latin-script options.
//
// word→meaning is intentionally NOT built here (architecture §6.3, §9.6): a
// later mode/topic reuses these same items' payload.meaning field.
package words

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"unicode"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// maxDistractors caps how many sibling languages join the correct answer as
// MCQ options (architecture §6.3: "3–5 distractor languages").
const maxDistractors = 5

// ErrMalformedPayload is returned by BuildExercise/BuildIntro when an item's
// payload isn't the {"word":...,"language":...,"meaning":...} shape Seed
// writes (architecture §6.3).
var ErrMalformedPayload = errors.New("words: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for this topic.
type itemPayload struct {
	Word     string `json:"word"`
	Language string `json:"language"`
	Meaning  string `json:"meaning"`
}

// parsePayload decodes an item's payload, rejecting anything missing the
// fields a word item needs (word + language; meaning may legitimately be
// used later but isn't required to render this mode's exercise/intro).
func parsePayload(raw []byte) (itemPayload, error) {
	if len(raw) == 0 {
		return itemPayload{}, ErrMalformedPayload
	}
	var p itemPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return itemPayload{}, fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}
	if p.Word == "" || p.Language == "" {
		return itemPayload{}, ErrMalformedPayload
	}
	return p, nil
}

// scriptOf classifies a word's script from its own characters (never from a
// per-language table): any Cyrillic letter present ⇒ "cyrillic", otherwise
// "latin". This deck only ever contains those two scripts (architecture
// §6.3/§4), and defaulting to "latin" correctly handles every diacritic
// variant in the deck (Vietnamese đ/ư/ơ, Polish ł, Czech ř, ...) without
// needing to enumerate Latin Unicode blocks.
func scriptOf(word string) string {
	for _, r := range word {
		if unicode.Is(unicode.Cyrillic, r) {
			return "cyrillic"
		}
	}
	return "latin"
}

// languageNames maps the 23 ISO-639-3 codes in the common-words deck to
// their English display name (architecture §6.3).
var languageNames = map[string]string{
	"bul": "Bulgarian",
	"cat": "Catalan",
	"ces": "Czech",
	"dan": "Danish",
	"fin": "Finnish",
	"fra": "French",
	"hrv": "Croatian",
	"ind": "Indonesian",
	"isl": "Icelandic",
	"ita": "Italian",
	"mkd": "Macedonian",
	"nor": "Norwegian",
	"pol": "Polish",
	"por": "Portuguese",
	"ron": "Romanian",
	"rus": "Russian",
	"slk": "Slovak",
	"slv": "Slovenian",
	"spa": "Spanish",
	"srp": "Serbian",
	"swe": "Swedish",
	"ukr": "Ukrainian",
	"vie": "Vietnamese",
}

// languageName returns the English display name for code, falling back to
// the bare code (uppercased) when it isn't in the table — a degraded label
// beats failing the exercise outright over an unrecognised code.
func languageName(code string) string {
	if name, ok := languageNames[code]; ok {
		return name
	}
	return strings.ToUpper(code)
}

// Generator implements topics.Generator for quiz_kind "word_language". It is
// stateless: BuildExercise/BuildIntro read only from the request/item they're
// given, so a single instance is safe to share and register once.
type Generator struct{}

// New constructs the common-words Generator. Callers must explicitly
// topics.Register(New()) — this package intentionally has no init()
// self-registration (architecture §8: topic workers never collide by
// registering themselves eagerly at import time).
func New() *Generator {
	return &Generator{}
}

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return "word_language" }

// BuildExercise implements topics.Generator: a ModeSingle word→language MCQ.
// Distractor languages are drawn deterministically (via rng) from req.
// Siblings whose word shares req.Item's script, up to maxDistractors.
// Candidates are canonicalized (deduped, sorted) before shuffling so the
// result depends only on the *set* of eligible sibling languages, not on
// req.Siblings' incoming order.
func (g *Generator) BuildExercise(_ context.Context, rng *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	target, err := parsePayload(req.Item.Payload)
	if err != nil {
		return topics.Exercise{}, fmt.Errorf("words: item %s: %w", req.Item.Key, err)
	}
	targetScript := scriptOf(target.Word)

	seen := map[string]struct{}{target.Language: {}}
	var candidates []string
	for _, sib := range req.Siblings {
		p, err := parsePayload(sib.Payload)
		if err != nil {
			continue // a malformed sibling shouldn't sink this exercise
		}
		if _, dup := seen[p.Language]; dup {
			continue
		}
		if scriptOf(p.Word) != targetScript {
			continue // same-script invariant (architecture §6.3)
		}
		seen[p.Language] = struct{}{}
		candidates = append(candidates, p.Language)
	}
	sort.Strings(candidates)
	rng.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	if len(candidates) > maxDistractors {
		candidates = candidates[:maxDistractors]
	}

	opts := make([]topics.Option, 0, len(candidates)+1)
	opts = append(opts, topics.Option{Key: target.Language, Label: languageName(target.Language)})
	for _, code := range candidates {
		opts = append(opts, topics.Option{Key: code, Label: languageName(code)})
	}
	rng.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })

	return topics.Exercise{
		Mode:          quiz.ModeSingle,
		Prompt:        fmt.Sprintf("You see “%s” on a sign — what language?", target.Word),
		Options:       opts,
		CorrectAnswer: target.Language,
	}, nil
}

// BuildIntro implements topics.Generator: the teaching blurb shown before an
// item's first exercise (architecture §5.1), e.g. 📖 "ulica" — "street" in
// Polish.
func (g *Generator) BuildIntro(_ context.Context, item storage.Item) (topics.IntroCard, error) {
	p, err := parsePayload(item.Payload)
	if err != nil {
		return topics.IntroCard{}, fmt.Errorf("words: item %s: %w", item.Key, err)
	}
	return topics.IntroCard{
		Text: fmt.Sprintf("\U0001F4D6 “%s” — “%s” in %s.", p.Word, p.Meaning, languageName(p.Language)),
	}, nil
}
