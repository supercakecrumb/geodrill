package engine

// languageGroups mirrors seeds/decks.yaml's guess-the-language deck taxonomy
// (internal/topics/guesslang/seed.go seeds these same decks into the game
// zone's language_id topic tree): the language-family groupings a "close
// distractors" topic (specialchars, words) reuses instead of grouping by
// script alone. Script alone is too coarse: it still lets a Latin-script
// question draw a distractor from a completely unrelated language family
// (e.g. a Polish item offering an Icelandic option — both "latin" script,
// nothing else in common), since romance, nordic, slavic-latin, and se-asia
// all share the Latin script.
//
// This is a static copy of the deck->language mapping, not a decks.yaml
// loader: Card.Group is computed by each topic's Parse on every exercise
// build (a hot path), so paying a YAML read + parse there on every call
// would be wasteful. Keeping exactly one static table here — rather than one
// per topic — is the single copy of the mapping; update it here, and only
// here, if seeds/decks.yaml's deck membership ever changes.
//
// seeds/decks.yaml lists "ind" (Indonesian) under both "se-asia" and
// "malay-indonesian" — this table resolves that overlap to "se-asia" (the
// deck it's listed under first in decks.yaml), since a language can only
// belong to one group here.
var languageGroups = map[string]string{
	// romance
	"spa": "romance", "por": "romance", "ita": "romance", "fra": "romance", "cat": "romance", "ron": "romance",
	// cjk
	"cmn": "cjk", "jpn": "cjk", "kor": "cjk",
	// slavic-latin
	"pol": "slavic-latin", "ces": "slavic-latin", "slk": "slavic-latin", "hrv": "slavic-latin", "slv": "slavic-latin",
	// slavic-cyrillic
	"rus": "slavic-cyrillic", "ukr": "slavic-cyrillic", "bul": "slavic-cyrillic", "srp": "slavic-cyrillic", "mkd": "slavic-cyrillic",
	// nordic
	"swe": "nordic", "nor": "nordic", "dan": "nordic", "isl": "nordic", "fin": "nordic",
	// se-asia (claims "ind" over malay-indonesian, per the doc comment above)
	"tha": "se-asia", "khm": "se-asia", "lao": "se-asia", "mya": "se-asia", "vie": "se-asia", "ind": "se-asia",
	// malay-indonesian ("ind" resolved to se-asia above, so only "msa" here)
	"msa": "malay-indonesian",
	// indian-scripts
	"hin": "indian-scripts", "mar": "indian-scripts", "ben": "indian-scripts", "tam": "indian-scripts",
	"tel": "indian-scripts", "guj": "indian-scripts", "kan": "indian-scripts", "mal": "indian-scripts", "pan": "indian-scripts",
}

// LanguageGroup returns the language-family group slug for an ISO 639-3
// code, per the guess-the-language deck taxonomy (seeds/decks.yaml). A code
// belonging to no deck falls back to the bare code itself: an unmapped
// language becomes its own singleton group rather than silently joining ""
// (which would make every unmapped language look like a same-group match
// against every other unmapped language).
func LanguageGroup(code string) string {
	if g, ok := languageGroups[code]; ok {
		return g
	}
	return code
}
