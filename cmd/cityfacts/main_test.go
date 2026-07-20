package main

import "testing"

func TestTrimBlurb(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "already short passthrough",
			input: "Munich is the capital of Bavaria.",
			want:  "Munich is the capital of Bavaria.",
		},
		{
			name:  "trims to two sentences when a third is present",
			input: "Munich is the capital of Bavaria. It is the second most populous city in the federal states of southern Germany. Munich is a major hub for science and technology.",
			want:  "Munich is the capital of Bavaria. It is the second most populous city in the federal states of southern Germany.",
		},
		{
			name: "350-char cap falls back to one sentence on a sentence boundary",
			input: "This is the first sentence and it is long enough on its own to occupy a very large share of the full three hundred and fifty character budget available to this particular blurb, stretching out quite a bit further than a typical opening line would normally go so it uses most of it up nearly entirely by itself here now yes." +
				" This is the second sentence which would push the combined text well past the limit.",
			want: "This is the first sentence and it is long enough on its own to occupy a very large share of the full three hundred and fifty character budget available to this particular blurb, stretching out quite a bit further than a typical opening line would normally go so it uses most of it up nearly entirely by itself here now yes.",
		},
		{
			name:  "empty input stays empty",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace-only input trims to empty",
			input: "   \n\t  ",
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimBlurb(tc.input)
			if got != tc.want {
				t.Errorf("trimBlurb(%q) = %q, want %q (len got=%d want=%d)", tc.input, got, tc.want, len(got), len(tc.want))
			}
			if len(got) > 350 {
				t.Errorf("trimBlurb(%q) result exceeds 350 chars: len=%d", tc.input, len(got))
			}
		})
	}
}

func TestTrimBlurbAbbreviationsAndDecimalsDoNotSplitSentences(t *testing.T) {
	// Regression test: a naive split-on-every-period would previously cut
	// these off mid-abbreviation ("...in the U.") or mid-number ("...of
	// 12."), producing a garbled blurb. Wikipedia lead paragraphs are full
	// of both patterns (country abbreviations, population figures).
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single-letter acronym like U.S. does not end the sentence",
			input: "Example City is the largest city in the U.S. state of Freedonia and a major port.",
			want:  "Example City is the largest city in the U.S. state of Freedonia and a major port.",
		},
		{
			name:  "decimal population figure does not end the sentence",
			input: "Example City has an estimated population of 12.5 million residents in its metro area.",
			want:  "Example City has an estimated population of 12.5 million residents in its metro area.",
		},
		{
			name:  "St. abbreviation in a city name does not end the sentence",
			input: "St. Exampleburg is a coastal city known for its harbor and old town.",
			want:  "St. Exampleburg is a coastal city known for its harbor and old town.",
		},
		{
			name:  "abbreviation mid-sentence, real sentence boundary still detected",
			input: "It is located in the U.S. Located on a river, the city is a regional hub.",
			want:  "It is located in the U.S. Located on a river, the city is a regional hub.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimBlurb(tc.input)
			if got != tc.want {
				t.Errorf("trimBlurb(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTrimBlurbHardTruncatesSingleOverlongSentence(t *testing.T) {
	// A single "sentence" (no terminal punctuation) longer than 350 chars
	// must still be capped, on a word boundary, with no sentence boundary
	// available.
	word := "abcdefghij " // 11 chars incl. space
	var input string
	for i := 0; i < 40; i++ {
		input += word
	}
	got := trimBlurb(input)
	if len(got) > 350 {
		t.Fatalf("expected result <= 350 chars, got %d", len(got))
	}
	if got == "" {
		t.Fatalf("expected non-empty result")
	}
}

func TestMatchesCityName(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		city     string
		altNames []string
		want     bool
	}{
		{
			name:  "exact case-insensitive match on name",
			title: "Munich",
			city:  "munich",
			want:  true,
		},
		{
			name:     "matches an alt_name instead of the primary name",
			title:    "São Paulo",
			city:     "Sao Paulo",
			altNames: []string{"São Paulo"},
			want:     true,
		},
		{
			name:  "no match",
			title: "Munich (disambiguation)",
			city:  "Munich",
			want:  false,
		},
		{
			name:     "trims surrounding whitespace before comparing",
			title:    "  Xi'an  ",
			city:     "Xi'an",
			altNames: nil,
			want:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesCityName(tc.title, tc.city, tc.altNames)
			if got != tc.want {
				t.Errorf("matchesCityName(%q, %q, %v) = %v, want %v", tc.title, tc.city, tc.altNames, got, tc.want)
			}
		})
	}
}

func TestSelectGeoSearchMatch(t *testing.T) {
	results := []geoSearchResult{
		{Title: "Munich Airport", Dist: 5000},
		{Title: "Munich", Dist: 100},
		{Title: "Munich (disambiguation)", Dist: 4000},
	}

	title, ok := selectGeoSearchMatch(results, "Munich", nil)
	if !ok || title != "Munich" {
		t.Errorf("selectGeoSearchMatch() = (%q, %v), want (%q, true)", title, ok, "Munich")
	}

	// No exact match: falls back to the nearest result whose title contains
	// the name (results are assumed pre-sorted by distance).
	containsOnly := []geoSearchResult{
		{Title: "Greater Munich Area", Dist: 200},
		{Title: "Munich Suburb", Dist: 50},
	}
	title, ok = selectGeoSearchMatch(containsOnly, "Munich", nil)
	if !ok || title != "Greater Munich Area" {
		t.Errorf("selectGeoSearchMatch() nearest-contains = (%q, %v), want (%q, true)", title, ok, "Greater Munich Area")
	}

	noMatch := []geoSearchResult{{Title: "Somewhere Else", Dist: 10}}
	_, ok = selectGeoSearchMatch(noMatch, "Munich", nil)
	if ok {
		t.Errorf("selectGeoSearchMatch() expected no match, got a match")
	}
}

func TestIsDisambiguation(t *testing.T) {
	cases := []struct {
		pageType string
		want     bool
	}{
		{pageType: "disambiguation", want: true},
		{pageType: "standard", want: false},
		{pageType: "", want: false},
	}
	for _, tc := range cases {
		if got := isDisambiguation(tc.pageType); got != tc.want {
			t.Errorf("isDisambiguation(%q) = %v, want %v", tc.pageType, got, tc.want)
		}
	}
}

func TestExtractWikipediaTitle(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{url: "https://en.wikipedia.org/wiki/Munich", want: "Munich"},
		{url: "https://en.wikipedia.org/wiki/New_York_City", want: "New York City"},
		{url: "https://en.wikipedia.org/wiki/S%C3%A3o_Paulo", want: "São Paulo"},
	}
	for _, tc := range cases {
		got, err := extractWikipediaTitle(tc.url)
		if err != nil {
			t.Errorf("extractWikipediaTitle(%q) unexpected error: %v", tc.url, err)
			continue
		}
		if got != tc.want {
			t.Errorf("extractWikipediaTitle(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}

	if _, err := extractWikipediaTitle("https://en.wikipedia.org/notwiki/Munich"); err == nil {
		t.Errorf("extractWikipediaTitle: expected error for non /wiki/ path")
	}
}

func TestPickWikidataMatch(t *testing.T) {
	// A single candidate is trusted even without a name match (the common,
	// unambiguous case).
	single := []wdMatch{{Title: "Munich", QID: "Q1726"}}
	got, ok := pickWikidataMatch(single, "Munich", nil)
	if !ok || got.Title != "Munich" {
		t.Errorf("pickWikidataMatch(single) = (%+v, %v), want Munich/true", got, ok)
	}

	// Multiple candidates for one GeoNames ID (real Wikidata data quality
	// issue observed live: a stray P1566 value shared with an unrelated
	// article) must only be trusted when one candidate's title matches the
	// city name/alt_names.
	ambiguous := []wdMatch{
		{Title: "Eye and ENT Hospital of Fudan University", QID: "Q10931810"},
		{Title: "Shanghai", QID: "Q8686"},
	}
	got, ok = pickWikidataMatch(ambiguous, "Shanghai", nil)
	if !ok || got.Title != "Shanghai" {
		t.Errorf("pickWikidataMatch(ambiguous) = (%+v, %v), want Shanghai/true", got, ok)
	}

	// Multiple candidates, none matching the city name: unresolved, caller
	// should fall back to GeoSearch rather than guess.
	noneMatch := []wdMatch{
		{Title: "Something Else", QID: "Q1"},
		{Title: "Another Thing", QID: "Q2"},
	}
	_, ok = pickWikidataMatch(noneMatch, "Munich", nil)
	if ok {
		t.Errorf("pickWikidataMatch(noneMatch) expected unresolved, got a match")
	}
}

func TestExtractQID(t *testing.T) {
	got := extractQID("http://www.wikidata.org/entity/Q1726")
	if got != "Q1726" {
		t.Errorf("extractQID() = %q, want %q", got, "Q1726")
	}
	if got := extractQID(""); got != "" {
		t.Errorf("extractQID(\"\") = %q, want empty", got)
	}
}
