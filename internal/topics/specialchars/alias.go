package specialchars

import (
	"sort"
	"strings"
)

// Language is one language this topic can ask about, exported for callers
// (cmd/bot) that build the inline-autocomplete suggestion index and need the
// language list without importing the private alias table.
type Language struct {
	Code string // ISO 639-3 code, e.g. "spa"
	Name string // English display name, e.g. "Spanish"
}

// Languages returns every language in the alias table as {Code, Name} pairs,
// sorted by Name for a deterministic order (so the built suggestion index and
// its tests don't depend on Go's randomized map iteration). Read-only: it
// does not expose or mutate the alias table itself.
func Languages() []Language {
	out := make([]Language, 0, len(languageAliases))
	for code, a := range languageAliases {
		out = append(out, Language{Code: code, Name: a.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// languageAlias is one entry of the internal language-name table used for
// MCQ labels, set labels, intro-card text, and ModeText's accepted
// spellings. Covers the languages referenced by seeds/special_chars.yaml
// (a subset of the full geodrill deck list, seeds/decks.yaml) — extend this
// table alongside special_chars.yaml if a new language's char is added.
// Native is best-effort: included only where the spelling is unambiguous
// and well documented; when in doubt, English name + ISO code alone are
// still always in Accept (see acceptSpellings).
type languageAlias struct {
	Name   string   // canonical English display name, e.g. "Norwegian"
	Native []string // native-script/native-spelling aliases, e.g. "norsk"
}

var languageAliases = map[string]languageAlias{
	// Latin script
	"pol": {Name: "Polish", Native: []string{"polski"}},
	"ron": {Name: "Romanian", Native: []string{"română", "romana"}},
	"por": {Name: "Portuguese", Native: []string{"português", "portugues"}},
	"cat": {Name: "Catalan", Native: []string{"català", "catala"}},
	"isl": {Name: "Icelandic", Native: []string{"íslenska", "islenska"}},
	"ces": {Name: "Czech", Native: []string{"čeština", "cestina"}},
	"spa": {Name: "Spanish", Native: []string{"español", "espanol"}},
	"nor": {Name: "Norwegian", Native: []string{"norsk"}},
	"dan": {Name: "Danish", Native: []string{"dansk"}},
	"swe": {Name: "Swedish", Native: []string{"svenska"}},
	"fin": {Name: "Finnish", Native: []string{"suomi"}},
	"fra": {Name: "French", Native: []string{"français", "francais"}},
	"slk": {Name: "Slovak", Native: []string{"slovenčina", "slovencina"}},
	"hrv": {Name: "Croatian", Native: []string{"hrvatski"}},
	"slv": {Name: "Slovenian", Native: []string{"slovenščina", "slovenscina"}},
	"vie": {Name: "Vietnamese", Native: []string{"tiếng việt", "tieng viet"}},

	// Cyrillic script
	"rus": {Name: "Russian", Native: []string{"русский", "russkiy"}},
	"ukr": {Name: "Ukrainian", Native: []string{"українська", "ukrainska"}},
	"srp": {Name: "Serbian", Native: []string{"srpski"}},
	"mkd": {Name: "Macedonian", Native: []string{"македонски", "makedonski"}},
}

// languageLabel returns the display label for an ISO 639-3 code: the
// English name from languageAliases, or the uppercased code itself as a
// defensive fallback for a code the table doesn't (yet) cover.
func languageLabel(code string) string {
	if a, ok := languageAliases[code]; ok {
		return a.Name
	}
	return strings.ToUpper(code)
}

// acceptSpellings returns the ModeText accepted-spellings list for code:
// English name + ISO code at minimum (per the task brief), plus native
// aliases where the table has them. Order is stable given a fixed code
// (no randomness), so it can be asserted on directly in tests.
func acceptSpellings(code string) []string {
	out := make([]string, 0, 4)
	if a, ok := languageAliases[code]; ok {
		out = append(out, a.Name)
	} else {
		out = append(out, strings.ToUpper(code))
	}
	out = append(out, code)
	if a, ok := languageAliases[code]; ok {
		out = append(out, a.Native...)
	}
	return out
}
