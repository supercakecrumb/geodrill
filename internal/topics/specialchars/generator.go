// Package specialchars implements the topics.Generator for quiz_kind
// "char_language" (architecture §6.1, topic path "languages/special-characters"):
// letters distinctive to one language or a small subgroup of languages
// (e.g. "ø" -> Norwegian/Danish). It never touches storage directly — see
// the internal/topics package doc's "content access stays out of this
// package" contract — because every char_language item carries all the
// content it needs inline in its payload (no sentence/media sampling
// required, unlike word- or sentence-backed topics).
package specialchars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
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

// errUnsupportedMode is returned when the caller's ExerciseRequest.Mode does
// not match what this item's payload shape supports (single-language items
// support ModeSingle/ModeText; subgroup items support ModeSet only,
// architecture §6.1). This is distinct from topics.ErrNoContent: a mode/shape
// mismatch is a caller-wiring problem (the future train service is expected
// to only ask a mode the item's shape supports), not "try another candidate,
// this one has no content" — so it is NOT the ErrNoContent sentinel.
var errUnsupportedMode = errors.New("specialchars: exercise mode not supported for this item's language shape")

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

// Generator implements topics.Generator for Kind.
type Generator struct{}

// New builds a Generator. No dependencies are injected — BuildExercise and
// BuildIntro derive everything from their arguments (the item's own payload
// plus its siblings' payloads); char_language items need no DB-backed
// content sampler the way a sentence- or word-based topic would. New is an
// explicit constructor (not a package-level singleton) and does NOT
// self-register via init(): wiring a Generator into the topics registry is
// deferred to a later wave's cmd/bot changes, per the orchestrator brief.
func New() *Generator { return &Generator{} }

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return Kind }

// BuildExercise implements topics.Generator. It dispatches on the item's
// language-count shape (single vs subgroup) and req.Mode; see errUnsupportedMode
// and parsePayload's doc for the two kinds of failure it can return.
func (g *Generator) BuildExercise(_ context.Context, rng *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	p, err := parsePayload(req.Item.Payload)
	if err != nil {
		return topics.Exercise{}, err
	}

	if len(p.Languages) == 1 {
		switch req.Mode {
		case quiz.ModeSingle:
			return buildSingleMCQ(rng, p, req.Siblings), nil
		case quiz.ModeText:
			return buildText(p), nil
		default:
			return topics.Exercise{}, fmt.Errorf("%w: mode=%d on single-language item %q", errUnsupportedMode, req.Mode, p.Char)
		}
	}

	// Subgroup item (len(languages) > 1): set-choice only.
	if req.Mode != quiz.ModeSet {
		return topics.Exercise{}, fmt.Errorf("%w: mode=%d on subgroup item %q", errUnsupportedMode, req.Mode, p.Char)
	}
	return buildSetMCQ(rng, p, req.Siblings), nil
}

// buildSingleMCQ builds the ModeSingle "which language uses this char?" MCQ:
// the correct language plus up to maxSingleDistractors distractors drawn
// from other same-script languages present across sibling items' payloads.
// Deterministic given rng: candidates are sorted before the first shuffle so
// the same rng state + input order always produce the same output.
func buildSingleMCQ(rng *rand.Rand, p payload, siblings []storage.Item) topics.Exercise {
	target := p.Languages[0]

	pool := sameScriptLanguages(p.Script, siblings, map[string]bool{target: true})
	sort.Strings(pool)
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if len(pool) > maxSingleDistractors {
		pool = pool[:maxSingleDistractors]
	}

	codes := make([]string, 0, len(pool)+1)
	codes = append(codes, target)
	codes = append(codes, pool...)

	options := make([]topics.Option, len(codes))
	for i, code := range codes {
		options[i] = topics.Option{Key: code, Label: languageLabel(code)}
	}
	rng.Shuffle(len(options), func(i, j int) { options[i], options[j] = options[j], options[i] })

	return topics.Exercise{
		Mode:          quiz.ModeSingle,
		Prompt:        fmt.Sprintf("Which language uses “%s”?", p.Char),
		Options:       options,
		CorrectAnswer: target,
	}
}

// buildText builds the ModeText free-typed variant of the single-language
// question: Accept holds the English name, ISO 639-3 code, and (where the
// alias table is confident) native spellings; CorrectAnswer is the
// canonical spelling (the English display name).
func buildText(p payload) topics.Exercise {
	target := p.Languages[0]
	return topics.Exercise{
		Mode:          quiz.ModeText,
		Prompt:        fmt.Sprintf("Type the language that uses “%s”:", p.Char),
		Accept:        acceptSpellings(target),
		CorrectAnswer: languageLabel(target),
	}
}

// buildSetMCQ builds the ModeSet "which languages use this char?" exercise
// (mirrors engram/quiz.GenerateSet's shape, adapted to topics.OptionSet since
// registry.go's Exercise carries OptionSets rather than quiz.SetExercise
// directly): the true language set plus up to maxSetDistractors same-size,
// same-script distractor sets built by swapping one member of the target set
// for another same-script sibling language.
func buildSetMCQ(rng *rand.Rand, p payload, siblings []storage.Item) topics.Exercise {
	targetKeys := quiz.CanonSet(p.Languages...)
	targetLabel := setLabelInOrder(p.Languages)

	pool := sameScriptLanguages(p.Script, siblings, nil) // includes target's own members; filtered below per-swap
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
		Prompt:        fmt.Sprintf("Which languages use “%s”?", p.Char),
		OptionSets:    optionSets,
		CorrectAnswer: strings.Join(targetKeys, ","),
	}
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
