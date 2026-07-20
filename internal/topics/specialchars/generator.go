// Package specialchars is the special-characters topic (architecture §6.1,
// quiz_kind "char_language", topic languages/special-characters): letters
// distinctive to one language or a small subgroup of languages (e.g. "ø" ->
// Norwegian/Danish), expressed as an engine.Descriptor. What stays here is
// genuinely this topic's: the payload shape and validation, the language
// alias/accepted-spellings table (alias.go), the intro list rendering
// (intro.go), and — the one custom generation mechanic — the ModeSet
// one-member-swap distractor-set builder below, hung on the descriptor's
// BuildSet hook. Single-choice sampling (same-language-group via
// engine.Card.Group/engine.LanguageGroup — a Cyrillic letter never offers a
// Latin/CJK option, and a Latin letter stays within its own language family
// rather than any Latin-script language), text mode, mode/shape dispatch,
// and seeding are the generic engine's (internal/topics/engine).
package specialchars

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"unicode"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Kind is the topics.quiz_kind this package's Generator registers under.
const Kind = "char_language"

// Tuning knobs for MCQ size. Deliberately small constants (not derived from
// the sibling pool size) so a button-based Telegram keyboard stays usable
// even though a script can have 15+ sibling languages in the dataset.
const (
	maxSingleDistractors = 3 // single-language MCQ: target + up to this many
	maxSetDistractors    = 3 // subgroup set-choice: target set + up to this many
)

// payload is the items.payload shape for a char_language item (architecture
// §2.2/§6.1): {"char":"ø","script":"latin","languages":["nor","dan"],"note":"..."}.
type payload struct {
	Char      string   `json:"char"`
	Script    string   `json:"script"`
	Languages []string `json:"languages"`
	Note      string   `json:"note"`
}

// parsePayload decodes and validates an item's payload. A JSON error or a
// missing char/script/languages is "malformed" — the one case BuildExercise
// is allowed to surface as topics.ErrNoContent (per the Generator contract:
// callers skip the item and try the next candidate rather than failing the
// whole request).
func parsePayload(raw []byte) (payload, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return payload{}, fmt.Errorf("%w: invalid payload json: %v", topics.ErrNoContent, err)
	}
	if p.Char == "" || p.Script == "" || len(p.Languages) == 0 {
		return payload{}, fmt.Errorf("%w: payload missing char/script/languages", topics.ErrNoContent)
	}
	return p, nil
}

// parseCard adapts an item payload to the engine's Card: the claimed
// languages as answer keys (declared order — one key = single-language
// item, several = subgroup, which the engine routes to BuildSet), the
// language-family group (distractorGroup) as the distractor-compatibility
// group, the character as the prompt subject, and the rendered intro blurb
// (intro.go).
func parseCard(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    p.Languages,
		Group:   distractorGroup(p.Languages),
		Subject: p.Char,
		Intro:   introText(p),
	}, nil
}

// distractorGroup computes the char item's engine.Card.Group: the
// guess-the-language family (engine.LanguageGroup) shared by every claimed
// language, so single-choice distractors are drawn from the same language
// family rather than merely the same script — a Cyrillic question must
// never offer a far-apart Latin/CJK option, and a Latin question must not
// offer, say, a Nordic option for a Slavic-Latin target, even though both
// are "latin" script.
//
// A handful of entries legitimately straddle two language families (e.g.
// "ă" is claimed by Romanian AND Vietnamese — romance and se-asia). Such an
// item is never itself a single-choice target (BuildExercise routes
// multi-key items to BuildSet, never the sampled single-choice builder),
// but it can still be scanned as a sibling candidate source for some other
// single-language target; picking one of its two families arbitrarily would
// let its off-family member leak into that target's distractor pool. So a
// straddling item's Group is mixedGroupSentinel instead — a value no
// single-language item's own Group is ever assigned — so it never matches
// any target's Group and is simply excluded as a distractor source (the
// target's pool loses one potential candidate, never gains a wrong-family
// one).
func distractorGroup(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	g := engine.LanguageGroup(codes[0])
	for _, c := range codes[1:] {
		if engine.LanguageGroup(c) != g {
			return mixedGroupSentinel
		}
	}
	return g
}

// mixedGroupSentinel marks a Card whose claimed languages span more than one
// engine.LanguageGroup family (see distractorGroup) — deliberately a value
// engine.LanguageGroup never returns, so it can never accidentally match a
// real single-language item's Group.
const mixedGroupSentinel = "\x00mixed"

// charScript classifies a char item's Unicode script from its own subject
// rune — used only by buildSetMCQ's same-script subgroup-swap pool
// (independent of Card.Group, which after distractorGroup now carries
// language-family information, not script). Every char in this deck is
// unambiguously Latin or Cyrillic script, and the seed's own `script` field
// always agrees with this classification (seeds/special_chars.yaml), so
// re-deriving it from the rune needs no payload lookup.
func charScript(subject string) string {
	for _, r := range subject {
		if unicode.Is(unicode.Cyrillic, r) {
			return "cyrillic"
		}
	}
	return "latin"
}

// labelTable projects the alias table (alias.go) into the descriptor's
// Labels map: ISO 639-3 code -> English display name. Codes missing from
// the table fall back to strings.ToUpper(code) inside the engine.
func labelTable() map[string]string {
	out := make(map[string]string, len(languageAliases))
	for code, a := range languageAliases {
		out[code] = a.Name
	}
	return out
}

// descriptor declares the whole topic for the engine: tree, parse, labels,
// prompts for the single and text modes, the same-language-group distractor
// policy, the accepted-spellings hook, and the custom set builder.
var descriptor = engine.Descriptor{
	QuizKind: Kind,
	Topic: []engine.TopicNode{
		{Slug: "languages", Name: "Languages"},
		{Slug: "special-characters", Name: "Special characters", BaseTier: 2, QuizKind: Kind, ExerciseModes: []string{"single", "set", "text"}, IsQuizzable: true},
	},
	Parse:        parseCard,
	Labels:       labelTable(),
	PromptSingle: "Which language uses “%s”?",
	PromptText:   "Type the language that uses “%s”:",
	Distractors:  engine.DistractorPolicy{Max: maxSingleDistractors, SameGroup: true},
	Accept:       acceptSpellings,
	// Autocomplete: the typed answer is a language name, for which the global
	// suggestion index carries a DomainLanguage source (Languages() feeds it
	// in cmd/bot) — so these text questions get the same "⌨️ Type your answer"
	// prefill button + inline suggestions country questions already have.
	Autocomplete: true,
	BuildSet:     buildSetMCQ,
}

// New builds the special-characters Generator (the generic engine one,
// driven by this package's descriptor). No DB dependencies — every
// char_language item carries all the content it needs inline in its
// payload. New does NOT self-register via init(): wiring a Generator into
// the topics registry is deferred to cmd/bot (architecture §8).
func New() *engine.Generator { return engine.New(descriptor) }

// buildSetMCQ is the descriptor's BuildSet hook: the ModeSet "which
// languages use this char?" exercise for subgroup items (mirrors
// engram/quiz.GenerateSet's shape, adapted to topics.OptionSet) — the true
// language set plus up to maxSetDistractors same-size, same-script
// distractor sets built by swapping one member of the target set for
// another same-script sibling language.
func buildSetMCQ(rng *rand.Rand, card engine.Card, siblings []storage.Item) (topics.Exercise, error) {
	targetKeys := quiz.CanonSet(card.Keys...)
	targetLabel := setLabelInOrder(card.Keys)

	// Script (not card.Group, which is now the language-family group used by
	// single-choice sampling) is what the one-member-swap pool needs here:
	// this deck's subgroup items are always uniform-script by construction
	// (e.g. "č" -> ces/slk/hrv/slv is entirely Latin), so charScript(target's
	// own subject rune) is the right, self-contained "which siblings can
	// swap in" scope.
	pool := sameScriptLanguages(charScript(card.Subject), siblings, nil) // includes target's own members; filtered below per-swap
	sort.Strings(pool)

	distractors := buildDistractorSets(rng, targetKeys, pool, maxSetDistractors)

	optionSets := make([]topics.OptionSet, 0, len(distractors)+1)
	optionSets = append(optionSets, topics.OptionSet{Keys: append([]string(nil), targetKeys...), Label: targetLabel})
	for _, d := range distractors {
		optionSets = append(optionSets, topics.OptionSet{Keys: append([]string(nil), d...), Label: setLabelInOrder(d)})
	}
	rng.Shuffle(len(optionSets), func(i, j int) { optionSets[i], optionSets[j] = optionSets[j], optionSets[i] })

	return topics.Exercise{
		Mode:          quiz.ModeSet,
		Prompt:        fmt.Sprintf("Which languages use “%s”?", card.Subject),
		OptionSets:    optionSets,
		CorrectAnswer: strings.Join(targetKeys, ","),
	}, nil
}

// buildDistractorSets builds up to max distinct, same-size distractor sets by
// swapping exactly one member of target for a same-script replacement drawn
// from pool (excluding replacements already in target). Deterministic: the
// candidate list is built in a fixed (sorted-input) order and deduplicated
// before a single rng.Shuffle picks which ones survive the cap.
func buildDistractorSets(rng *rand.Rand, target quiz.KeySet, pool []string, max int) []quiz.KeySet {
	inTarget := make(map[string]bool, len(target))
	for _, m := range target {
		inTarget[m] = true
	}

	seen := map[string]bool{strings.Join(target, ","): true}
	var candidates []quiz.KeySet
	for _, member := range target {
		for _, repl := range pool {
			if inTarget[repl] {
				continue
			}
			swapped := make([]string, 0, len(target))
			for _, m := range target {
				if m == member {
					swapped = append(swapped, repl)
				} else {
					swapped = append(swapped, m)
				}
			}
			cs := quiz.CanonSet(swapped...)
			key := strings.Join(cs, ",")
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, cs)
		}
	}

	rng.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	if len(candidates) > max {
		candidates = candidates[:max]
	}
	return candidates
}

// sameScriptLanguages returns the distinct language codes appearing in
// siblings' payloads whose script matches script, skipping any sibling that
// fails to parse (a malformed sibling shouldn't block generation for a valid
// item) and any code present in exclude. Order is insertion order (over
// siblings, then over each payload's Languages) — callers that need
// determinism sort the result themselves before shuffling.
func sameScriptLanguages(script string, siblings []storage.Item, exclude map[string]bool) []string {
	seen := map[string]bool{}
	for k, v := range exclude {
		seen[k] = v
	}
	var out []string
	for _, sib := range siblings {
		sp, err := parsePayload(sib.Payload)
		if err != nil || sp.Script != script {
			continue
		}
		for _, lang := range sp.Languages {
			if seen[lang] {
				continue
			}
			seen[lang] = true
			out = append(out, lang)
		}
	}
	return out
}

// setLabelInOrder renders a human label for a language-code list, preserving
// the caller's given order (e.g. the seed file's declared order, so "ø" ->
// languages [nor, dan] reads "Norwegian / Danish", matching the source data's
// declared order rather than alphabetical-by-code).
func setLabelInOrder(codes []string) string {
	labels := make([]string, len(codes))
	for i, c := range codes {
		labels[i] = languageLabel(c)
	}
	return strings.Join(labels, " / ")
}
