package specialchars

import (
	"fmt"
	"strings"
)

// introText renders the teaching blurb shown before an item's first
// exercise (architecture §5.1) — consumed by parseCard as engine.Card.Intro
// — e.g. `🔤 "ø" — used in Norwegian and Danish. o with stroke`.
// Plain text — HTML-escaping is the telegram layer's job (the topics
// package's IntroCard doc).
func introText(p payload) string {
	text := fmt.Sprintf("🔤 “%s” — used in %s.", p.Char, languageListText(p.Languages))
	if p.Note != "" {
		text += " " + p.Note
	}
	return text
}

// languageListText renders a natural-language "A and B" / "A, B and C" list
// of display names for a language-code list, preserving the caller's order
// (the seed file's declared order).
func languageListText(codes []string) string {
	names := make([]string, len(codes))
	for i, c := range codes {
		names[i] = languageLabel(c)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}
