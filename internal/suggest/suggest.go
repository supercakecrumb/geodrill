// Package suggest is an in-memory, typo-tolerant suggestion index for
// Telegram inline-mode autocomplete answers
// (vibe/spike-autocomplete-inline.md): given a short, possibly-misspelled
// query typed after tapping a topic's "⌨️ Type your answer" prefill button,
// Match ranks the closest candidates out of a fixed set loaded once at
// startup.
//
// The index is built from generic Entry{Label, Emoji, Key} values rather
// than any one domain type, so a second suggestion source can contribute its
// own Entries into the same Index (see NewFromCountries for the first
// source — countries — and NewFromCountriesAndCapitals for a second —
// capital cities — merged into one global Index, which is what the
// single-handler OnQuery design needs: it answers from one index and can't
// know which exercise is currently open, so per-source Indexes held side by
// side wouldn't help it pick the right one).
//
// Ranking reuses two of engram's exported quiz-package primitives —
// quiz.Normalize (Unicode casefold + trim + internal-space collapse) and
// quiz.Levenshtein (edit distance) — the same building blocks
// internal/study's free-text grading (quiz.TextMatcher) is built on, so
// autocomplete's notion of "close enough" matches grading's. It does NOT
// reuse quiz.TextMatcher itself: TextMatcher answers one yes/no question
// ("does typed match ANY of these accepted spellings within MaxEdits?") for
// grading a single already-open exercise; Match's job — rank N candidates
// out of a few hundred by closeness to a partial, per-keystroke query — is a
// different shape of problem that TextMatcher's API doesn't fit. Match's own
// maxEdits constant (2) mirrors internal/study.matchTypedText's MaxEdits so
// the two features feel equally forgiving of typos.
package suggest

import (
	"sort"
	"strings"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// maxEdits is the Levenshtein tolerance for Match's typo-tolerant ranking
// tier, matching internal/study.matchTypedText's MaxEdits (architecture
// §1.6 step 6) for a consistent "how forgiving of typos" feel between
// grading and autocomplete.
const maxEdits = 2

// Domain is the answer-kind an Entry belongs to — the scoping unit for
// MatchDomain/DomainForAnswer (kanban card "Autocomplete must be scoped to
// the question's answer domain"). There are only two autocomplete answer
// domains in the whole app today: city→country, capital→country,
// tld→country, and flags all answer a COUNTRY; only country→capital answers
// a CAPITAL (country→tld is plain text, no autocomplete at all) — so these
// two values are the complete set.
type Domain int8

const (
	// DomainCountry is the default/zero Domain: every country Entry
	// (countryEntries) is tagged with this, and it's DomainForAnswer's
	// fallback for an answer that matches neither a known country nor a
	// known capital.
	DomainCountry Domain = iota
	// DomainCapital tags capital-city entries (NewFromCountriesAndCapitals's
	// merged-in capitals).
	DomainCapital
)

// Entry is one suggestion candidate: a display label, an optional emoji
// prefix (e.g. a flag), a stable key, and the Domain it belongs to. Key is
// carried through to Suggestion unused by ranking itself — it exists so a
// caller can correlate a suggestion back to its source row (e.g. an ISO
// alpha-2 code) if it ever needs to, without Match itself caring what it
// means.
type Entry struct {
	Label  string
	Emoji  string
	Key    string
	Domain Domain
}

// Suggestion is one ranked Match/MatchDomain result. Its shape mirrors Entry
// exactly (same fields, same order) — kept as a distinct type anyway so a
// caller (internal/telegram) depends on "what Match returns" rather than
// "what an Index happens to be built from", which stays free to grow
// index-internal fields later without changing Match's contract.
type Suggestion struct {
	Label  string
	Emoji  string
	Key    string
	Domain Domain
}

// Index is an in-memory suggestion source built from a fixed slice of Entry
// values (New). No database access happens in this package at construction
// or match time — callers fetch rows themselves and hand over plain data —
// which is what makes both New and Match unit-testable with hand-written
// literals instead of a live store.
type Index struct {
	entries []Entry
}

// New builds an Index over entries. The slice is copied, so the caller's
// own slice can be mutated or discarded afterward without affecting the
// Index.
func New(entries []Entry) *Index {
	cp := make([]Entry, len(entries))
	copy(cp, entries)
	return &Index{entries: cp}
}

// NewFromCountries builds an Index from countries (name + flag emoji + ISO
// alpha-2 as Key) — the first suggestion source (see the package doc for
// how a second, e.g. cities, would plug in the same way via New).
func NewFromCountries(countries []storage.Country) *Index {
	return New(countryEntries(countries))
}

// CapitalEntry is one country's primary capital city, adapted by the caller
// (cmd/bot/main.go) from whatever it loaded (a seeded country_facts row via
// internal/topics/capitals.FactKeyCapital, or seeds/capitals.yaml directly)
// into this shape — kept free of any capitals-topic-specific import so this
// package stays generic (mirrors how NewFromCountries only depends on
// storage.Country, not a topic package).
type CapitalEntry struct {
	CountryISO string // iso_a2, used to build Key ("cap:" + CountryISO)
	Name       string // capital city name, e.g. "Bogotá"
	FlagEmoji  string // the country's flag, e.g. "🇨🇴"
}

// NewFromCountriesAndCapitals builds ONE merged Index covering both
// suggestion sources the countries/capitals quiz needs: every country (as
// NewFromCountries) plus one additional entry per capital (label = capital
// name, emoji = the country's flag, key = "cap:<iso2>" so a capital entry
// never collides with its own country's plain-iso2 key in the same index).
//
// This is deliberately ONE index, not two side-by-side ones: the inline
// OnQuery handler answers from a single global index and cannot know which
// exercise (country->capital or capital->country) is currently open for the
// querying user (vibe/spike-autocomplete-inline.md's design — grading
// happens against whatever exercise is open, not the suggestion source), so
// a merged index is the only shape that serves both directions from one
// handler. The extra suggestions from the "other" source are harmless noise
// for whichever direction isn't asking for them — e.g. typing "Bog" while a
// capital->country exercise is open still surfaces "🇨🇴 Bogotá" alongside
// any country whose name happens to start similarly, and the open
// exercise's own Accept/CorrectAnswer (not the suggestion list) is what
// actually grades the typed answer.
func NewFromCountriesAndCapitals(countries []storage.Country, capitals []CapitalEntry) *Index {
	entries := countryEntries(countries)
	for _, c := range capitals {
		entries = append(entries, Entry{Label: c.Name, Emoji: c.FlagEmoji, Key: "cap:" + c.CountryISO, Domain: DomainCapital})
	}
	return New(entries)
}

func countryEntries(countries []storage.Country) []Entry {
	entries := make([]Entry, len(countries))
	for i, c := range countries {
		entries[i] = Entry{Label: c.Name, Emoji: c.FlagEmoji, Key: c.ISOA2, Domain: DomainCountry}
	}
	return entries
}

// Match ranks Index's entries against query and returns at most limit
// Suggestions: case-folded prefix matches first (shortest normalized label
// first, then alphabetically — so e.g. "Chad" outranks "China" for the
// query "ch", both being prefix matches but "Chad" the closer one), then
// typo-tolerant matches (whole-label Levenshtein distance <= maxEdits from
// the whole query, ordered by distance then alphabetically). An entry is
// never counted in both tiers.
//
// query == "" returns the first limit entries in Index's own construction
// order rather than erroring or returning nothing — matching the spike's
// guidance on handling an inline query's empty Query.Text gracefully
// (vibe/spike-autocomplete-inline.md §3). Feeding NewFromCountries a
// storage.Store.ListCountries result (already alphabetical by name, per
// that method's own doc) means this "default list" comes out alphabetical
// for the countries source without Match needing its own opinion on
// ordering.
//
// limit <= 0 returns nil. A nil Index (e.g. an unwired *suggest.Index used
// directly rather than through the nil-safe Suggester gate in
// internal/telegram) also returns nil rather than panicking.
func (idx *Index) Match(query string, limit int) []Suggestion {
	if limit <= 0 {
		return nil
	}
	if idx == nil {
		return nil
	}
	return matchEntries(idx.entries, query, limit)
}

// MatchDomain is Match scoped to entries tagged with domain — e.g. only
// DomainCountry entries when handleQuery's open exercise expects a country
// answer, only DomainCapital entries for a capital answer (see
// DomainForAnswer) — using the identical prefix-then-fuzzy ranking Match
// itself uses, just over the domain-filtered subset. Same nil-safety and
// limit<=0 contract as Match.
func (idx *Index) MatchDomain(query string, domain Domain, limit int) []Suggestion {
	if limit <= 0 {
		return nil
	}
	if idx == nil {
		return nil
	}

	var filtered []Entry
	for _, e := range idx.entries {
		if e.Domain == domain {
			filtered = append(filtered, e)
		}
	}
	return matchEntries(filtered, query, limit)
}

// DomainForAnswer resolves which Domain an open exercise's CorrectAnswer
// belongs to, so a caller (internal/telegram's handleQuery) can pass the
// right Domain to MatchDomain instead of always merging both suggestion
// sources. Membership is COUNTRY-FIRST: idx's DomainCountry entries are
// checked before its DomainCapital entries, so a city-state whose capital
// shares its country's name (Singapore, Monaco) resolves to DomainCountry —
// only a name that is genuinely a capital-and-nothing-else (Canberra,
// Ottawa) resolves to DomainCapital. An unrecognized answer, an empty
// string, or a nil Index all default to DomainCountry, the same default a
// caller falls back to when no exercise is open at all. Comparison is
// casefolded via quiz.Normalize, consistent with Match's own notion of "the
// same label".
func (idx *Index) DomainForAnswer(correct string) Domain {
	if idx == nil {
		return DomainCountry
	}
	norm := quiz.Normalize(correct)
	if norm == "" {
		return DomainCountry
	}

	for _, e := range idx.entries {
		if e.Domain == DomainCountry && quiz.Normalize(e.Label) == norm {
			return DomainCountry
		}
	}
	for _, e := range idx.entries {
		if e.Domain == DomainCapital && quiz.Normalize(e.Label) == norm {
			return DomainCapital
		}
	}
	return DomainCountry
}

// matchEntries is Match/MatchDomain's shared ranking core: case-folded
// prefix matches first (shortest normalized label first, then
// alphabetically), then typo-tolerant matches (whole-label Levenshtein
// distance <= maxEdits from the whole query, ordered by distance then
// alphabetically) — see Match's own doc comment for the full ranking
// rationale, which applies unchanged here over whatever subset of entries
// the caller hands in.
func matchEntries(entries []Entry, query string, limit int) []Suggestion {
	q := quiz.Normalize(query)
	if q == "" {
		return toSuggestions(firstN(entries, limit))
	}

	var prefix, fuzzy []rankedEntry
	for _, e := range entries {
		norm := quiz.Normalize(e.Label)
		if norm == "" {
			continue
		}
		if strings.HasPrefix(norm, q) {
			prefix = append(prefix, rankedEntry{entry: e, norm: norm, score: len(norm)})
			continue
		}
		if dist := quiz.Levenshtein(norm, q); dist <= maxEdits {
			fuzzy = append(fuzzy, rankedEntry{entry: e, norm: norm, score: dist})
		}
	}

	sortRanked(prefix)
	sortRanked(fuzzy)

	out := make([]Suggestion, 0, limit)
	for _, r := range prefix {
		if len(out) >= limit {
			return out
		}
		out = append(out, Suggestion(r.entry))
	}
	for _, r := range fuzzy {
		if len(out) >= limit {
			return out
		}
		out = append(out, Suggestion(r.entry))
	}
	return out
}

// rankedEntry is one candidate mid-ranking: its source Entry, precomputed
// normalized label (avoids re-normalizing inside the sort comparator), and
// a bucket-specific score where "closer is smaller" for both buckets
// (prefix: normalized label length; fuzzy: edit distance) — letting a
// single ascending sort serve either bucket.
type rankedEntry struct {
	entry Entry
	norm  string
	score int
}

// sortRanked sorts by score ascending, then by normalized label
// alphabetically, in place.
func sortRanked(entries []rankedEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score < entries[j].score
		}
		return entries[i].norm < entries[j].norm
	})
}

func toSuggestions(entries []Entry) []Suggestion {
	out := make([]Suggestion, len(entries))
	for i, e := range entries {
		out[i] = Suggestion(e)
	}
	return out
}

// firstN returns the first n entries of entries (or all of them, if fewer
// than n exist).
func firstN(entries []Entry, n int) []Entry {
	if n >= len(entries) {
		return entries
	}
	return entries[:n]
}
