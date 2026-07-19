// Package profiles is geodrill's country -> language quiz
// (vibe/design-country-profiles.md, narrowed by the P3.3 task brief to one
// quizzable topic over three seeded country_facts): "What language do you
// see on signs in 🇰🇪 Kenya?" — single-choice, with distractors drawn from
// OTHER countries in the SAME geographic region (Card.Group = region,
// engine.DistractorPolicy.SameGroup), so a question about an East African
// country never offers a Northern European language as a wrong option. The
// other two facts this package seeds (main_religion, region) are stored for
// future sibling quizzes (per the design doc) but have no generator here yet
// — only country->language is quizzable this wave.
//
// # Why this package wraps engine.Generator instead of using it bare
//
// Many countries in seeds/country_profiles.yaml are multi-language (e.g.
// Kenya: Swahili, English) and Card.Keys must stay length 1 for every item —
// including multi-language countries — to keep ModeSingle available (the
// engine's dispatch requires ModeSet+BuildSet the moment an item's Keys has
// more than one entry, and this topic never implements BuildSet). So
// CorrectAnswer is always the country's PRIMARY sign language
// (Languages[0]), and the same-region candidate pool is built from other
// countries' own primary languages only.
//
// That creates a real correctness gap the generic engine.Distractors sampler
// cannot see: colonial/regional language reuse means a same-region country's
// PRIMARY language is frequently one of the TARGET country's own secondary
// languages (162 such collisions across the real 247-country dataset — e.g.
// Uganda's primary "English" would be a technically-also-correct distractor
// for Kenya's "Swahili" question, since English is also spoken in Kenya).
// engine.buildSampled only ever excludes the target's Keys[0] (via its
// internal `seen` dedup set) — it has no hook to exclude keys that don't
// belong to the target item at all. Fixing this needs both the target's
// correct key AND its full language list at once, which only this package
// (not engine, which sees siblings one at a time) has together.
//
// So Generator wraps engine.New(descriptor) with the SAME same-region
// Distractors policy, but with the engine's Max generously set (comfortably
// above the largest region's distinct-primary-language count — Southern
// Europe currently tops out at 13) so the FULL same-region candidate pool
// survives engine-side sampling; this package's BuildExercise then
// post-filters engine's Options to drop any candidate that's ALSO one of the
// target country's own languages, before capping to the visible distractor
// count. Everything else — parsing, intro cards, prompt rendering, dedup,
// shuffling, determinism, the under-fill fallback for thin regions (e.g.
// North Africa's 7 countries share a single distinct primary language, so
// its questions surface with zero distractors — a degenerate but valid
// single-choice exercise, never an error) — stays the engine's.
package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Kind is the topics.quiz_kind this package's Generator registers under.
const Kind = "country_language"

// engineDistractorMax is the Max passed to the engine's DistractorPolicy: it
// must be at least as large as the biggest region's distinct-primary-language
// count (13, Southern Europe, as of the committed seed data) so the engine's
// same-region sampler returns the FULL candidate pool — this package's own
// exclusion filter (see package doc) then decides the final visible set,
// rather than the engine truncating the pool before exclusion gets a chance
// to work with a full picture.
const engineDistractorMax = 24

// maxVisibleDistractors is the number of distractor options actually shown
// alongside the correct answer, after this package's exclusion filter runs
// (mirrors tld/capitals' maxDistractors=3 convention).
const maxVisibleDistractors = 3

// ErrMalformedPayload is returned (wrapped) by BuildExercise/BuildIntro when
// an item's payload isn't the itemPayload shape Seed writes.
var ErrMalformedPayload = errors.New("profiles: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for a profiles
// item. Region and Languages are caches of the region/languages_spoken
// country_facts rows (the single source of truth — consistency asserted by
// the integration test); Languages is ordered primary-first, exactly as
// seeds/country_profiles.yaml lists them.
type itemPayload struct {
	Flag      string   `json:"flag"`
	Name      string   `json:"name"`
	ISOA2     string   `json:"iso_a2"`
	ISOA3     string   `json:"iso_a3"`
	Region    string   `json:"region"`
	Languages []string `json:"languages"`
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
	if p.Name == "" || p.ISOA2 == "" || p.Region == "" || len(p.Languages) == 0 {
		return itemPayload{}, ErrMalformedPayload
	}
	for _, l := range p.Languages {
		if l == "" {
			return itemPayload{}, ErrMalformedPayload
		}
	}
	return p, nil
}

// languageKey slugifies a language display name into a stable answer key:
// lowercase ASCII letters/digits, runs of anything else collapsed to a single
// "-". It only needs to be stable and collision-free across the dataset's
// language names (checked by loadLookupTables at wiring time), not
// human-readable — display always goes through the Labels table.
func languageKey(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	if s == "" {
		return "unknown"
	}
	return s
}

// joinLanguages renders a natural-language list ("Swahili", "Swahili and
// English", "Catalan, French and Spanish") for the intro card.
func joinLanguages(langs []string) string {
	switch len(langs) {
	case 0:
		return ""
	case 1:
		return langs[0]
	case 2:
		return langs[0] + " and " + langs[1]
	default:
		return strings.Join(langs[:len(langs)-1], ", ") + " and " + langs[len(langs)-1]
	}
}

// introText renders the teaching blurb: "you'll be asked to recall this"
// framing per design §7, phrased around what you'd actually see on signs.
func introText(p itemPayload) string {
	verb := "is"
	noun := "language"
	if len(p.Languages) > 1 {
		verb = "are"
		noun = "languages"
	}
	return fmt.Sprintf("%s %s's sign-visible %s %s %s.", p.Flag, p.Name, noun, verb, joinLanguages(p.Languages))
}

// parseCard maps a payload to the engine Card: the answer key is the
// slugified PRIMARY language (Languages[0]), the distractor-compatibility
// group is the raw region string (compared for plain equality by
// DistractorPolicy.SameGroup, never displayed), the prompt subject is the
// flag+name pair, and the intro is the direction-agnostic teaching blurb.
func parseCard(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    []string{languageKey(p.Languages[0])},
		Group:   p.Region,
		Subject: p.Flag + " " + p.Name,
		Intro:   introText(p),
	}, nil
}

// lookupTables holds the language-key -> canonical display name table the
// engine's Labels needs (see the package doc; mirrors tld/capitals'
// lookupTables, one table instead of two since this topic has only one
// answer-key space).
type lookupTables struct {
	languageLabels map[string]string
}

var (
	tablesOnce sync.Once
	tables     lookupTables
	tablesErr  error
)

// loadLookupTables builds (once per process) the language-key -> display
// name table every primary language in the committed seed file needs — only
// primary languages ever become a Card.Keys entry (see package doc), so only
// primary languages need a label. Collisions (two different display names
// slugifying to the same key) are treated as a wiring-time error, same as a
// malformed seed file.
func loadLookupTables() (lookupTables, error) {
	tablesOnce.Do(func() {
		sf, err := loadProfilesFile(profilesSeedPath())
		if err != nil {
			tablesErr = err
			return
		}
		labels := make(map[string]string, len(sf.Profiles))
		for _, e := range sf.Profiles {
			if len(e.Languages) == 0 {
				continue
			}
			primary := e.Languages[0]
			key := languageKey(primary)
			if existing, ok := labels[key]; ok && existing != primary {
				tablesErr = fmt.Errorf("profiles: language key %q collides between %q and %q", key, existing, primary)
				return
			}
			labels[key] = primary
		}
		tables = lookupTables{languageLabels: labels}
	})
	return tables, tablesErr
}

// Generator wraps the generic engine.Generator to add the own-language
// distractor exclusion described in the package doc. It implements
// topics.Generator directly rather than being an *engine.Generator, unlike
// the simpler topics (roadside, tld, capitals) that need no post-processing.
type Generator struct {
	inner *engine.Generator
}

var _ topics.Generator = (*Generator)(nil)

// New builds the country->language Generator. It panics on a broken seed
// file (a wiring-time error, consistent with engine.New's invalid-descriptor
// panic and tld/capitals' own New).
func New() *Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("profiles: build %s descriptor: %v", Kind, err))
	}
	d := engine.Descriptor{
		QuizKind:     Kind,
		Topic:        languageTopic(),
		Parse:        parseCard,
		Labels:       t.languageLabels,
		PromptSingle: "What language do you see on signs in %s?",
		Distractors:  engine.DistractorPolicy{Max: engineDistractorMax, SameGroup: true},
	}
	return &Generator{inner: engine.New(d)}
}

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return g.inner.Kind() }

// BuildIntro implements topics.Generator: no post-processing needed, the
// intro card doesn't involve distractors.
func (g *Generator) BuildIntro(ctx context.Context, item storage.Item) (topics.IntroCard, error) {
	return g.inner.BuildIntro(ctx, item)
}

// BuildExercise implements topics.Generator: it delegates to the wrapped
// engine.Generator (which does the actual parsing, same-region sampling,
// dedup and shuffling) and then, for ModeSingle only, filters out any
// distractor option that's one of the TARGET country's own languages before
// capping to maxVisibleDistractors (see package doc for why this can't be
// done inside the engine's own sampler).
func (g *Generator) BuildExercise(ctx context.Context, rng *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	ex, err := g.inner.BuildExercise(ctx, rng, req)
	if err != nil || ex.Mode != quiz.ModeSingle {
		return ex, err
	}

	p, perr := parsePayload(req.Item.Payload)
	if perr != nil {
		// The wrapped call above already parsed this same payload
		// successfully (via the same Parse func), so this should be
		// unreachable — but fail safe by returning the engine's result
		// unfiltered rather than losing a working exercise.
		return ex, nil
	}
	exclude := make(map[string]bool, len(p.Languages))
	for _, l := range p.Languages {
		exclude[languageKey(l)] = true
	}

	filtered := make([]topics.Option, 0, len(ex.Options))
	for _, o := range ex.Options {
		if o.Key == ex.CorrectAnswer || !exclude[o.Key] {
			filtered = append(filtered, o)
		}
	}
	ex.Options = capOptions(filtered, ex.CorrectAnswer, maxVisibleDistractors)
	return ex, nil
}

// capOptions trims opts (already deduped, region-filtered, own-language-
// filtered, and shuffled by the engine) down to the correct answer plus at
// most maxDistractors others, preserving the engine's shuffle order — a
// uniformly-shuffled sequence's surviving subsequence, taken in order, stays
// uniformly random, so no reshuffle is needed here.
func capOptions(opts []topics.Option, correctAnswer string, maxDistractors int) []topics.Option {
	if len(opts) <= maxDistractors+1 {
		return opts
	}
	out := make([]topics.Option, 0, maxDistractors+1)
	kept := 0
	for _, o := range opts {
		if o.Key == correctAnswer {
			out = append(out, o)
			continue
		}
		if kept < maxDistractors {
			out = append(out, o)
			kept++
		}
	}
	return out
}
