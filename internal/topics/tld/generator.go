// Package tld is the country-code top-level domain quiz (vibe/design-tlds.md),
// built both directions as two SIBLING topics under a countries/domains
// container so each direction is introduced and reviewed on its own FSRS
// track (knowing .de -> Germany doesn't imply instant recall the other way):
//
//   - tld -> country ("Which country uses the domain .de?"): the answer is a
//     COUNTRY, entered via inline-mode AUTOCOMPLETE (the topic's exercise
//     mode is "autocomplete", which internal/study maps to a free-text
//     ModeText exercise plus the "⌨️ Type your answer" country-suggestion
//     button) — not a tapped option list.
//   - country -> tld ("🇩🇪 Germany — which top-level domain?"): the answer is
//     the short TLD string, typed as plain free text (ModeText, no
//     autocomplete).
//
// The original in-flight version of this package predated the topic engine:
// it hand-wrote a Generator that switched on a topics.config "direction" key
// under one quiz_kind. That approach doesn't fit the engine, whose Generator
// is one-per-quiz_kind and reads only its Descriptor (never req.Topic.Config).
// So this rebuild is two engine.Descriptors under two distinct quiz_kinds —
// KindTLDToCountry and KindCountryToTLD — registered independently. Sibling
// topics, independent progress, no direction switch.
//
// # Why this package loads its seed files at wiring time
//
// The engine's ModeText hooks are key-only: Descriptor.Accept is
// func(key)[]string and CorrectAnswer is Descriptor.Label(key) — neither can
// see items.payload. A country's accepted spellings ("Germany", "DE", "DEU",
// aliases) and a nice display label are per-country reference data not
// derivable from a bare key, so this package builds iso2 -> {label, accepted
// spellings} tables once (loadLookupTables) from the committed seed files —
// seeds/countries.yaml for the country side, seeds/tlds.yaml for the TLD
// side. This mirrors how words hard-codes its languageNames label table; it's
// just loaded from the shared reference files rather than inlined, and it
// happens at New() time (a wiring-time panic on a broken file, consistent
// with engine.New panicking on an invalid descriptor), so generation itself
// stays database-free.
package tld

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// The two quiz_kinds this package registers (one Generator each). They are
// siblings in the topic tree but distinct kinds so their Generators, and the
// users' FSRS progress, never share state.
const (
	KindTLDToCountry = "tld_to_country"
	KindCountryToTLD = "country_to_tld"
)

// maxDistractors caps the single-choice MCQ fallback. Neither topic is
// configured for "single" (tld->country is autocomplete, country->tld is
// text), so this fallback is never exercised in production — but the engine's
// Descriptor.Validate requires a valid single-choice config (FixedOptions or
// Distractors.Max >= 1) on every descriptor, and a sampled country/TLD MCQ is
// the sensible thing for it to be.
const maxDistractors = 3

// ErrMalformedPayload is returned (wrapped) by the descriptors' Parse when an
// item's payload isn't the itemPayload shape Seed writes.
var ErrMalformedPayload = errors.New("tld: malformed item payload")

// itemPayload is the exact JSON shape stored in items.payload for a tld item
// (both directions share it). It caches everything the intro card needs and
// the answer keys both directions parse — the country's iso2 (the answer key
// for both directions and the item key) and the TLD (the prompt subject for
// the country side, the fact for the intro). The TLD value is also written to
// country_facts by Seed (the single source of truth); payload is a cache the
// integration test asserts never diverges from it.
type itemPayload struct {
	Flag  string `json:"flag"`
	Name  string `json:"name"`
	ISOA2 string `json:"iso_a2"`
	ISOA3 string `json:"iso_a3"`
	TLD   string `json:"tld"` // dot-prefixed, e.g. ".tv"
	Note  string `json:"note,omitempty"`
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
	if p.Name == "" || p.ISOA2 == "" || p.TLD == "" {
		return itemPayload{}, ErrMalformedPayload
	}
	return p, nil
}

// introText renders the direction-agnostic teaching blurb (design §6: state
// the fact both ways regardless of which direction-topic it belongs to).
func introText(p itemPayload) string {
	text := fmt.Sprintf("%s %s's country-code domain is %s.", p.Flag, p.Name, p.TLD)
	if p.Note != "" {
		text += " (" + quirkNote(p.Name, p.Note) + ")"
	}
	return text
}

// quirkNote strips a leading "<country name>; " prefix from note when present
// — most seeds/tlds.yaml notes follow that "Name; quirk" shape, but a few
// (e.g. GB's) are standalone sentences and are returned unchanged.
func quirkNote(name, note string) string {
	prefix := name + "; "
	if strings.HasPrefix(note, prefix) {
		return note[len(prefix):]
	}
	return note
}

// parseCardTLDToCountry maps a payload to the engine Card for the tld->country
// direction: the answer key is the country iso2, the prompt subject is the TLD
// (".de", slotted into "Which country uses the domain %s?").
func parseCardTLDToCountry(raw []byte) (engine.Card, error) {
	p, err := parsePayload(raw)
	if err != nil {
		return engine.Card{}, err
	}
	return engine.Card{
		Keys:    []string{p.ISOA2},
		Subject: p.TLD,
		Intro:   introText(p),
	}, nil
}

// parseCardCountryToTLD maps a payload to the engine Card for the
// country->tld direction: the answer key is still the country iso2 (so the
// TLD label/accepted-spellings tables are keyed uniformly across both
// directions), the prompt subject is the flag+name pair ("🇩🇪 Germany",
// slotted into "%s — which top-level domain?").
func parseCardCountryToTLD(raw []byte) (engine.Card, error) {
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

// countryAliases adds a few common alternative spellings to the accepted
// answers for the tld->country direction, on top of each country's canonical
// name + iso2 + iso3. The free-text matcher (quiz.TextMatcher, MaxEdits 2)
// already forgives typos, so this table only needs the aliases a user is
// genuinely likely to type that are too far for edit distance to reach.
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

// lookupTables holds the per-direction iso2-keyed label and accepted-spelling
// maps the engine's key-only ModeText hooks need (see the package doc).
type lookupTables struct {
	countryLabels map[string]string   // iso2 -> country display name (tld->country CorrectAnswer)
	countryAccept map[string][]string // iso2 -> accepted country spellings
	tldLabels     map[string]string   // iso2 -> ".de" (country->tld CorrectAnswer)
	tldAccept     map[string][]string // iso2 -> accepted TLD spellings (".de", "de")
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
			tablesErr = fmt.Errorf("tld: %w", err)
			return
		}
		sf, err := loadTLDsFile(tldsSeedPath())
		if err != nil {
			tablesErr = err
			return
		}

		t := lookupTables{
			countryLabels: make(map[string]string, len(countries)),
			countryAccept: make(map[string][]string, len(countries)),
			tldLabels:     make(map[string]string, len(sf.TLDs)),
			tldAccept:     make(map[string][]string, len(sf.TLDs)),
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
		for _, e := range sf.TLDs {
			t.tldLabels[e.Country] = e.TLD
			bare := strings.TrimPrefix(e.TLD, ".")
			t.tldAccept[e.Country] = []string{e.TLD, bare}
		}
		tables = t
	})
	return tables, tablesErr
}

// NewTLDToCountry builds the tld->country Generator (the generic engine one,
// driven by this package's descriptor). The topic's exercise mode is
// "autocomplete": internal/study.modeFromString maps that to ModeText and
// internal/study.buildExerciseForItem turns the literal "autocomplete" mode
// string into Prompt.Autocomplete, so the country-suggestion button renders
// on top of the ordinary free-text exercise. New panics on a broken seed file
// (a wiring-time error, consistent with engine.New's invalid-descriptor
// panic).
func NewTLDToCountry() *engine.Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("tld: build %s descriptor: %v", KindTLDToCountry, err))
	}
	accept := t.countryAccept
	return engine.New(engine.Descriptor{
		QuizKind:     KindTLDToCountry,
		Topic:        tldToCountryTopic(),
		Parse:        parseCardTLDToCountry,
		Labels:       t.countryLabels,
		PromptSingle: "Which country uses the domain %s?",
		PromptText:   "Which country uses the domain %s?",
		Distractors:  engine.DistractorPolicy{Max: maxDistractors},
		Accept: func(key string) []string {
			if names, ok := accept[key]; ok {
				return names
			}
			return []string{key}
		},
	})
}

// NewCountryToTLD builds the country->tld Generator. The topic's exercise mode
// is plain "text" (no autocomplete): the answer is a short string the user
// types directly. Accepted spellings are the TLD with and without its leading
// dot; case-insensitivity is quiz.TextMatcher.Normalize's job.
func NewCountryToTLD() *engine.Generator {
	t, err := loadLookupTables()
	if err != nil {
		panic(fmt.Sprintf("tld: build %s descriptor: %v", KindCountryToTLD, err))
	}
	accept := t.tldAccept
	return engine.New(engine.Descriptor{
		QuizKind:     KindCountryToTLD,
		Topic:        countryToTLDTopic(),
		Parse:        parseCardCountryToTLD,
		Labels:       t.tldLabels,
		PromptSingle: "%s — which top-level domain?",
		PromptText:   "%s — which top-level domain?",
		Distractors:  engine.DistractorPolicy{Max: maxDistractors},
		Accept: func(key string) []string {
			if spellings, ok := accept[key]; ok {
				return spellings
			}
			return []string{key}
		},
	})
}
