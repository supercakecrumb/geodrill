// Package capitals is the country-capital quiz (vibe/adding-topics.md's
// capitals slot in the P3 backlog), built both directions as two SIBLING
// topics under a countries/capitals container so each direction is
// introduced and reviewed on its own FSRS track (knowing Bogotá -> Colombia
// doesn't imply instant recall the other way) — mirrors
// internal/topics/tld's tld<->country split exactly:
//
//   - country -> capital ("What's the capital of 🇨🇴 Colombia?"): the answer
//     is a CAPITAL CITY, entered via inline-mode AUTOCOMPLETE over capital
//     names (the topic's exercise mode is "autocomplete", which
//     internal/study maps to a free-text ModeText exercise plus the
//     "⌨️ Type your answer" suggestion button — internal/suggest's index is
//     extended with capital entries for this direction, see
//     internal/suggest/suggest.go).
//   - capital -> country ("🏛 Bogotá is the capital of which country?"): the
//     answer is a COUNTRY, entered the same way via the existing
//     country-suggestion entries already in the index (see tld's
//     tld_to_country for precedent).
//
// Both directions are "autocomplete"/ModeText descriptors — capital ->
// country isn't plain text like tld's country->tld side, because typing a
// full country name from scratch is a much bigger ask than typing a short
// TLD; autocomplete both ways is the better UX here (task brief).
//
// # Multi-capital countries
//
// A handful of countries have more than one listed capital
// (seeds/capitals.yaml's `capitals:` list, primary first — e.g. South
// Africa: Pretoria/Cape Town/Bloemfontein, Bolivia: Sucre/La Paz). This
// package's rule, applied uniformly:
//
//   - country -> capital: the displayed CorrectAnswer is always the PRIMARY
//     capital (capitals[0]), but Accept is generous — every listed capital
//     for that country grades correct (so typing "Cape Town" for South
//     Africa is accepted alongside "Pretoria").
//   - capital -> country: only the PRIMARY capital ever generates a
//     question. Secondary capitals (La Paz, Cape Town, The Hague, Cotonou,
//     Putrajaya, Dar es Salaam, Abidjan, Lobamba, ...) do NOT get their own
//     capital->country item. This is deliberately conservative rather than
//     trying to judge which secondary capitals are "unambiguous enough":
//     every one of them is itself a well-known city that could plausibly be
//     asked about on its own terms in a future topic, and skipping them here
//     keeps this topic's item set in 1:1 correspondence with country->capital
//     (mirrors tld's direction-parity invariant, checked by the integration
//     test) rather than needing a second, larger item count for one
//     direction only.
//
// Countries with no capital at all (seeds/capitals.yaml entries with a
// `note` but no `capitals` list — Antarctica, Bouvet Island, Western Sahara,
// Heard/McDonald Islands, Svalbard, French Southern Territories, US Minor
// Outlying Islands) get no item in either direction.
package capitals

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// The two quiz_kinds this package registers (one Generator each). They are
// siblings in the topic tree but distinct kinds so their Generators, and the
// users' FSRS progress, never share state.
const (
	KindCountryToCapital = "country_to_capital"
	KindCapitalToCountry = "capital_to_country"
)

// maxDistractors caps the single-choice MCQ fallback. Neither topic is
// configured for "single" (both directions are autocomplete/ModeText), so
// this fallback is never exercised in production — but the engine's
// Descriptor.Validate requires a valid single-choice config on every
// descriptor, and a sampled country/capital MCQ is the sensible thing for it
// to be (mirrors tld.maxDistractors).
const maxDistractors = 3

// ErrMalformedPayload is returned (wrapped) by the descriptors' Parse when an
// item's payload isn't the itemPayload shape Seed writes.
var ErrMalformedPayload = errors.New("capitals: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for a capitals
// item (both directions share it). Capital is always the PRIMARY capital
// (capitals[0] in seeds/capitals.yaml) — the single source of truth
// country_facts also stores (design mirrors tld's payload.tld / FactKeyTLD
// pairing); the accepted-spellings table for the OTHER listed capitals lives
// in lookupTables (loaded from the seed file directly), not in payload, so
// payload stays a plain per-item cache rather than duplicating the whole
// accept table on every item.
type itemPayload struct {
	Flag    string `json:"flag"`
	Name    string `json:"name"`
	ISOA2   string `json:"iso_a2"`
	ISOA3   string `json:"iso_a3"`
	Capital string `json:"capital"` // primary capital, e.g. "Bogotá"
	Note    string `json:"note,omitempty"`
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
	if p.Name == "" || p.ISOA2 == "" || p.Capital == "" {
		return itemPayload{}, ErrMalformedPayload
	}
	return p, nil
}

// introText renders the direction-agnostic teaching blurb (mirrors
// tld.introText): state the fact both ways regardless of which
// direction-topic it belongs to.
func introText(p itemPayload) string {
	text := fmt.Sprintf("%s %s's capital is %s.", p.Flag, p.Name, p.Capital)
	if p.Note != "" {
		text += " (" + p.Note + ")"
	}
	return text
}

// parseCardCountryToCapital maps a payload to the engine Card for the
// country->capital direction: the answer key is the country iso2, the
// prompt subject is the flag+name pair ("🇨🇴 Colombia", slotted into "What's
// the capital of %s?").
func parseCardCountryToCapital(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    []string{p.ISOA2},
		Subject: p.Flag + " " + p.Name,
		Intro:   introText(p),
	}, nil
}

// parseCardCapitalToCountry maps a payload to the engine Card for the
// capital->country direction: the answer key is still the country iso2 (so
// the country label/accepted-spellings tables are keyed uniformly across
// both directions), the prompt subject is the PRIMARY capital name alone
// (slotted into "🏛 %s is the capital of which country?").
func parseCardCapitalToCountry(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    []string{p.ISOA2},
		Subject: p.Capital,
		Intro:   introText(p),
	}, nil
}

// countryAliases adds a few common alternative spellings to the accepted
// answers for the capital->country direction, on top of each country's
// canonical name + iso2 + iso3 — the free-text matcher (quiz.TextMatcher,
// MaxEdits 2) already forgives typos, so this table only needs the aliases a
// user is genuinely likely to type that are too far for edit distance to
// reach (mirrors tld.countryAliases).
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

// capitalAliases adds a few common alternative spellings for the
// country->capital direction's accepted capital-name answers, beyond the
// listed capitals themselves — spellings a user is likely to type that
// aren't close enough (edit distance <= 2) to the canonical name(s).
var capitalAliases = map[string][]string{
	"US": {"Washington", "Washington DC", "Washington D.C."},
	"VA": {"Vatican"},
	"NL": {"Den Haag"}, // common alternative name for The Hague
	"ST": {"Sao Tome"}, // unaccented "São Tomé"
	"CI": {"Abidjan"},  // already primary/secondary via seed data, kept explicit
}

// lookupTables holds the per-direction iso2-keyed label and accepted-
// spelling maps the engine's key-only ModeText hooks need (see the package
// doc; mirrors tld.lookupTables).
type lookupTables struct {
	countryLabels map[string]string   // iso2 -> country display name (capital->country CorrectAnswer)
	countryAccept map[string][]string // iso2 -> accepted country spellings
	capitalLabels map[string]string   // iso2 -> primary capital name (country->capital CorrectAnswer)
	capitalAccept map[string][]string // iso2 -> accepted capital spellings (all listed capitals + aliases)
}

var (
	tablesOnce sync.Once
	tables     lookupTables
	tablesErr  error
)

// loadLookupTables builds (once per process) the iso2-keyed tables both
// descriptors' Accept/Labels close over, from the committed seed files.
func loadLookupTables() (lookupTables, error) {
	tablesOnce.Do(func() {
		countries, err := engine.LoadCountriesFile(countriesSeedPath())
		if err != nil {
			tablesErr = fmt.Errorf("capitals: %w", err)
			return
		}
		sf, err := loadCapitalsFile(capitalsSeedPath())
		if err != nil {
			tablesErr = err
			return
		}

		t := lookupTables{
			countryLabels: make(map[string]string, len(countries)),
			countryAccept: make(map[string][]string, len(countries)),
			capitalLabels: make(map[string]string, len(sf.Entries)),
			capitalAccept: make(map[string][]string, len(sf.Entries)),
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
		for _, e := range sf.Entries {
			if len(e.Capitals) == 0 {
				continue // no capital at all (Antarctica, Bouvet Island, ...)
			}
			t.capitalLabels[e.Country] = e.Capitals[0].Name
			var spellings []string
			for _, c := range e.Capitals {
				spellings = append(spellings, c.Name)
			}
			if a, ok := capitalAliases[e.Country]; ok {
				spellings = append(spellings, a...)
			}
			t.capitalAccept[e.Country] = spellings
		}
		tables = t
	})
	return tables, tablesErr
}

// NewCountryToCapital builds the country->capital Generator (the generic
// engine one, driven by this package's descriptor). The topic's exercise
// mode is "autocomplete": internal/study.modeFromString maps that to
// ModeText and internal/study.buildExerciseForItem turns the literal
// "autocomplete" mode string into Prompt.Autocomplete, so the
// capital-suggestion button renders on top of the ordinary free-text
// exercise. New panics on a broken seed file (a wiring-time error,
// consistent with engine.New's invalid-descriptor panic).
func NewCountryToCapital() *engine.Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("capitals: build %s descriptor: %v", KindCountryToCapital, err))
	}
	accept := t.capitalAccept
	return engine.New(engine.Descriptor{
		QuizKind:     KindCountryToCapital,
		Topic:        countryToCapitalTopic(),
		Parse:        parseCardCountryToCapital,
		Labels:       t.capitalLabels,
		PromptSingle: "What's the capital of %s?",
		PromptText:   "What's the capital of %s?",
		Distractors:  engine.DistractorPolicy{Max: maxDistractors},
		Accept: func(key string) []string {
			if names, ok := accept[key]; ok {
				return names
			}
			return []string{key}
		},
	})
}

// NewCapitalToCountry builds the capital->country Generator. Same
// "autocomplete"/ModeText shape as country->capital, but over the country
// suggestion source already in internal/suggest's index (see tld's
// tld_to_country for the precedent of reusing the country autocomplete
// entries for a different direction).
func NewCapitalToCountry() *engine.Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("capitals: build %s descriptor: %v", KindCapitalToCountry, err))
	}
	accept := t.countryAccept
	return engine.New(engine.Descriptor{
		QuizKind:     KindCapitalToCountry,
		Topic:        capitalToCountryTopic(),
		Parse:        parseCardCapitalToCountry,
		Labels:       t.countryLabels,
		PromptSingle: "🏛 %s is the capital of which country?",
		PromptText:   "🏛 %s is the capital of which country?",
		Distractors:  engine.DistractorPolicy{Max: maxDistractors},
		Accept: func(key string) []string {
			if names, ok := accept[key]; ok {
				return names
			}
			return []string{key}
		},
	})
}
