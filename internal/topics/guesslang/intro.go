package guesslang

import (
	"context"
	"fmt"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// maxSampleRunes caps how much of a sampled sentence the intro card quotes —
// sentences in the corpus can run much longer than a Telegram teaching card
// should show.
const maxSampleRunes = 80

// BuildIntro implements topics.Generator: the teaching blurb shown before an
// item's first exercise (architecture §5.1), e.g.
// `🗣 Spanish — you'll see sentences like: "El coche es rojo."`. BuildIntro
// has no per-user context (topics.Generator's signature), so the sample is
// an unfiltered SampleContentAny — falls back to a sample-free blurb when
// the language currently has no ingested content, rather than failing the
// intro card outright.
func (g *Generator) BuildIntro(ctx context.Context, item storage.Item) (topics.IntroCard, error) {
	content, found, err := g.sampler.SampleContentAny(ctx, item.Key)
	if err != nil {
		return topics.IntroCard{}, err
	}
	if !found {
		return topics.IntroCard{
			Text: fmt.Sprintf("🗣 %s — you'll see sentences in this language to guess.", item.Label),
		}, nil
	}
	return topics.IntroCard{
		Text: fmt.Sprintf("🗣 %s — you'll see sentences like: “%s”", item.Label, shortSample(content.Payload)),
	}, nil
}

// shortSample truncates s to a short teaching-card sample, cutting at a rune
// boundary and appending an ellipsis when truncated.
func shortSample(s string) string {
	r := []rune(s)
	if len(r) <= maxSampleRunes {
		return s
	}
	return string(r[:maxSampleRunes]) + "…"
}
