// Package engine is geodrill's universal topic engine: ONE generic exercise
// generator (Generator) and ONE generic seeder (Seed), both driven by a
// declarative Descriptor, so adding a topic means writing a seed dataset and
// a small descriptor — data, not another package of copy-pasted generation
// and seeding Go (the abstraction extracted after specialchars, words,
// roadside, and guesslang each re-implemented the same loops).
//
// A topic package now contains only what is genuinely its own:
//
//   - its seed YAML schema and the mapping from that schema to []ItemSeed
//     (schemas legitimately differ per topic, so this stays typed Go);
//   - a Parse func decoding items.payload into a Card (payload shapes and
//     validation rules differ per topic — each keeps its own sentinel);
//   - truly custom mechanics, hung on the Descriptor's hook fields (e.g.
//     specialchars' set-choice building via BuildSet and its accepted-
//     spellings table via Accept).
//
// Everything else — mode dispatch, distractor sampling, option assembly and
// shuffling, prompt rendering, intro cards, topic-path/item/country/fact
// upserts, tier rules — is the engine's, written once.
package engine

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// Card is a topic item's payload decoded into what the engine needs: the
// answer key(s), the distractor-compatibility group, and the two rendered
// strings (prompt subject, intro text) the templates consume. A Descriptor's
// Parse produces one Card per item; the engine never sees raw payload JSON.
type Card struct {
	// Keys holds the item's answer key(s) in the payload's declared order.
	// Exactly one key = a single-answer item (ModeSingle/ModeText); more
	// than one = a set-shaped item (ModeSet via Descriptor.BuildSet).
	Keys []string

	// Group is the distractor-compatibility group: with
	// DistractorPolicy.SameGroup set, distractors are drawn only from
	// sibling items whose Card.Group equals the target's (e.g. the
	// guess-the-language family — "romance"/"slavic-cyrillic"/... via
	// engine.LanguageGroup, which specialchars and words both use — so a
	// Cyrillic question never offers a Latin/CJK option, and a Latin
	// question stays within its own language family rather than any
	// Latin-script language). Because Parse computes it, a topic can
	// tighten its distractor pool to any "close distractors" notion — same
	// language family, same region, same first letter — by changing only
	// how Group is derived, with no engine change.
	Group string

	// Subject is the rendered subject slotted into the Descriptor's prompt
	// templates (their single %s verb): the word, the character, the
	// "🇬🇧 United Kingdom" flag+name pair.
	Subject string

	// Intro is the fully rendered introduction-card text for the item
	// (architecture §5.1) — intro texts vary too much across topics
	// (conditional notes, natural-language list joins) to be a template, so
	// Parse renders them and the engine just wraps the result.
	Intro string
}

// DistractorPolicy shapes how the generic single-choice builder draws wrong
// options from sibling items.
type DistractorPolicy struct {
	// Max caps how many distractors join the correct answer.
	Max int

	// SameGroup restricts distractors to siblings whose Card.Group equals
	// the target's (see Card.Group). false = any sibling is eligible.
	SameGroup bool
}

// TopicNode is one node of a topic path (parents first) for the seeder.
// Zero-value defaults match the tree conventions every existing topic uses:
// QuizKind "" seeds as "container", nil ExerciseModes as {"single"}, nil
// Config as {}.
type TopicNode struct {
	Slug          string
	Name          string
	Position      int
	BaseTier      int16
	QuizKind      string
	ExerciseModes []string
	IsQuizzable   bool
	Config        []byte
}

func (n TopicNode) quizKind() string {
	if n.QuizKind == "" {
		return "container"
	}
	return n.QuizKind
}

func (n TopicNode) exerciseModes() []string {
	if len(n.ExerciseModes) == 0 {
		return []string{"single"}
	}
	return n.ExerciseModes
}

func (n TopicNode) config() []byte {
	if len(n.Config) == 0 {
		return []byte(`{}`)
	}
	return n.Config
}

// Descriptor declares one topic for the engine: its quiz_kind, its topic
// path, how payloads decode, and how each exercise mode is built. Hook
// fields (Parse, Accept, BuildSet) are plain funcs supplied in the topic's
// descriptor literal — used only where a topic genuinely varies; everything
// left nil/zero simply disables that capability.
type Descriptor struct {
	// QuizKind is the topics.quiz_kind this descriptor's Generator
	// registers under and the seeder stamps on the quizzable topic row.
	QuizKind string

	// Topic is the topic path to seed, parents first; the last node is the
	// topic items attach to. Used only by Seed — a seed-only descriptor
	// (e.g. guesslang, whose exercise moved to the game zone) fills just
	// QuizKind and Topic and never constructs a Generator.
	Topic []TopicNode

	// Parse decodes and validates one item payload into a Card. It owns the
	// topic's malformed-payload policy: whatever error it returns (each
	// topic wraps its own sentinel) is propagated by
	// BuildExercise/BuildIntro, so existing errors.Is contracts hold.
	Parse func(raw []byte) (Card, error)

	// Labels maps an answer key to its display label; keys missing from the
	// map fall back to strings.ToUpper(key) (the degraded-label-beats-
	// failure convention every topic already used). May be nil when
	// FixedOptions carries its own labels.
	Labels map[string]string

	// PromptSingle is the ModeSingle question template — a fmt format
	// string with exactly one %s, filled with Card.Subject.
	PromptSingle string

	// PromptText is the ModeText question template (same %s convention).
	// Must be set exactly when Accept is set.
	PromptText string

	// FixedOptions, when non-nil, replaces distractor sampling for
	// ModeSingle: every option is always shown, verbatim and in this fixed
	// order, never shuffled (roadside's two-button Left/Right rule); the
	// correct answer is Card.Keys[0]. Mutually exclusive with Distractors.
	FixedOptions []topics.Option

	// Distractors drives the sampled ModeSingle builder (ignored when
	// FixedOptions is set): eligible sibling keys are deduped, sorted,
	// shuffled with the caller's rng, capped at Max, then shuffled together
	// with the correct answer.
	Distractors DistractorPolicy

	// Accept, when non-nil, enables ModeText: it returns the accepted
	// spellings for an answer key (quiz.TextMatcher input). CorrectAnswer
	// is the key's display label.
	Accept func(key string) []string

	// Autocomplete opts this topic's ModeText exercises into the inline
	// autocomplete button ("⌨️ Type your answer") + domain suggestions, by
	// stamping Exercise.Autocomplete on every buildText result. It is
	// independent of the exercise_modes "autocomplete" string route
	// (internal/study.trainer): that route is per-topic-config data, this is a
	// descriptor-level opt-in for topics (e.g. special characters) whose typed
	// answers have a suggestion domain in internal/suggest.
	Autocomplete bool

	// BuildSet, when non-nil, handles set-shaped items (len(Card.Keys) > 1,
	// ModeSet only). Set-choice option building is the one generation
	// mechanic that stayed custom (specialchars' one-member-swap distractor
	// sets), so it is a hook rather than engine code: it receives the raw
	// sibling items and applies its own payload parsing and pooling.
	BuildSet func(rng *rand.Rand, card Card, siblings []storage.Item) (topics.Exercise, error)
}

// Validate checks that d can drive a Generator. Seed-only descriptors are
// not expected to pass (Seed performs its own, narrower checks).
func (d Descriptor) Validate() error {
	if d.QuizKind == "" {
		return fmt.Errorf("engine: descriptor has empty QuizKind")
	}
	if d.Parse == nil {
		return fmt.Errorf("engine: descriptor %q has nil Parse", d.QuizKind)
	}
	if d.PromptSingle == "" {
		return fmt.Errorf("engine: descriptor %q has empty PromptSingle", d.QuizKind)
	}
	if d.FixedOptions != nil {
		if d.Distractors.Max != 0 || d.Distractors.SameGroup {
			return fmt.Errorf("engine: descriptor %q sets both FixedOptions and a DistractorPolicy — they are mutually exclusive", d.QuizKind)
		}
	} else if d.Distractors.Max < 1 {
		return fmt.Errorf("engine: descriptor %q needs Distractors.Max >= 1 for sampled single-choice (or FixedOptions)", d.QuizKind)
	}
	if (d.Accept == nil) != (d.PromptText == "") {
		return fmt.Errorf("engine: descriptor %q must set Accept and PromptText together (ModeText needs both)", d.QuizKind)
	}
	return nil
}

// Label resolves an answer key's display label per the Labels-map-with-
// uppercase-fallback convention.
func (d Descriptor) Label(key string) string {
	if l, ok := d.Labels[key]; ok {
		return l
	}
	return strings.ToUpper(key)
}
