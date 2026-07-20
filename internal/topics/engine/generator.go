package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// ErrUnsupportedMode is returned by BuildExercise when the requested
// ExerciseRequest.Mode does not match what the item's shape and the
// descriptor's capabilities support (e.g. ModeSet on a single-answer item,
// or ModeText on a descriptor with no Accept). Distinct from
// topics.ErrNoContent: a mode/shape mismatch is a caller-wiring problem —
// the trainer only asks a topic's configured exercise_modes — not "skip
// this item and try another".
var ErrUnsupportedMode = errors.New("engine: exercise mode not supported for this item")

// Generator is the ONE generic topics.Generator, driven entirely by a
// Descriptor. It is stateless beyond its descriptor: BuildExercise and
// BuildIntro read only from their arguments, so a single instance is safe
// to share and register once per quiz_kind.
type Generator struct {
	d Descriptor
}

var _ topics.Generator = (*Generator)(nil)

// New builds a Generator over d, panicking on an invalid descriptor —
// descriptors are wired once at startup (cmd/bot), so a bad one is a
// programmer error caught at wiring time, mirroring topics.Register's
// duplicate-kind panic.
func New(d Descriptor) *Generator {
	if err := d.Validate(); err != nil {
		panic(err.Error())
	}
	return &Generator{d: d}
}

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return g.d.QuizKind }

// BuildExercise implements topics.Generator: it parses the item into a Card
// and dispatches on the item's shape (single-answer vs set-shaped) and
// req.Mode. Deterministic given rng — every random draw goes through the
// injected rng, and candidate pools are canonicalized (deduped, sorted)
// before the first shuffle so the result depends only on the set of
// eligible siblings, never their incoming order.
func (g *Generator) BuildExercise(_ context.Context, rng *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	card, err := g.d.Parse(req.Item.Payload)
	if err != nil {
		// Wrap with item context but keep the topic's own error chain intact
		// (each topic's malformed-payload sentinel stays errors.Is-able).
		return topics.Exercise{}, fmt.Errorf("%s: item %s: %w", g.d.QuizKind, req.Item.Key, err)
	}
	if len(card.Keys) == 0 {
		return topics.Exercise{}, fmt.Errorf("engine: %s: item %q parsed to a Card with no answer keys", g.d.QuizKind, req.Item.Key)
	}

	if len(card.Keys) > 1 {
		// Set-shaped item: ModeSet via the descriptor's custom builder only.
		if req.Mode != quiz.ModeSet || g.d.BuildSet == nil {
			return topics.Exercise{}, fmt.Errorf("%w: mode=%d on set-shaped item %q (quiz_kind %s)", ErrUnsupportedMode, req.Mode, req.Item.Key, g.d.QuizKind)
		}
		return g.d.BuildSet(rng, card, req.Siblings)
	}

	switch req.Mode {
	case quiz.ModeText:
		if g.d.Accept == nil {
			return topics.Exercise{}, fmt.Errorf("%w: mode=%d on item %q (quiz_kind %s has no text mode)", ErrUnsupportedMode, req.Mode, req.Item.Key, g.d.QuizKind)
		}
		return g.buildText(card), nil
	case quiz.ModeSet:
		return topics.Exercise{}, fmt.Errorf("%w: mode=%d on single-answer item %q (quiz_kind %s)", ErrUnsupportedMode, req.Mode, req.Item.Key, g.d.QuizKind)
	default: // quiz.ModeSingle
		if g.d.FixedOptions != nil {
			return g.buildFixed(card), nil
		}
		return g.buildSampled(rng, card, req.Siblings), nil
	}
}

// BuildIntro implements topics.Generator: the teaching card is rendered by
// Parse (Card.Intro), so this is parse + wrap.
func (g *Generator) BuildIntro(_ context.Context, item storage.Item) (topics.IntroCard, error) {
	card, err := g.d.Parse(item.Payload)
	if err != nil {
		return topics.IntroCard{}, fmt.Errorf("%s: item %s: %w", g.d.QuizKind, item.Key, err)
	}
	return topics.IntroCard{Text: card.Intro}, nil
}

// buildFixed builds the fixed-option ModeSingle exercise: every descriptor
// option, verbatim, in declared order, never shuffled.
func (g *Generator) buildFixed(card Card) topics.Exercise {
	return topics.Exercise{
		Mode:          quiz.ModeSingle,
		Prompt:        fmt.Sprintf(g.d.PromptSingle, card.Subject),
		Options:       append([]topics.Option(nil), g.d.FixedOptions...),
		CorrectAnswer: card.Keys[0],
	}
}

// buildSampled builds the sampled ModeSingle MCQ: the correct answer plus up
// to Distractors.Max distractor keys drawn from sibling items' Cards —
// deduped, group-filtered per the policy, sorted, shuffled, capped — then
// the whole option list is shuffled.
func (g *Generator) buildSampled(rng *rand.Rand, card Card, siblings []storage.Item) topics.Exercise {
	target := card.Keys[0]

	seen := make(map[string]bool, len(siblings)+1)
	seen[target] = true
	var candidates []string
	for _, sib := range siblings {
		sc, err := g.d.Parse(sib.Payload)
		if err != nil {
			continue // a malformed sibling must not sink this exercise
		}
		if g.d.Distractors.SameGroup && sc.Group != card.Group {
			continue
		}
		for _, k := range sc.Keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			candidates = append(candidates, k)
		}
	}
	sort.Strings(candidates)
	rng.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	if len(candidates) > g.d.Distractors.Max {
		candidates = candidates[:g.d.Distractors.Max]
	}

	opts := make([]topics.Option, 0, len(candidates)+1)
	opts = append(opts, topics.Option{Key: target, Label: g.d.Label(target)})
	for _, k := range candidates {
		opts = append(opts, topics.Option{Key: k, Label: g.d.Label(k)})
	}
	rng.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })

	return topics.Exercise{
		Mode:          quiz.ModeSingle,
		Prompt:        fmt.Sprintf(g.d.PromptSingle, card.Subject),
		Options:       opts,
		CorrectAnswer: target,
	}
}

// buildText builds the ModeText exercise: accepted spellings from the
// descriptor's Accept hook, canonical answer = the key's display label.
func (g *Generator) buildText(card Card) topics.Exercise {
	target := card.Keys[0]
	return topics.Exercise{
		Mode:          quiz.ModeText,
		Prompt:        fmt.Sprintf(g.d.PromptText, card.Subject),
		Accept:        g.d.Accept(target),
		CorrectAnswer: g.d.Label(target),
		Autocomplete:  g.d.Autocomplete,
	}
}
