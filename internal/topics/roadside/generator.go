// Package roadside is the which-side-of-the-road topic (architecture §6.2,
// quiz_kind "road_side", topic path "roads/which-side"): a country -> which
// side of the road they drive on single-choice quiz, expressed as an
// engine.Descriptor with fixed options — a two-button MCQ that is ALWAYS
// Left then Right, never shuffled (task spec: with only ever two options, a
// stable position beats the usual shuffle-for-fairness treatment other
// topics use).
//
// It never touches storage at generation time: every item's payload caches
// everything the exercise and intro need — the correct side, plus the
// country's flag/name/un_member/gg_coverage — at seed time (seed.go). This
// is the documented resolution of the original "inject a country-lookup
// interface, or cache in payload" choice: every field the generator needs
// is static per-country data already known at seed time, so caching wins
// over a DI seam this package doesn't otherwise need.
package roadside

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Kind is the topics.quiz_kind this package's Generator registers under.
const Kind = "road_side"

// ErrMalformedPayload is returned by BuildExercise/BuildIntro when an item's
// payload isn't the itemPayload shape Seed writes.
var ErrMalformedPayload = errors.New("roadside: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for a
// road_side item (architecture §6.2). Side is the cache of the country's
// drives_on country_facts row and MUST match it — the design's §6.2 audit,
// enforced by this package's integration test.
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

// parseCard adapts an item payload to the engine's Card: the correct fixed
// option key ("left"/"right"), the "🇬🇧 United Kingdom" flag+name pair as
// the prompt subject, and the rendered intro blurb — with a parenthetical
// note for the "interesting" cases (not a UN member, or no official
// GeoGuessr Street View coverage).
func parseCard(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}

	key, side := "left", "LEFT"
	if p.Side == "R" {
		key, side = "right", "RIGHT"
	}

	intro := fmt.Sprintf("🚗 %s %s drives on the %s.", p.Flag, p.Name, side)
	var notes []string
	if !p.UNMember {
		notes = append(notes, "not a UN member state")
	}
	if !p.GGCoverage {
		notes = append(notes, "no official Street View coverage")
	}
	if len(notes) > 0 {
		intro += " (" + strings.Join(notes, "; ") + ")"
	}

	return engine.Card{
		Keys:    []string{key},
		Subject: p.Flag + " " + p.Name,
		Intro:   intro,
	}, nil
}

// descriptor declares the whole topic for the engine: tree, parse, prompt,
// and the fixed never-shuffled Left/Right option pair.
var descriptor = engine.Descriptor{
	QuizKind: Kind,
	Topic: []engine.TopicNode{
		{Slug: RootSlug, Name: RootName, Position: rootPosition},
		{Slug: TopicSlug, Name: TopicName, Position: topicPosition, BaseTier: BaseTier, QuizKind: Kind, ExerciseModes: []string{"single"}, IsQuizzable: true},
	},
	Parse:        parseCard,
	PromptSingle: "%s — which side of the road do they drive on?",
	FixedOptions: []topics.Option{
		{Key: "left", Label: "⬅️ Left"},
		{Key: "right", Label: "➡️ Right"},
	},
}

// New builds the road-side Generator (the generic engine one, driven by
// this package's descriptor). New does NOT self-register via init(): wiring
// a Generator into the topics registry is deferred to cmd/bot (architecture
// §8), same as specialchars and words.
func New() *engine.Generator { return engine.New(descriptor) }
