// Package flags is geodrill's flag-recognition quiz (quiz_kind
// "flag_country", vibe/design-flags-quiz.md): a flag PHOTO -> country name
// quiz, built on the universal topic engine (internal/topics/engine) plus a
// thin wrapper that adds the one capability the generic engine doesn't have
// yet — attaching a MediaPath (photo-from-birth, architecture §5.1 decision
// 6) to an Exercise/IntroCard. engine.Card has no MediaPath field and
// engine.Generator's BuildExercise/BuildIntro never set topics.Exercise's or
// topics.IntroCard's MediaPath, so this package's Generator wraps an inner
// *engine.Generator (built from an engine.Descriptor, exactly like every
// other topic) and patches MediaPath onto whatever the inner generator
// returns — no engine package change needed (out of this package's file
// scope; a genuine gap the design doc calls out for the "first media topic"
// to work around).
//
// One topic mixes two item shapes, mirroring specialchars' single/subgroup
// split:
//
//   - single items (one country, items.country_id set): the main mode is
//     ModeText via inline-mode AUTOCOMPLETE over country names (the topic's
//     exercise_modes includes "autocomplete") — "Which country is this?"
//     shown as the flag's photo caption, or (content-availability fallback,
//     when no image has been ingested for this item yet) an emoji-prefixed
//     text-only question, no MediaPath.
//   - confusable-group items (items.country_id NULL, key = sorted iso list):
//     set-choice ONLY (ModeSet, via the descriptor's BuildSet hook) —
//     "Which countries could this flag belong to?", one randomly-chosen
//     member's flag shown as the photo, options are the true member set plus
//     swapped-one-member distractor sets (mirrors
//     specialchars.buildSetMCQ/buildDistractorSets).
//
// engine.BuildExercise dispatches purely on item shape and req.Mode
// (len(Card.Keys) > 1 always routes to BuildSet; a single-key item never
// builds under ModeSet), so a topic exercise_modes of {"autocomplete","set"}
// is sufficient for both shapes to always land on their one intended mode:
// modeRotationOrder (internal/study) tries every configured mode for a turn,
// and "set" always fails to build on a single-key item (and vice versa),
// so whichever mode fits the item's shape is the one that actually builds.
package flags

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Kind is the topics.quiz_kind this package's Generator registers under.
const Kind = "flag_country"

// DefaultMediaRoot is the flag PNG cache directory (design §5): gitignored,
// populated locally by scripts/fetch-flags.sh and ingested into media_files
// by cmd/flagassets. Image paths stored in items.payload are bare filenames
// relative to this root (e.g. "fr.png"), joined here at generation time.
const DefaultMediaRoot = "data/flags"

// maxSingleDistractors caps the (production-unused but descriptor-valid)
// single-choice MCQ fallback — the topic's exercise_modes never selects
// "single", but engine.Descriptor.Validate requires a valid single-choice
// config on every descriptor (mirrors tld.maxDistractors / cities.maxDistractors).
const maxSingleDistractors = 3

// maxSetDistractors caps the confusable-group set-choice MCQ: target set +
// up to this many swapped-one-member distractor sets (mirrors
// specialchars.maxSetDistractors).
const maxSetDistractors = 3

// ErrMalformedPayload is returned (wrapped) by parsePayload when an item's
// payload isn't one of the two shapes Seed writes (single or confusable-group).
var ErrMalformedPayload = errors.New("flags: malformed item payload")

// itemPayload is the items.payload shape for BOTH item shapes this topic
// seeds (design §3): a single item sets FlagEmoji/Image/Name/ISOA2/ISOA3/
// IsSubdivision and leaves Countries empty; a confusable-group item sets
// Countries/Images/Label and leaves the single-item fields empty. Parse
// discriminates on len(Countries) > 0 (isGroup).
type itemPayload struct {
	// Single-item fields.
	FlagEmoji     string `json:"flag_emoji,omitempty"`
	Image         string `json:"image,omitempty"` // bare filename under the media root; "" = no asset ingested yet
	Name          string `json:"name,omitempty"`
	ISOA2         string `json:"iso_a2,omitempty"`
	ISOA3         string `json:"iso_a3,omitempty"`
	IsSubdivision bool   `json:"is_subdivision,omitempty"`

	// Confusable-group fields (design §3/§5a).
	Countries []string `json:"countries,omitempty"`
	Images    []string `json:"images,omitempty"` // same order/length as Countries
	Label     string   `json:"label,omitempty"`  // e.g. "Chad / Romania"
}

// isGroup reports whether p is a confusable-group item's payload.
func isGroup(p itemPayload) bool { return len(p.Countries) > 0 }

// parsePayload decodes and validates an item's payload against whichever of
// the two shapes it claims to be.
func parsePayload(raw []byte) (itemPayload, error) {
	if len(raw) == 0 {
		return itemPayload{}, ErrMalformedPayload
	}
	var p itemPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return itemPayload{}, fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}
	if isGroup(p) {
		if len(p.Images) != len(p.Countries) || p.Label == "" {
			return itemPayload{}, ErrMalformedPayload
		}
		return p, nil
	}
	if p.Name == "" || p.ISOA2 == "" {
		return itemPayload{}, ErrMalformedPayload
	}
	return p, nil
}

// singleQuestion renders the ModeText prompt for a single item: a flat
// question when a photo is attached (image != ""; the flag itself is shown
// as the photo, so the text needs no emoji), or an emoji-prefixed fallback
// question when no asset has been ingested yet for this item (design §6's
// content-availability fallback, not a user-selectable mode).
func singleQuestion(p itemPayload) string {
	if p.Image != "" {
		return "Which country is this?"
	}
	return fmt.Sprintf("%s — which country is this?", p.FlagEmoji)
}

// singleIntro renders a single item's introduction-card text.
func singleIntro(p itemPayload) string {
	return fmt.Sprintf("%s This is %s's flag.", p.FlagEmoji, p.Name)
}

// groupIntro renders a confusable-group item's introduction-card text.
func groupIntro(p itemPayload) string {
	return fmt.Sprintf("🚩 %s look alike — learn to tell them apart.", p.Label)
}

// parseCard adapts an item payload to the engine's Card.
//
// For a confusable-group item, Card.Group is deliberately repurposed to
// smuggle the member images list through to the descriptor's BuildSet hook
// (buildSetMCQ): BuildSet only receives (rng, card, siblings) — no access to
// req.Item's raw payload — so Card's two free-form string fields (Group,
// Subject) are this package's only channel for everything BuildSet needs
// about the target item beyond its answer keys. Subject carries the
// authored label (design §5a, e.g. "Chad / Romania"); Group carries the
// comma-joined image filenames (never containing a comma themselves, so the
// join is unambiguous and needs no escaping). This is private to this
// package — the engine only ever treats Group as an opaque string.
func parseCard(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	if isGroup(p) {
		return engine.Card{
			Keys:    append([]string(nil), p.Countries...),
			Group:   strings.Join(p.Images, ","),
			Subject: p.Label,
			Intro:   groupIntro(p),
		}, nil
	}
	return engine.Card{
		Keys:    []string{p.ISOA2},
		Subject: singleQuestion(p),
		Intro:   singleIntro(p),
	}, nil
}

// countryAliases adds a few common alternative spellings to the accepted
// country answers, on top of each country's canonical name + iso2 + iso3 —
// an exact copy of tld.countryAliases / capitals.countryAliases / cities.countryAliases
// (the established pattern: "a local copy is fine").
var countryAliases = map[string][]string{
	"US": {"USA", "United States of America", "America"},
	"GB": {"UK", "Britain", "Great Britain", "England"},
	"AE": {"UAE"},
	"KR": {"Korea"},
	"CZ": {"Czechia"},
	"CD": {"DR Congo", "Democratic Republic of the Congo"},
	"RU": {"Russian Federation"},
	"NL": {"Holland"},
}

// lookupTables holds the iso2-keyed label and accepted-spelling maps the
// engine's key-only ModeText hooks need, plus the same country-name lookup
// for rendering confusable-group distractor-set labels (mirrors
// tld.lookupTables / capitals.lookupTables / cities.lookupTables).
type lookupTables struct {
	countryLabels map[string]string   // iso2 -> country display name (CorrectAnswer, set-option labels)
	countryAccept map[string][]string // iso2 -> accepted country spellings
}

var (
	tablesOnce sync.Once
	tables     lookupTables
	tablesErr  error
)

// loadLookupTables builds (once per process) the iso2-keyed table
// Accept/Labels close over, from the committed seeds/countries.yaml.
func loadLookupTables() (lookupTables, error) {
	tablesOnce.Do(func() {
		countries, err := engine.LoadCountriesFile(countriesSeedPath())
		if err != nil {
			tablesErr = fmt.Errorf("flags: %w", err)
			return
		}

		t := lookupTables{
			countryLabels: make(map[string]string, len(countries)),
			countryAccept: make(map[string][]string, len(countries)),
		}
		for _, c := range countries {
			t.countryLabels[c.ISOA2] = c.Name
			spellings := []string{c.Name, c.ISOA2}
			if c.ISOA3 != "" {
				spellings = append(spellings, c.ISOA3)
			}
			if a, ok := countryAliases[c.ISOA2]; ok {
				spellings = append(spellings, a...)
			}
			t.countryAccept[c.ISOA2] = spellings
		}
		tables = t
	})
	return tables, tablesErr
}

// setLabelFor renders a human label for a country-code slice, in the given
// order (mirrors specialchars.setLabelInOrder), falling back to the bare
// code for any iso2 missing from labels (degraded-label-beats-failure, the
// same convention engine.Descriptor.Label uses).
func setLabelFor(codes []string, labels map[string]string) string {
	names := make([]string, len(codes))
	for i, c := range codes {
		if n, ok := labels[c]; ok {
			names[i] = n
		} else {
			names[i] = c
		}
	}
	return strings.Join(names, " / ")
}

// siblingCountryPool returns eligible replacement ISO codes for the
// one-member-swap distractor algorithm (design §6): every other group
// item's member countries, plus every single item's own country — excluding
// anything already in target. Malformed siblings are skipped (a bad sibling
// must not sink this exercise), mirroring specialchars.sameScriptLanguages.
func siblingCountryPool(siblings []storage.Item, target quiz.KeySet) []string {
	inTarget := make(map[string]bool, len(target))
	for _, k := range target {
		inTarget[k] = true
	}
	seen := map[string]bool{}
	var out []string
	add := func(code string) {
		if inTarget[code] || seen[code] {
			return
		}
		seen[code] = true
		out = append(out, code)
	}
	for _, sib := range siblings {
		p, err := parsePayload(sib.Payload)
		if err != nil {
			continue
		}
		if isGroup(p) {
			for _, c := range p.Countries {
				add(c)
			}
			continue
		}
		add(p.ISOA2)
	}
	return out
}

// buildDistractorSets builds up to max distinct, same-size distractor sets
// by swapping exactly one member of target for a replacement drawn from
// pool (excluding replacements already in target) — an exact copy of
// specialchars.buildDistractorSets' algorithm (design §6: "the same
// swap-one-member algorithm"), generalized from language codes to country
// codes. Deterministic: the candidate list is built in a fixed (caller-sorted
// pool) order and deduplicated before a single rng.Shuffle picks which ones
// survive the cap.
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

// buildSetMCQ is the descriptor's BuildSet hook: the ModeSet "which
// countries could this flag belong to?" exercise for confusable-group items.
// mediaRoot and labels are closed over by New (they're per-process config,
// not per-call state).
func buildSetMCQ(rng *rand.Rand, card engine.Card, siblings []storage.Item, mediaRoot string, labels map[string]string) (topics.Exercise, error) {
	if card.Group == "" {
		return topics.Exercise{}, fmt.Errorf("%w: confusable-group item has no member images", ErrMalformedPayload)
	}
	images := strings.Split(card.Group, ",")
	if len(images) != len(card.Keys) {
		return topics.Exercise{}, fmt.Errorf("%w: confusable-group item has %d images for %d countries", ErrMalformedPayload, len(images), len(card.Keys))
	}

	targetKeys := quiz.CanonSet(card.Keys...)
	// design §3: BuildExercise picks which member's image to show for a
	// group item every time, varying which member is displayed across
	// repetitions — order doesn't matter for a uniform random pick among
	// the group's own images, so no correspondence with targetKeys (sorted)
	// is needed here.
	mediaPath := filepath.Join(mediaRoot, images[rng.Intn(len(images))])

	pool := siblingCountryPool(siblings, targetKeys)
	sort.Strings(pool)
	distractors := buildDistractorSets(rng, targetKeys, pool, maxSetDistractors)

	optionSets := make([]topics.OptionSet, 0, len(distractors)+1)
	optionSets = append(optionSets, topics.OptionSet{Keys: append([]string(nil), targetKeys...), Label: card.Subject})
	for _, d := range distractors {
		optionSets = append(optionSets, topics.OptionSet{Keys: append([]string(nil), d...), Label: setLabelFor(d, labels)})
	}
	rng.Shuffle(len(optionSets), func(i, j int) { optionSets[i], optionSets[j] = optionSets[j], optionSets[i] })

	return topics.Exercise{
		Mode:          quiz.ModeSet,
		Prompt:        "Which countries could this flag belong to?",
		MediaPath:     mediaPath,
		OptionSets:    optionSets,
		CorrectAnswer: strings.Join(targetKeys, ","),
	}, nil
}

// Generator wraps the generic engine.Generator to add MediaPath support
// (this package's doc comment explains why): every BuildExercise/BuildIntro
// call delegates to the inner generator first, then patches MediaPath onto
// the result for single items with an ingested image (confusable-group
// items already get MediaPath from buildSetMCQ, which sets it directly).
type Generator struct {
	inner     *engine.Generator
	mediaRoot string
}

var _ topics.Generator = (*Generator)(nil)

// New builds the flags Generator against DefaultMediaRoot — the constructor
// cmd/bot wires (topics.Register(flags.New())), matching every other
// topic's no-arg New() convention.
func New() *Generator { return NewWithMediaRoot(DefaultMediaRoot) }

// NewWithMediaRoot builds the flags Generator with an explicit media root
// (design §6: "constructed with the media root path injected") — used by
// tests so they don't depend on DefaultMediaRoot resolving relative to the
// process's working directory. Panics on a broken seeds/countries.yaml (a
// wiring-time error, consistent with engine.New's invalid-descriptor panic
// and every other topic package's New).
func NewWithMediaRoot(mediaRoot string) *Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("flags: build %s descriptor: %v", Kind, err))
	}
	accept := t.countryAccept
	labels := t.countryLabels

	d := engine.Descriptor{
		QuizKind: Kind,
		Topic:    flagsTopic(),
		Parse:    parseCard,
		Labels:   labels,
		// PromptSingle/PromptText are "%s": Card.Subject already carries the
		// fully-rendered question (singleQuestion) — flags needs no separate
		// template since the photo-vs-emoji-fallback distinction is decided
		// entirely inside Parse, not by which template is selected.
		PromptSingle: "%s",
		PromptText:   "%s",
		Distractors:  engine.DistractorPolicy{Max: maxSingleDistractors},
		Accept: func(key string) []string {
			if names, ok := accept[key]; ok {
				return names
			}
			return []string{key}
		},
		BuildSet: func(rng *rand.Rand, card engine.Card, siblings []storage.Item) (topics.Exercise, error) {
			return buildSetMCQ(rng, card, siblings, mediaRoot, labels)
		},
	}
	return &Generator{inner: engine.New(d), mediaRoot: mediaRoot}
}

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return g.inner.Kind() }

// BuildExercise implements topics.Generator: delegates to the inner engine
// Generator, then attaches MediaPath for a single item with an ingested
// image (req.Item.Payload.image != ""). Confusable-group items already
// carry MediaPath from buildSetMCQ, so this only ever patches an empty one.
func (g *Generator) BuildExercise(ctx context.Context, rng *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	ex, err := g.inner.BuildExercise(ctx, rng, req)
	if err != nil {
		return topics.Exercise{}, err
	}
	if ex.MediaPath == "" {
		if p, perr := parsePayload(req.Item.Payload); perr == nil && !isGroup(p) && p.Image != "" {
			ex.MediaPath = filepath.Join(g.mediaRoot, p.Image)
		}
	}
	return ex, nil
}

// BuildIntro implements topics.Generator: delegates to the inner engine
// Generator for the rendered text, then attaches MediaPath — a single
// item's own image when present, or a confusable-group item's first member
// image (no rng available in this call to pick randomly; BuildExercise is
// what varies which member is shown across repetitions).
func (g *Generator) BuildIntro(ctx context.Context, item storage.Item) (topics.IntroCard, error) {
	card, err := g.inner.BuildIntro(ctx, item)
	if err != nil {
		return topics.IntroCard{}, err
	}
	if p, perr := parsePayload(item.Payload); perr == nil {
		switch {
		case isGroup(p) && len(p.Images) > 0:
			card.MediaPath = filepath.Join(g.mediaRoot, p.Images[0])
		case !isGroup(p) && p.Image != "":
			card.MediaPath = filepath.Join(g.mediaRoot, p.Image)
		}
	}
	return card, nil
}
