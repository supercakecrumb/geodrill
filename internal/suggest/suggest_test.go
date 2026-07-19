package suggest

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

func labels(suggestions []Suggestion) []string {
	out := make([]string, len(suggestions))
	for i, s := range suggestions {
		out[i] = s.Label
	}
	return out
}

func TestMatch_PrefixRanking(t *testing.T) {
	idx := New([]Entry{
		{Label: "China"},
		{Label: "Chad"},
		{Label: "Chile"},
	})

	got := labels(idx.Match("ch", 10))
	// All three are prefix matches (case-folded); shortest normalized
	// label first ("Chad", 4 runes), then alphabetically among the
	// length-5 tie ("Chile" < "China").
	want := []string{"Chad", "Chile", "China"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Match(%q) = %v, want %v", "ch", got, want)
	}
}

func TestMatch_CaseFold(t *testing.T) {
	idx := New([]Entry{{Label: "France"}})

	got := labels(idx.Match("FRA", 10))
	if len(got) != 1 || got[0] != "France" {
		t.Fatalf("expected a case-folded prefix match, got %v", got)
	}
}

func TestMatch_TypoTolerance(t *testing.T) {
	idx := New([]Entry{{Label: "France"}, {Label: "Germany"}})

	// "Farnce" is an adjacent-letter transposition of "France": under plain
	// (non-Damerau) Levenshtein that costs exactly 2 edits — the maxEdits
	// boundary this package mirrors from internal/study.matchTypedText.
	got := labels(idx.Match("Farnce", 10))
	if len(got) != 1 || got[0] != "France" {
		t.Fatalf("expected a typo-tolerant match on France, got %v", got)
	}
}

func TestMatch_TypoToleranceExceeded(t *testing.T) {
	idx := New([]Entry{{Label: "France"}})

	// Nowhere near a prefix or within 2 edits of "France".
	got := idx.Match("zzzzzzzz", 10)
	if len(got) != 0 {
		t.Fatalf("expected no match for a query far outside tolerance, got %v", got)
	}
}

func TestMatch_PrefixBeforeFuzzy(t *testing.T) {
	idx := New([]Entry{{Label: "China"}, {Label: "Chile"}})

	// query "chile" is an exact (trivially prefix) match for "Chile", and
	// exactly 2 substitutions away from "China" ("chi_l_e" vs "chi_n_a" —
	// positions 3 and 4 differ) — within maxEdits, so a fuzzy match too.
	// The prefix hit must rank first regardless of the fuzzy candidate's
	// distance.
	got := labels(idx.Match("chile", 10))
	want := []string{"Chile", "China"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Match(%q) = %v, want %v (prefix before fuzzy)", "chile", got, want)
	}
}

func TestMatch_Limit(t *testing.T) {
	idx := New([]Entry{{Label: "Chad"}, {Label: "Chile"}, {Label: "China"}})

	got := idx.Match("ch", 2)
	if len(got) != 2 {
		t.Fatalf("expected limit=2 to cap the result, got %d: %v", len(got), got)
	}
	if got[0].Label != "Chad" || got[1].Label != "Chile" {
		t.Fatalf("expected the two closest prefix matches, got %v", labels(got))
	}
}

func TestMatch_LimitZeroOrNegative(t *testing.T) {
	idx := New([]Entry{{Label: "France"}})
	if got := idx.Match("fra", 0); got != nil {
		t.Fatalf("expected nil for limit=0, got %v", got)
	}
	if got := idx.Match("fra", -1); got != nil {
		t.Fatalf("expected nil for limit=-1, got %v", got)
	}
}

func TestMatch_EmptyQueryReturnsDefaultList(t *testing.T) {
	idx := New([]Entry{{Label: "Albania"}, {Label: "Belgium"}, {Label: "Chad"}})

	got := labels(idx.Match("", 2))
	want := []string{"Albania", "Belgium"} // first N in construction order
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Match(\"\", 2) = %v, want %v", got, want)
	}
}

func TestMatch_NilIndexIsSafe(t *testing.T) {
	var idx *Index
	if got := idx.Match("anything", 10); got != nil {
		t.Fatalf("expected nil from a nil *Index, got %v", got)
	}
}

func TestNewFromCountries_MapsFields(t *testing.T) {
	countries := []storage.Country{
		{ID: uuid.New(), Name: "France", FlagEmoji: "🇫🇷", ISOA2: "FR"},
		{ID: uuid.New(), Name: "Germany", FlagEmoji: "🇩🇪", ISOA2: "DE"},
	}

	idx := NewFromCountries(countries)
	got := idx.Match("franc", 10)
	if len(got) != 1 {
		t.Fatalf("expected exactly one match, got %v", got)
	}
	want := Suggestion{Label: "France", Emoji: "🇫🇷", Key: "FR"}
	if got[0] != want {
		t.Fatalf("NewFromCountries mapping = %+v, want %+v", got[0], want)
	}
}

func TestNewFromCountriesAndCapitals_MergesBothSources(t *testing.T) {
	countries := []storage.Country{
		{ID: uuid.New(), Name: "Colombia", FlagEmoji: "🇨🇴", ISOA2: "CO"},
		{ID: uuid.New(), Name: "France", FlagEmoji: "🇫🇷", ISOA2: "FR"},
	}
	capitalEntries := []CapitalEntry{
		{CountryISO: "CO", Name: "Bogotá", FlagEmoji: "🇨🇴"},
		{CountryISO: "FR", Name: "Paris", FlagEmoji: "🇫🇷"},
	}

	idx := NewFromCountriesAndCapitals(countries, capitalEntries)

	// A country-name query still matches (the merge didn't drop the
	// country source).
	got := idx.Match("colom", 10)
	if len(got) != 1 || got[0] != (Suggestion{Label: "Colombia", Emoji: "🇨🇴", Key: "CO"}) {
		t.Fatalf("country match = %v, want Colombia/CO", got)
	}

	// A capital-name query matches the merged-in capital entry, keyed
	// distinctly from its country ("cap:" prefix) so it never collides with
	// the country's own iso2 key in the same index, and tagged
	// DomainCapital (not the country entries' DomainCountry).
	got = idx.Match("bogo", 10)
	if len(got) != 1 || got[0] != (Suggestion{Label: "Bogotá", Emoji: "🇨🇴", Key: "cap:CO", Domain: DomainCapital}) {
		t.Fatalf("capital match = %v, want Bogotá/cap:CO/DomainCapital", got)
	}
}

// ── Domain / MatchDomain / DomainForAnswer ──────────────────────────────

func testCountriesAndCapitalsIndex() *Index {
	countries := []storage.Country{
		{ID: uuid.New(), Name: "Australia", FlagEmoji: "🇦🇺", ISOA2: "AU"},
		{ID: uuid.New(), Name: "Singapore", FlagEmoji: "🇸🇬", ISOA2: "SG"},
		{ID: uuid.New(), Name: "France", FlagEmoji: "🇫🇷", ISOA2: "FR"},
	}
	capitalEntries := []CapitalEntry{
		{CountryISO: "AU", Name: "Canberra", FlagEmoji: "🇦🇺"},
		// Singapore is a city-state: its capital fact carries the same name
		// as the country itself, exercising DomainForAnswer's
		// country-first rule.
		{CountryISO: "SG", Name: "Singapore", FlagEmoji: "🇸🇬"},
		{CountryISO: "FR", Name: "Paris", FlagEmoji: "🇫🇷"},
	}
	return NewFromCountriesAndCapitals(countries, capitalEntries)
}

func TestDomainForAnswer_CountryNameResolvesCountry(t *testing.T) {
	idx := testCountriesAndCapitalsIndex()
	if got := idx.DomainForAnswer("Australia"); got != DomainCountry {
		t.Fatalf("DomainForAnswer(%q) = %v, want DomainCountry", "Australia", got)
	}
}

func TestDomainForAnswer_PureCapitalResolvesCapital(t *testing.T) {
	idx := testCountriesAndCapitalsIndex()
	if got := idx.DomainForAnswer("Canberra"); got != DomainCapital {
		t.Fatalf("DomainForAnswer(%q) = %v, want DomainCapital", "Canberra", got)
	}
}

func TestDomainForAnswer_CityStateIsCountryFirst(t *testing.T) {
	idx := testCountriesAndCapitalsIndex()
	// Singapore is both a country name and (per the seeded capital fact) a
	// capital name; country-first membership must resolve it to
	// DomainCountry, not DomainCapital.
	if got := idx.DomainForAnswer("Singapore"); got != DomainCountry {
		t.Fatalf("DomainForAnswer(%q) = %v, want DomainCountry (country-first)", "Singapore", got)
	}
}

func TestDomainForAnswer_UnknownDefaultsCountry(t *testing.T) {
	idx := testCountriesAndCapitalsIndex()
	if got := idx.DomainForAnswer("Atlantis"); got != DomainCountry {
		t.Fatalf("DomainForAnswer(%q) = %v, want DomainCountry (default)", "Atlantis", got)
	}
}

func TestDomainForAnswer_NilIndexIsSafe(t *testing.T) {
	var idx *Index
	if got := idx.DomainForAnswer("Australia"); got != DomainCountry {
		t.Fatalf("DomainForAnswer on nil Index = %v, want DomainCountry", got)
	}
}

func TestMatchDomain_ScopesToCountryOnly(t *testing.T) {
	idx := testCountriesAndCapitalsIndex()
	got := labels(idx.MatchDomain("a", DomainCountry, 10))
	for _, l := range got {
		if l == "Canberra" || l == "Paris" {
			t.Fatalf("MatchDomain(DomainCountry) leaked a capital-only entry: %v", got)
		}
	}
}

func TestMatchDomain_ScopesToCapitalOnly(t *testing.T) {
	idx := testCountriesAndCapitalsIndex()
	got := labels(idx.MatchDomain("ca", DomainCapital, 10))
	want := []string{"Canberra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchDomain(DomainCapital, %q) = %v, want %v", "ca", got, want)
	}
	// "Australia" (a country) must never surface from a capital-scoped
	// match even though it's a prefix match for itself.
	if leaked := idx.MatchDomain("austra", DomainCapital, 10); len(leaked) != 0 {
		t.Fatalf("MatchDomain(DomainCapital) leaked a country-only entry: %v", leaked)
	}
}

func TestNew_CopiesInputSlice(t *testing.T) {
	entries := []Entry{{Label: "France"}}
	idx := New(entries)
	entries[0].Label = "mutated"

	got := idx.Match("franc", 10)
	if len(got) != 1 || got[0].Label != "France" {
		t.Fatalf("expected New to copy its input, got %v", got)
	}
}
