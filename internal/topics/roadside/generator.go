// Package roadside implements the topics.Generator for quiz_kind
// "road_side" (architecture §6.2, topic path "roads/which-side"): a
// country -> which side of the road they drive on single-choice quiz. Like
// specialchars and words, it never touches storage directly (see the
// internal/topics package doc's "content access stays out of this package"
// contract): every item's payload caches everything BuildExercise/
// BuildIntro need — the correct side, plus the country's flag/name/
// un_member/gg_coverage — at seed time (see Seed in seed.go), so no
// DB-backed country lookup is required at generation time. This is the
// documented resolution of the task brief's "inject a country-lookup
// interface, or cache in payload — decide, document" choice: every field
// the generator needs is static per-country data already known at seed
// time, so caching wins over adding a DI seam this package doesn't
// otherwise need.
package roadside

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// Kind is the topics.quiz_kind this package's Generator registers under.
const Kind = "road_side"

// ErrMalformedPayload is returned by BuildExercise/BuildIntro when an item's
// payload isn't the itemPayload shape Seed writes.
var ErrMalformedPayload = errors.New("roadside: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for a
// road_side item (architecture §6.2). Side is the cache of the country's
// drives_on country_facts row and MUST match it — the design's §6.2 audit,
// enforced by this package's integration test. Flag/Name/UNMember/
// GGCoverage are cached at seed time so this generator never needs a
// DB-backed country lookup (see the package doc).
type itemPayload struct {
	Side       string `json:"side"` // "L" or "R"
	Flag       string `json:"flag"`
	Name       string `json:"name"`
	UNMember   bool   `json:"un_member"`
	GGCoverage bool   `json:"gg_coverage"`
}

// parsePayload decodes and validates an item's payload.
func parsePayload(raw []byte) (itemPayload, error) {
	if len(raw) == 0 {
		return itemPayload{}, ErrMalformedPayload
	}
	var p itemPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return itemPayload{}, fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}
	if p.Name == "" || (p.Side != "L" && p.Side != "R") {
		return itemPayload{}, ErrMalformedPayload
	}
	return p, nil
}

// Generator implements topics.Generator for Kind. It is stateless:
// BuildExercise/BuildIntro read only the item they're given, so a single
// instance is safe to share and register once.
type Generator struct{}

// New builds a road-side Generator. No dependencies are injected — see the
// package doc for why BuildExercise/BuildIntro never need a DB-backed
// country lookup. New does NOT self-register via init(): wiring a
// Generator into the topics registry is deferred to cmd/bot (architecture
// §8), same as specialchars and words.
func New() *Generator { return &Generator{} }

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return Kind }

// BuildExercise implements topics.Generator (ModeSingle only): a fixed,
// never-shuffled two-option MCQ — Left then Right, always in that order
// (task spec: with only ever two options, a stable position beats the
// usual shuffle-for-fairness treatment other topics use). rng and ctx are
// accepted only for Generator-interface compliance.
func (g *Generator) BuildExercise(_ context.Context, _ *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	p, err := parsePayload(req.Item.Payload)
	if err != nil {
		return topics.Exercise{}, fmt.Errorf("roadside: item %s: %w", req.Item.Key, err)
	}

	correct := "left"
	if p.Side == "R" {
		correct = "right"
	}

	return topics.Exercise{
		Mode:   quiz.ModeSingle,
		Prompt: fmt.Sprintf("%s %s — which side of the road do they drive on?", p.Flag, p.Name),
		Options: []topics.Option{
			{Key: "left", Label: "⬅️ Left"},
			{Key: "right", Label: "➡️ Right"},
		},
		CorrectAnswer: correct,
	}, nil
}

// BuildIntro implements topics.Generator: the teaching blurb shown before
// an item's first exercise (architecture §5.1), with a parenthetical note
// for the "interesting" cases the task brief calls out — not a UN member,
// or no official GeoGuessr Street View coverage.
func (g *Generator) BuildIntro(_ context.Context, item storage.Item) (topics.IntroCard, error) {
	p, err := parsePayload(item.Payload)
	if err != nil {
		return topics.IntroCard{}, fmt.Errorf("roadside: item %s: %w", item.Key, err)
	}

	side := "LEFT"
	if p.Side == "R" {
		side = "RIGHT"
	}
	text := fmt.Sprintf("🚗 %s %s drives on the %s.", p.Flag, p.Name, side)

	var notes []string
	if !p.UNMember {
		notes = append(notes, "not a UN member state")
	}
	if !p.GGCoverage {
		notes = append(notes, "no official Street View coverage")
	}
	if len(notes) > 0 {
		text += " (" + strings.Join(notes, "; ") + ")"
	}

	return topics.IntroCard{Text: text}, nil
}
