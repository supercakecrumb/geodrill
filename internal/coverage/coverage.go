// Package coverage bridges the language topics' ISO-639-3 deck codes to the
// GeoGuessr-coverage signal that lives on countries, for the per-user
// GeoGuessr-only filter (users.gg_only). It is used at ingest time by
// cmd/ingest's relevance pass to precompute items.gg_relevant for language
// items (country_id IS NULL) — country-linked items get their gg_relevant
// straight from countries.gg_coverage and never reach this package.
//
// The mapping (seeds/language_coverage.yaml) and the decision rule are kept
// here, in one small pure package, so both the ingest pass and its audit test
// share exactly one implementation of "is this language covered".
package coverage

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultSeedPath is seeds/language_coverage.yaml relative to the repo root
// (matching every other topic seed's -seeds default convention).
const DefaultSeedPath = "seeds/language_coverage.yaml"

// Mapping maps an ISO-639-3 deck code to the languages_spoken English-name
// spellings that count as "the same language".
type Mapping map[string][]string

// seedFile is the top-level shape of seeds/language_coverage.yaml.
type seedFile struct {
	Languages Mapping `yaml:"languages"`
}

// Load reads and parses the language-coverage mapping at path.
func Load(path string) (Mapping, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read language coverage %s: %w", path, err)
	}
	var sf seedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse language coverage %s: %w", path, err)
	}
	if len(sf.Languages) == 0 {
		return nil, fmt.Errorf("language coverage %s: empty mapping", path)
	}
	return sf.Languages, nil
}

// Decider decides whether a language item is GeoGuessr-relevant, given the
// code→name mapping and two name sets derived from the country facts: the
// languages spoken in covered countries, and all languages spoken anywhere.
// Everything is compared case-insensitively / trimmed.
type Decider struct {
	codeToNames map[string][]string // normalized code → normalized names
	covered     map[string]bool     // normalized covered names
	known       map[string]bool     // normalized names present in the fact data at all
}

// NewDecider builds a Decider. coveredNames are the languages_spoken values in
// gg_coverage countries; allNames are the languages_spoken values across every
// country (covered or not).
func NewDecider(m Mapping, coveredNames, allNames []string) *Decider {
	d := &Decider{
		codeToNames: make(map[string][]string, len(m)),
		covered:     make(map[string]bool, len(coveredNames)),
		known:       make(map[string]bool, len(allNames)),
	}
	for code, names := range m {
		norm := make([]string, 0, len(names))
		for _, n := range names {
			norm = append(norm, normalize(n))
		}
		d.codeToNames[normalize(code)] = norm
	}
	for _, n := range coveredNames {
		d.covered[normalize(n)] = true
	}
	for _, n := range allNames {
		d.known[normalize(n)] = true
	}
	return d
}

// Relevant decides a language item's gg_relevant from its language codes.
//
//   - covered:        at least one code maps to a language spoken in a covered
//     country → relevant.
//   - non-covered:    every code either is unmapped-but-known, or maps only to
//     names that DO appear in the fact data but never in a covered country →
//     not relevant (safe to hide).
//   - undeterminable: at least one code is unmapped, or maps only to names
//     absent from the fact data entirely → relevant is returned true and
//     undeterminable is true, so the caller keeps the item visible (never
//     silently hide a language we have no coverage data for) and can log it.
//
// An item with no codes at all is treated as undeterminable → kept.
func (d *Decider) Relevant(codes []string) (relevant, undeterminable bool) {
	if len(codes) == 0 {
		return true, true
	}
	anyKnownName := false
	for _, code := range codes {
		names, mapped := d.codeToNames[normalize(code)]
		if !mapped {
			// Unmapped deck code — coverage undeterminable for this item.
			return true, true
		}
		for _, n := range names {
			if d.covered[n] {
				return true, false
			}
			if d.known[n] {
				anyKnownName = true
			}
		}
	}
	if !anyKnownName {
		// None of the item's languages appear in the fact data at all —
		// coverage undeterminable, keep it visible.
		return true, true
	}
	// Languages are represented in the data but only in non-coverage
	// countries → genuinely not covered.
	return false, false
}

// normalize lower-cases and trims a name/code for case-insensitive matching.
func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
