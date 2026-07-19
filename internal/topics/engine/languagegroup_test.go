package engine

import "testing"

// TestLanguageGroup_KnownDecks pins the group assignment for a representative
// language from each seeds/decks.yaml deck — the taxonomy specialchars and
// words both reuse for "close distractors" — so an accidental edit to
// languageGroups is caught immediately rather than surfacing as a subtly
// wrong-family distractor downstream.
func TestLanguageGroup_KnownDecks(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"rus", "slavic-cyrillic"},
		{"ukr", "slavic-cyrillic"},
		{"bul", "slavic-cyrillic"},
		{"srp", "slavic-cyrillic"},
		{"mkd", "slavic-cyrillic"},
		{"spa", "romance"},
		{"por", "romance"},
		{"ita", "romance"},
		{"fra", "romance"},
		{"cat", "romance"},
		{"ron", "romance"},
		{"pol", "slavic-latin"},
		{"ces", "slavic-latin"},
		{"slk", "slavic-latin"},
		{"hrv", "slavic-latin"},
		{"slv", "slavic-latin"},
		{"swe", "nordic"},
		{"nor", "nordic"},
		{"dan", "nordic"},
		{"isl", "nordic"},
		{"fin", "nordic"},
		{"cmn", "cjk"},
		{"jpn", "cjk"},
		{"kor", "cjk"},
		{"vie", "se-asia"},
		{"tha", "se-asia"},
		{"msa", "malay-indonesian"},
		{"hin", "indian-scripts"},
	}
	for _, c := range cases {
		if got := LanguageGroup(c.code); got != c.want {
			t.Errorf("LanguageGroup(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}

// TestLanguageGroup_IndOverlapResolvesToSeAsia pins the documented resolution
// of seeds/decks.yaml's one overlapping code ("ind" appears in both se-asia
// and malay-indonesian): it must resolve to exactly one group, deterministically.
func TestLanguageGroup_IndOverlapResolvesToSeAsia(t *testing.T) {
	if got := LanguageGroup("ind"); got != "se-asia" {
		t.Fatalf(`LanguageGroup("ind") = %q, want "se-asia" (documented overlap resolution)`, got)
	}
}

// TestLanguageGroup_UnknownCodeFallsBackToItself guards the singleton-group
// fallback: an unmapped code must not collapse to "" (which would make every
// unmapped language falsely same-group with every other unmapped language).
func TestLanguageGroup_UnknownCodeFallsBackToItself(t *testing.T) {
	if got := LanguageGroup("xyz"); got != "xyz" {
		t.Fatalf(`LanguageGroup("xyz") = %q, want "xyz" (bare-code fallback)`, got)
	}
	if LanguageGroup("xyz") == LanguageGroup("abc") {
		t.Fatalf("two different unmapped codes must not collapse to the same fallback group")
	}
}
