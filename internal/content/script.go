package content

import "unicode"

// expectedScripts maps ISO-639-3 language codes to the set of Unicode scripts
// a sentence in that language is expected to use (architecture contract §6).
var expectedScripts = map[string][]*unicode.RangeTable{
	// Latin
	"spa": {unicode.Latin},
	"por": {unicode.Latin},
	"ita": {unicode.Latin},
	"fra": {unicode.Latin},
	"cat": {unicode.Latin},
	"ron": {unicode.Latin},
	"pol": {unicode.Latin},
	"ces": {unicode.Latin},
	"slk": {unicode.Latin},
	"hrv": {unicode.Latin},
	"slv": {unicode.Latin},
	"swe": {unicode.Latin},
	"nor": {unicode.Latin},
	"dan": {unicode.Latin},
	"isl": {unicode.Latin},
	"fin": {unicode.Latin},
	"vie": {unicode.Latin},
	"ind": {unicode.Latin},
	"msa": {unicode.Latin},

	// South Asian scripts (India, Bangladesh, Sri Lanka)
	"hin": {unicode.Devanagari}, // Hindi
	"mar": {unicode.Devanagari}, // Marathi (looks like Hindi — the hard pair)
	"ben": {unicode.Bengali},
	"tam": {unicode.Tamil},
	"tel": {unicode.Telugu},
	"guj": {unicode.Gujarati},
	"kan": {unicode.Kannada},
	"mal": {unicode.Malayalam},
	"pan": {unicode.Gurmukhi}, // Punjabi
	"sin": {unicode.Sinhala},  // Sinhala (Sri Lanka)

	// Cyrillic — this is what drops Latin-script srp rows.
	"rus": {unicode.Cyrillic},
	"ukr": {unicode.Cyrillic},
	"bul": {unicode.Cyrillic},
	"srp": {unicode.Cyrillic},
	"mkd": {unicode.Cyrillic},

	// Han
	"cmn": {unicode.Han},

	// Han + Hiragana + Katakana
	"jpn": {unicode.Han, unicode.Hiragana, unicode.Katakana},

	// Hangul
	"kor": {unicode.Hangul},

	// Thai
	"tha": {unicode.Thai},

	// Khmer
	"khm": {unicode.Khmer},

	// Lao
	"lao": {unicode.Lao},

	// Myanmar
	"mya": {unicode.Myanmar},
}

// ExpectedScripts returns the expected Unicode scripts for lang. ok is false
// if lang is not a recognized code.
func ExpectedScripts(lang string) ([]*unicode.RangeTable, bool) {
	scripts, ok := expectedScripts[lang]
	return scripts, ok
}

// ScriptOK reports whether every letter rune in text belongs to at least one
// of lang's expected scripts. Non-letter runes (digits, punctuation,
// whitespace, combining marks, symbols) are exempt from the check. If lang is
// unknown, ScriptOK always returns false.
func ScriptOK(lang, text string) bool {
	scripts, ok := ExpectedScripts(lang)
	if !ok {
		return false
	}
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		if !unicode.In(r, scripts...) {
			return false
		}
	}
	return true
}
