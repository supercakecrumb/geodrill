// round.go builds one Language Roulette round (design doc "Language
// Roulette"): picking a random sentence from whichever seeded language
// currently has ingested content, then choosing its 4 answer options (1
// correct, 3 distractors) per the streak-based difficulty ramp.
package game

import (
	"context"
	"math/rand"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// Language is one guessable language in the game's catalog: its ISO-639-3
// answer key, display label, and the group topic slug it belongs to (e.g.
// "romance") — Group is what the difficulty ramp's "same group" test
// compares (design doc table).
type Language struct {
	Key   string
	Label string
	Group string
}

// Round is one built Language Roulette round: the sampled sentence plus its
// 4 language options in shuffled display order (one correct, three
// distractors chosen per the difficulty ramp).
type Round struct {
	ContentID uuid.UUID
	Prompt    string
	Source    string
	Correct   Language
	Options   []Language
}

// Difficulty ramp thresholds (design doc table) and the "≥3 siblings" gate
// for the hardest tier.
const (
	distractorCount = 3

	streakCloserAt             = 5  // >=5: at least one same-group distractor
	streakHardestAt            = 15 // >=15 with enough siblings: all same-group
	minGroupSiblingsForHardest = 3  // "when it has ≥3 siblings" (design doc)
)

// maxContentAttemptsPerLanguage bounds retries against the same language
// when a freshly sampled sentence collides with one already shown this run
// (design doc "no repeat sentences within a run"): the sampler has no
// "exclude these ids" query (SampleContent only excludes a user's
// recently-REVIEWED content, unrelated to games), so retrying a few times
// before moving to the next language is the practical way to honor it
// without a schema change.
const maxContentAttemptsPerLanguage = 5

// ContentSampler mirrors storage.Store's sentence-sampling methods exactly
// (internal/topics/guesslang's own former ContentSampler contract) — the
// game reuses the same content pool and two-step sampling (SampleContent,
// falling back to SampleContentAny) guess-the-language used before it moved
// into the game zone. *storage.Store already satisfies this directly.
type ContentSampler interface {
	// SampleContent samples a sentence for key, excluding the user's
	// recently-seen content (storage.Store.SampleContent semantics).
	SampleContent(ctx context.Context, userID uuid.UUID, key string) (storage.Content, bool, error)
	// SampleContentAny samples a sentence for key with no exclusion.
	SampleContentAny(ctx context.Context, key string) (storage.Content, bool, error)
}

// sameGroupSiblings returns every language in catalog sharing correct's
// Group, excluding correct itself.
func sameGroupSiblings(catalog []Language, correct Language) []Language {
	var out []Language
	for _, l := range catalog {
		if l.Key != correct.Key && l.Group == correct.Group {
			out = append(out, l)
		}
	}
	return out
}

// otherGroupLanguages returns every language in catalog NOT sharing
// correct's Group.
func otherGroupLanguages(catalog []Language, correct Language) []Language {
	var out []Language
	for _, l := range catalog {
		if l.Group != correct.Group {
			out = append(out, l)
		}
	}
	return out
}

// pickUniqueRandom returns up to n languages drawn from pool in
// rng-shuffled order, skipping any language whose Key is in exclude (a nil
// exclude is a valid, empty set). Returns fewer than n when pool doesn't
// have enough eligible languages — callers top up from a wider pool
// themselves.
func pickUniqueRandom(rng *rand.Rand, pool []Language, exclude map[string]bool, n int) []Language {
	candidates := make([]Language, 0, len(pool))
	for _, l := range pool {
		if !exclude[l.Key] {
			candidates = append(candidates, l)
		}
	}
	rng.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	if len(candidates) > n {
		candidates = candidates[:n]
	}
	return candidates
}

// pickDistractors chooses distractorCount languages (never correct) from
// catalog, following the streak-based difficulty ramp (design doc table):
// distinct groups at low streaks, at least one same-group distractor from
// streakCloserAt, and every distractor same-group from streakHardestAt when
// the group is large enough. Falls back to a looser policy whenever the
// stricter one can't be satisfied (e.g. correct's group has no siblings at
// all). Deterministic given rng.
func pickDistractors(rng *rand.Rand, catalog []Language, correct Language, streak int) []Language {
	siblings := sameGroupSiblings(catalog, correct)
	exclude := map[string]bool{correct.Key: true}

	switch {
	case streak >= streakHardestAt && len(siblings) >= minGroupSiblingsForHardest:
		// 15+ with enough siblings: every distractor from the same group.
		return pickUniqueRandom(rng, siblings, exclude, distractorCount)

	case streak >= streakCloserAt && len(siblings) > 0:
		// 5-14 (or 15+ without enough siblings): force one same-group
		// distractor, then fill the rest from the whole catalog.
		forced := pickUniqueRandom(rng, siblings, exclude, 1)
		out := append([]Language(nil), forced...)
		for _, l := range out {
			exclude[l.Key] = true
		}
		return append(out, pickUniqueRandom(rng, catalog, exclude, distractorCount-len(out))...)

	default:
		// 0-4 (or no same-group sibling exists at all): distinct groups
		// first, topping up from the full catalog if the corpus is too
		// small to fill purely cross-group.
		others := otherGroupLanguages(catalog, correct)
		out := pickUniqueRandom(rng, others, exclude, distractorCount)
		if len(out) < distractorCount {
			for _, l := range out {
				exclude[l.Key] = true
			}
			out = append(out, pickUniqueRandom(rng, catalog, exclude, distractorCount-len(out))...)
		}
		return out
	}
}

// buildRound assembles a Round from a pre-picked correct language + sampled
// content and the full catalog: chooses distractors per streak's
// difficulty ramp, then shuffles all options into display order.
// Deterministic given rng.
func buildRound(rng *rand.Rand, catalog []Language, correct Language, content storage.Content, streak int) Round {
	options := append([]Language{correct}, pickDistractors(rng, catalog, correct, streak)...)
	rng.Shuffle(len(options), func(i, j int) {
		options[i], options[j] = options[j], options[i]
	})
	return Round{
		ContentID: content.ID,
		Prompt:    content.Payload,
		Source:    content.Source,
		Correct:   correct,
		Options:   options,
	}
}

// pickCorrect samples a random language+sentence from catalog that
// currently has ingested content and hasn't been shown yet this run (used,
// keyed by content id — design doc "no repeat sentences within a run"). It
// shuffles catalog with rng and tries each language in turn (so which
// language "wins" is itself rng-driven, per the design doc's "any of the
// languages with content"), retrying a language a few times before moving
// on, and falling back through every language before giving up. ok=false
// means no eligible sentence exists anywhere in the catalog right now (e.g.
// nothing ingested yet).
func pickCorrect(ctx context.Context, sampler ContentSampler, rng *rand.Rand, userID uuid.UUID, catalog []Language, used map[uuid.UUID]bool) (Language, storage.Content, bool, error) {
	order := pickUniqueRandom(rng, catalog, nil, len(catalog))
	for _, lang := range order {
		for attempt := 0; attempt < maxContentAttemptsPerLanguage; attempt++ {
			content, found, err := sampler.SampleContent(ctx, userID, lang.Key)
			if err != nil {
				return Language{}, storage.Content{}, false, err
			}
			if !found {
				content, found, err = sampler.SampleContentAny(ctx, lang.Key)
				if err != nil {
					return Language{}, storage.Content{}, false, err
				}
			}
			if !found {
				break // no content at all for this language; try the next one
			}
			if !used[content.ID] {
				return lang, content, true, nil
			}
		}
	}
	return Language{}, storage.Content{}, false, nil
}
