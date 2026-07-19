// Package cities is the "which country is this city from?" quiz — the
// first slice of the cities topic (task brief overriding
// vibe/design-cities.md's older map/photo-recognition sketch: no maps, no
// terrain/position/elevation questions, those stay in the backlog). Unlike
// tld/capitals' sibling-direction-pair pattern, this is ONE direction only:
// the question is always a CITY (studied biggest -> smallest, via
// items.position = population rank) and the answer is always a COUNTRY,
// entered via inline-mode AUTOCOMPLETE (the topic's exercise mode is
// "autocomplete", which internal/study maps to a free-text ModeText
// exercise plus the "⌨️ Type your answer" country-suggestion button) over
// the EXISTING global country-suggestion index — internal/suggest already
// merges every country regardless of which topic's exercise is open (see
// internal/topics/tld's tld_to_country for the precedent of reusing that
// index for a different item shape). Cities themselves are never added to
// the suggestion index.
//
// # Why this package loads seeds/countries.yaml at wiring time
//
// The engine's ModeText hooks are key-only: Descriptor.Accept is
// func(key)[]string and CorrectAnswer is Descriptor.Label(key) — neither can
// see items.payload. A country's accepted spellings ("Germany", "DE", "DEU",
// aliases) and a nice display label are per-country reference data not
// derivable from a bare key, so this package builds an iso2 -> {label,
// accepted spellings} table once (loadLookupTables) from the committed
// seeds/countries.yaml — mirrors internal/topics/tld's tld_to_country
// direction exactly (same alias table, same load-once-at-New pattern).
package cities

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Kind is this package's sole quiz_kind.
const Kind = "city_to_country"

// maxDistractors caps the single-choice MCQ fallback. The topic is
// configured for "autocomplete" (ModeText), so this fallback is never
// exercised in production — but the engine's Descriptor.Validate requires a
// valid single-choice config (FixedOptions or Distractors.Max >= 1) on every
// descriptor, and a sampled country MCQ is the sensible thing for it to be
// (mirrors tld.maxDistractors / capitals.maxDistractors).
const maxDistractors = 3

// ErrMalformedPayload is returned (wrapped) by Parse when an item's payload
// isn't the itemPayload shape Seed writes.
var ErrMalformedPayload = errors.New("cities: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for a cities
// item. It caches everything the generator needs — the city's display name
// (prompt subject, intro) and the country's flag/name/iso codes (intro,
// answer key) — so BuildExercise/BuildIntro stay pure, no DB lookup needed
// at generation time.
type itemPayload struct {
	CityName    string `json:"city_name"`
	Flag        string `json:"flag"`
	CountryName string `json:"country_name"`
	ISOA2       string `json:"iso_a2"`
	ISOA3       string `json:"iso_a3"`
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
	if p.CityName == "" || p.ISOA2 == "" || p.CountryName == "" {
		return itemPayload{}, ErrMalformedPayload
	}
	return p, nil
}

// introText renders the introduction-card text: the city, its flag, and its
// country.
func introText(p itemPayload) string {
	return fmt.Sprintf("🏙 %s is a city in %s %s.", p.CityName, p.Flag, p.CountryName)
}

// parseCard maps a payload to the engine Card: the answer key is the
// country's iso2, the prompt subject is the bare city name (slotted into
// "🏙 Which country is %s in?").
func parseCard(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    []string{p.ISOA2},
		Subject: p.CityName,
		Intro:   introText(p),
	}, nil
}

// countryAliases adds a few common alternative spellings to the accepted
// country answers, on top of each country's canonical name + iso2 + iso3.
// The free-text matcher (quiz.TextMatcher, MaxEdits 2) already forgives
// typos, so this table only needs the aliases a user is genuinely likely to
// type that are too far for edit distance to reach — an exact copy of
// tld.countryAliases / capitals.countryAliases (the task brief's "reuse the
// pattern, a local copy is fine").
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
// engine's key-only ModeText hooks need (see the package doc; mirrors
// tld.lookupTables / capitals.lookupTables, minus the second (TLD/capital)
// side since this topic is one direction only).
type lookupTables struct {
	countryLabels map[string]string   // iso2 -> country display name (CorrectAnswer)
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
			tablesErr = fmt.Errorf("cities: %w", err)
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

// New builds the city->country Generator (the generic engine one, driven by
// this package's descriptor). The topic's exercise mode is "autocomplete":
// internal/study.modeFromString maps that to ModeText and
// internal/study.buildExerciseForItem turns the literal "autocomplete" mode
// string into Prompt.Autocomplete, so the country-suggestion button renders
// on top of the ordinary free-text exercise. New panics on a broken seed
// file (a wiring-time error, consistent with engine.New's invalid-descriptor
// panic).
func New() *engine.Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("cities: build %s descriptor: %v", Kind, err))
	}
	accept := t.countryAccept
	return engine.New(engine.Descriptor{
		QuizKind:     Kind,
		Topic:        cityTopic(),
		Parse:        parseCard,
		Labels:       t.countryLabels,
		PromptSingle: "🏙 Which country is %s in?",
		PromptText:   "🏙 Which country is %s in?",
		Distractors:  engine.DistractorPolicy{Max: maxDistractors},
		Accept: func(key string) []string {
			if names, ok := accept[key]; ok {
				return names
			}
			return []string{key}
		},
	})
}
