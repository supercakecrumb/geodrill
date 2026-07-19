// Package words is the common-words topic (architecture §6.3, quiz_kind
// "word_language", topic languages/common-words): a word→language
// single-choice quiz over sign/street vocabulary ("ulica", "avenida",
// "проспект", ...), expressed as an engine.Descriptor. Only what is
// genuinely this topic's lives here — the payload shape and its validation,
// the script derivation (from the word's own characters, never a static
// per-language table), the language-name table, and the prompt/intro texts;
// option building, same-script distractor sampling, and seeding are the
// generic engine's (internal/topics/engine).
//
// Distractor languages are drawn from sibling items that share the target
// word's script (Latin vs Cyrillic — engine.Card.Group), so a Cyrillic word
// is never quizzed against Latin-script options.
//
// word→meaning is intentionally NOT built here (architecture §6.3, §9.6): a
// later mode/topic reuses these same items' payload.meaning field.
package words

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
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

// languageNames maps the ISO-639-3 codes in the common-words deck to their
// English display name (architecture §6.3). Doubles as the descriptor's
// Labels table (unknown codes fall back to the uppercased code, the
// engine's degraded-label-beats-failure convention).
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
// the bare code (uppercased) when it isn't in the table.
func languageName(code string) string {
	if name, ok := languageNames[code]; ok {
		return name
	}
	return strings.ToUpper(code)
}

// parseCard adapts an item payload to the engine's Card: one answer key
// (the language), the word's script as the distractor-compatibility group,
// the word as the prompt subject, and the rendered intro blurb
// (architecture §5.1), e.g. 📖 "ulica" — "street" in Polish.
func parseCard(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    []string{p.Language},
		Group:   scriptOf(p.Word),
		Subject: p.Word,
		Intro:   fmt.Sprintf("\U0001F4D6 “%s” — “%s” in %s.", p.Word, p.Meaning, languageName(p.Language)),
	}, nil
}

// descriptor declares the whole topic for the engine: tree, parse, labels,
// prompt, and the same-script distractor policy.
var descriptor = engine.Descriptor{
	QuizKind: QuizKind,
	Topic: []engine.TopicNode{
		{Slug: RootSlug, Name: RootName},
		{Slug: TopicSlug, Name: TopicName, Position: topicPosition, BaseTier: BaseTier, QuizKind: QuizKind, ExerciseModes: []string{"single"}, IsQuizzable: true},
	},
	Parse:        parseCard,
	Labels:       languageNames,
	PromptSingle: "You see “%s” on a sign — what language?",
	Distractors:  engine.DistractorPolicy{Max: maxDistractors, SameGroup: true},
}

// New constructs the common-words Generator (the generic engine one, driven
// by this package's descriptor). Callers must explicitly
// topics.Register(New()) — this package intentionally has no init()
// self-registration (architecture §8: topic workers never collide by
// registering themselves eagerly at import time).
func New() *engine.Generator {
	return engine.New(descriptor)
}
