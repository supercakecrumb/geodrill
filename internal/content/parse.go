// Package content implements the pure filter/parse logic for the Tatoeba
// ingest pipeline (architecture contract §6): TSV line parsing, length and
// script checks, dedupe, and seeded-sample capping. Download/decompress
// concerns live in downloader.go; the pipeline glue lives in pipeline.go.
package content

import (
	"strings"
	"unicode/utf8"
)

// ParseLine parses one line of a Tatoeba per-language TSV export, which has
// the shape "id<TAB>lang<TAB>text". Trailing \r/\n are trimmed from text.
// ok is false for malformed lines (fewer than 3 tab-separated fields).
func ParseLine(line string) (id string, lang string, text string, ok bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	id = parts[0]
	lang = parts[1]
	text = strings.TrimRight(parts[2], "\r\n")
	if id == "" || lang == "" || text == "" {
		return "", "", "", false
	}
	return id, lang, text, true
}

// LengthOK reports whether text's rune count falls within [min, max]
// (inclusive on both ends).
func LengthOK(text string, min, max int) bool {
	n := utf8.RuneCountInString(text)
	return n >= min && n <= max
}

// DefaultMinLen and DefaultMaxLen are the contract's default sentence-length
// bounds (§6), in runes.
const (
	DefaultMinLen = 20
	DefaultMaxLen = 120
)
