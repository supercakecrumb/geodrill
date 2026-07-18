package specialchars

import (
	"context"
	"fmt"
	"strings"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// BuildIntro implements topics.Generator: the teaching blurb shown before an
// item's first exercise (architecture §5.1), e.g.
// `🔤 "ø" — used in Norwegian and Danish. o with stroke — Norwegian, Danish`.
// Plain text — HTML-escaping is the telegram layer's job (registry.go's
// IntroCard doc).
func (g *Generator) BuildIntro(_ context.Context, item storage.Item) (topics.IntroCard, error) {
	p, err := parsePayload(item.Payload)
	if err != nil {
		return topics.IntroCard{}, err
	}

	text := fmt.Sprintf("🔤 “%s” — used in %s.", p.Char, languageListText(p.Languages))
	if p.Note != "" {
		text += " " + p.Note
	}
	return topics.IntroCard{Text: text}, nil
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
