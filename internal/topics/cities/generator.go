// Package cities is the "which city is at the marker?" quiz — a map-based
// city-recognition topic (vibe/design-cities-on-map.md, which supersedes the
// older city->country direction). The question shows a static, label-free
// map with a red marker at the city's location and the user TYPES the city
// name (ModeText via inline-mode AUTOCOMPLETE over city-name suggestions);
// the intro card ("city discovered") shows the same map plus a rich caption
// (country, region, population, elevation, and — for the biggest cities — a
// short scraped fact). The answer is the CITY (studied biggest -> smallest
// via items.position = population rank); city names, not countries, are the
// accepted spellings.
//
// # Media wrapper (no IO), mirroring internal/topics/flags
//
// The map image lives in Garage S3, not on local disk, so the generator does
// NO filesystem or network probe: whether an image exists was decided at seed
// time (seed.go sets itemPayload.MapImage only for images already registered
// in media_files). This package's Generator wraps an inner *engine.Generator
// (built from an engine.Descriptor exactly like every other topic) and, per
// item, either attaches MediaPath = MediaRootRef + "/" + MapImage (when
// present) or degrades to the §2 text fallback (a still-answerable text
// question / a text-only intro) — the same MediaPath-patching shape flags
// uses, minus the disk lookup.
//
// # Why this package loads seeds/cities.yaml at wiring time
//
// The engine's ModeText hooks are key-only: Descriptor.Accept is
// func(key)[]string and CorrectAnswer is Descriptor.Label(key) — neither can
// see items.payload. A city's accepted spellings (its exonym + GeoNames
// native name/asciiname alternates) and its display label are per-city
// reference data not derivable from a bare key, so this package builds a
// key -> {label, accepted spellings} table once (loadLookupTables) from the
// committed seeds/cities.yaml (mirrors flags/tld's load-once-at-New pattern,
// but keyed on the city key rather than a country iso2).
package cities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Kind is this package's sole quiz_kind.
const Kind = "city_on_map"

// MediaRootRef is the garage:// ref prefix under which city-map images are
// registered in media_files. It MUST match cmd/citymapsync's bucket default
// ("apps-geodrill") + "citymaps/" key prefix (vibe/design-cities-on-map.md
// §5): the seeder registers refs of the form
// "garage://apps-geodrill/citymaps/<file>", and this generator reconstructs
// the same ref as the Exercise/IntroCard MediaPath.
const MediaRootRef = "garage://apps-geodrill/citymaps"

// maxDistractors caps the single-choice MCQ fallback. The topic is configured
// for "autocomplete" (ModeText), so this fallback is never exercised in
// production — but the engine's Descriptor.Validate requires a valid
// single-choice config (FixedOptions or Distractors.Max >= 1) on every
// descriptor, and a sampled sibling-city MCQ is the sensible thing for it to
// be (mirrors flags.maxSingleDistractors / tld.maxDistractors).
const maxDistractors = 3

// captionLimit is Telegram's photo-caption cap; introCaption stays under it
// (audited in audit_test.go).
const captionLimit = 1024

// ErrMalformedPayload is returned (wrapped) by parsePayload when an item's
// payload isn't the itemPayload shape Seed writes.
var ErrMalformedPayload = errors.New("cities: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for a cities
// item (vibe/design-cities-on-map.md §7). It caches everything the generator
// needs to render the question, the map ref, and the intro caption — so
// BuildExercise/BuildIntro stay pure, no DB lookup at generation time.
type itemPayload struct {
	Key         string  `json:"key"` // items.key echoed — engine hooks are key-only
	CityName    string  `json:"city_name"`
	Flag        string  `json:"flag"`
	CountryName string  `json:"country_name"`
	ISOA2       string  `json:"iso_a2"`
	ISOA3       string  `json:"iso_a3"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	Region      string  `json:"region,omitempty"`
	Population  int64   `json:"population"`
	ElevationM  *int    `json:"elevation_m,omitempty"`
	MapImage    string  `json:"map_image"`      // bare filename; "" = not synced to Garage yet
	Fact        string  `json:"fact,omitempty"` // tiers 0-2 only
	FactURL     string  `json:"fact_url,omitempty"`
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
	if p.Key == "" || p.CityName == "" || p.ISOA2 == "" || p.CountryName == "" {
		return itemPayload{}, ErrMalformedPayload
	}
	return p, nil
}

// reviewSubject is the map-question prompt shown as the photo caption (the
// whole question is carried through Card.Subject and rendered by the "%s"
// prompt template — flags precedent).
const reviewSubject = "📍 Which city is at the red marker? Type its name."

// formatInt renders n with comma thousands-grouping (e.g. 1512491 ->
// "1,512,491"). Local helper — the intro caption's population line.
func formatInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// factHost renders the visible attribution target for a fact source URL: its
// host + path (e.g. "https://en.wikipedia.org/wiki/Munich" ->
// "en.wikipedia.org/wiki/Munich"). Falls back to the raw URL if it doesn't
// parse.
func factHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host + u.Path
}

// introCaption renders the "city discovered" intro-card caption
// (vibe/design-cities-on-map.md §2): a header block (city/country, region,
// population, elevation — each line omitted when its datum is missing), then,
// only when a fact blurb exists, a blank line + the blurb + a blank line + the
// CC BY-SA 4.0 attribution. Plain text (the intro sender HTML-escapes it);
// kept under Telegram's caption cap.
func introCaption(p itemPayload) string {
	lines := []string{fmt.Sprintf("📍 %s — %s %s", p.CityName, p.Flag, p.CountryName)}
	if p.Region != "" {
		lines = append(lines, fmt.Sprintf("🗺 %s", p.Region))
	}
	if p.Population > 0 {
		lines = append(lines, fmt.Sprintf("👥 %s people", formatInt(p.Population)))
	}
	if p.ElevationM != nil {
		lines = append(lines, fmt.Sprintf("⛰ %d m elevation", *p.ElevationM))
	}
	caption := strings.Join(lines, "\n")
	if p.Fact != "" {
		caption += "\n\n" + p.Fact + "\n\n" + fmt.Sprintf("📖 %s · CC BY-SA 4.0", factHost(p.FactURL))
	}
	return caption
}

// roundToSigFigs rounds n to sig significant figures (e.g. 1512491, 2 ->
// 1500000) — the "about N people" figure in the missing-map fallback prompt.
func roundToSigFigs(n int64, sig int) int64 {
	if n <= 0 || sig <= 0 {
		return n
	}
	pow := int64(1)
	digits := 0
	for v := n; v > 0; v /= 10 {
		digits++
	}
	if digits <= sig {
		return n
	}
	for i := 0; i < digits-sig; i++ {
		pow *= 10
	}
	// round-half-up on the division.
	return (n + pow/2) / pow * pow
}

// fallbackPrompt is the still-answerable text question used when a city's map
// image hasn't been synced yet (vibe/design-cities-on-map.md §2's missing-map
// fallback): names the country (+ region when known) and the rounded
// population instead of showing a marker.
func fallbackPrompt(p itemPayload) string {
	region := ""
	if p.Region != "" {
		region = fmt.Sprintf(" (%s)", p.Region)
	}
	return fmt.Sprintf("🏙 Name the city in %s %s%s with about %s people.",
		p.Flag, p.CountryName, region, formatInt(roundToSigFigs(p.Population, 2)))
}

// parseCard maps a payload to the engine Card: the answer key is the city's
// own key (the answer is the CITY), Subject carries the whole map question,
// and Intro is the rich §2 caption.
func parseCard(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    []string{p.Key},
		Subject: reviewSubject,
		Intro:   introCaption(p),
	}, nil
}

// lookupTables holds the city-key-keyed label and accepted-spelling maps the
// engine's key-only ModeText hooks need (see the package doc): cityLabels maps
// a city key to its display name (CorrectAnswer), cityAccept to its accepted
// typed spellings (name + alt_names).
type lookupTables struct {
	cityLabels map[string]string   // key -> city display name
	cityAccept map[string][]string // key -> accepted city spellings
}

var (
	tablesOnce sync.Once
	tables     lookupTables
	tablesErr  error
)

// dedupe returns the non-empty values in order with duplicates removed.
func dedupe(values ...string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// loadLookupTables builds (once per process) the city-key-keyed tables
// Accept/Labels close over, from the committed seeds/cities.yaml.
func loadLookupTables() (lookupTables, error) {
	tablesOnce.Do(func() {
		sf, err := loadCitiesFile(citiesSeedPath())
		if err != nil {
			tablesErr = err
			return
		}
		t := lookupTables{
			cityLabels: make(map[string]string, len(sf.Cities)),
			cityAccept: make(map[string][]string, len(sf.Cities)),
		}
		for _, c := range sf.Cities {
			t.cityLabels[c.Key] = c.Name
			t.cityAccept[c.Key] = dedupe(append([]string{c.Name}, c.AltNames...)...)
		}
		tables = t
	})
	return tables, tablesErr
}

// Generator wraps the generic engine.Generator to add MediaPath support for
// the Garage-backed map image (see the package doc): BuildExercise/BuildIntro
// delegate to the inner generator, then attach the garage:// ref when the item
// has a synced map, else fall back to the §2 text form.
type Generator struct {
	inner     *engine.Generator
	mediaRoot string
}

var _ topics.Generator = (*Generator)(nil)

// New builds the cities Generator against MediaRootRef — the constructor
// cmd/bot wires (topics.Register(cities.New())).
func New() *Generator { return NewWithMediaRoot(MediaRootRef) }

// NewWithMediaRoot builds the cities Generator with an explicit media-root ref
// — used by tests so they can point at any ref (or "" to force the missing-map
// fallback path). Panics on a broken seeds/cities.yaml (a wiring-time error,
// consistent with engine.New's invalid-descriptor panic).
func NewWithMediaRoot(mediaRoot string) *Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("cities: build %s descriptor: %v", Kind, err))
	}
	accept := t.cityAccept
	d := engine.Descriptor{
		QuizKind: Kind,
		Topic:    cityTopic(),
		Parse:    parseCard,
		Labels:   t.cityLabels,
		// PromptSingle/PromptText are "%s": Card.Subject already carries the
		// fully-rendered question — the map-question text never varies, and
		// the missing-map fallback prompt is applied by this wrapper after the
		// fact, not selected by a template (flags precedent).
		PromptSingle: "%s",
		PromptText:   "%s",
		Distractors:  engine.DistractorPolicy{Max: maxDistractors},
		Accept: func(key string) []string {
			if names, ok := accept[key]; ok {
				return names
			}
			return []string{key}
		},
	}
	return &Generator{inner: engine.New(d), mediaRoot: mediaRoot}
}

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return g.inner.Kind() }

// mediaRefFor returns the garage:// ref for a payload's map image, or "" when
// no image has been synced for it.
func (g *Generator) mediaRefFor(p itemPayload) string {
	if g.mediaRoot == "" || p.MapImage == "" {
		return ""
	}
	return g.mediaRoot + "/" + p.MapImage
}

// BuildExercise implements topics.Generator: delegates to the inner engine
// Generator (the ModeText map question), then either attaches the map image's
// garage:// ref as MediaPath or — when the image hasn't been synced yet —
// clears MediaPath and swaps the prompt for the still-answerable missing-map
// text fallback (vibe/design-cities-on-map.md §2). No IO: presence was decided
// at seed time (itemPayload.MapImage).
func (g *Generator) BuildExercise(ctx context.Context, rng *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	ex, err := g.inner.BuildExercise(ctx, rng, req)
	if err != nil {
		return topics.Exercise{}, err
	}
	p, perr := parsePayload(req.Item.Payload)
	if perr != nil {
		return topics.Exercise{}, fmt.Errorf("%s: item %s: %w", Kind, req.Item.Key, perr)
	}
	if ref := g.mediaRefFor(p); ref != "" {
		ex.MediaPath = ref
	} else {
		ex.MediaPath = ""
		ex.Prompt = fallbackPrompt(p)
	}
	return ex, nil
}

// BuildIntro implements topics.Generator: delegates to the inner engine
// Generator for the rendered §2 caption, then attaches the map image's
// garage:// ref as MediaPath when synced (else the intro is text-only — the
// caption is already the content).
func (g *Generator) BuildIntro(ctx context.Context, item storage.Item) (topics.IntroCard, error) {
	card, err := g.inner.BuildIntro(ctx, item)
	if err != nil {
		return topics.IntroCard{}, err
	}
	if p, perr := parsePayload(item.Payload); perr == nil {
		if ref := g.mediaRefFor(p); ref != "" {
			card.MediaPath = ref
		}
	}
	return card, nil
}
