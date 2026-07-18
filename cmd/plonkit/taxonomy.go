package main

import (
	"sort"
	"time"
)

// knownCategories is plonkit.net's own tip-category enum, read directly out
// of its frontend bundle (assets/index-*.js): a Zod schema literal
//
//	["architecture","bollard","chevron/sign","coverage","guardrail",
//	 "important","landscape","language","license plates","moving info",
//	 "pole","roadline","vegetation"]
//
// found while investigating why a plain HTML fetch of a guide page returned
// no visible tip text (see guide.go's preloadedRe doc comment for how the
// content is actually served). This is the authoritative taxonomy the site
// itself enforces on tip authors, independent of which categories happen to
// appear in any particular scrape sample — see vibe/plonkit-topics.md.
var knownCategories = []string{
	"architecture", "bollard", "chevron/sign", "coverage", "guardrail",
	"important", "landscape", "language", "license plates", "moving info",
	"pole", "roadline", "vegetation",
}

// untaggedKey is the taxonomy bucket for tips with no "tags" array at all —
// in the sample scraped for this prototype, most tips fall here (see design
// doc: only ~43% of tips carry a formal category).
const untaggedKey = "<untagged>"

type categoryCount struct {
	Key   string `yaml:"key"`
	Count int    `yaml:"count"`
}

// taxonomyDoc is written to taxonomy.yaml: the discovered category
// distribution across every successfully scraped country in one run, cross-
// checked against knownCategories.
type taxonomyDoc struct {
	GeneratedAt     time.Time       `yaml:"generated_at"`
	SampleCountries []string        `yaml:"sample_countries"`
	KnownCategories []string        `yaml:"known_categories"`
	Categories      []categoryCount `yaml:"categories"`
	UnknownTags     []string        `yaml:"unknown_tags,omitempty"`
}

// tagCounter accumulates category counts (including untaggedKey) across
// every scraped country in one run.
type tagCounter struct {
	counts map[string]int
}

func newTagCounter() *tagCounter { return &tagCounter{counts: map[string]int{}} }

func (c *tagCounter) add(doc countryDoc) {
	for _, sec := range doc.Sections {
		for _, tip := range sec.Tips {
			if len(tip.Categories) == 0 {
				c.counts[untaggedKey]++
				continue
			}
			for _, cat := range tip.Categories {
				c.counts[cat]++
			}
		}
	}
}

// build renders the accumulated counts into a taxonomyDoc, sorted by count
// descending (ties broken alphabetically) and cross-checked against
// knownCategories: any discovered tag knownCategories doesn't list is
// surfaced in UnknownTags rather than silently absorbed, since that would
// mean the site's schema changed since 2026-07-18.
func (c *tagCounter) build(sampleISO []string) taxonomyDoc {
	known := make(map[string]bool, len(knownCategories))
	for _, k := range knownCategories {
		known[k] = true
	}

	cats := make([]categoryCount, 0, len(c.counts))
	var unknown []string
	for k, n := range c.counts {
		cats = append(cats, categoryCount{Key: k, Count: n})
		if k != untaggedKey && !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Slice(cats, func(i, j int) bool {
		if cats[i].Count != cats[j].Count {
			return cats[i].Count > cats[j].Count
		}
		return cats[i].Key < cats[j].Key
	})
	sort.Strings(unknown)

	return taxonomyDoc{
		GeneratedAt:     time.Now().UTC(),
		SampleCountries: sampleISO,
		KnownCategories: knownCategories,
		Categories:      cats,
		UnknownTags:     unknown,
	}
}
