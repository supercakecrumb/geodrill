package study

import (
	"testing"

	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/telegram"
)

func TestIntroReasonForEmpty(t *testing.T) {
	if got := introReasonForEmpty(0); got != telegram.IntroNoneAvailable {
		t.Fatalf("no candidates at all should be IntroNoneAvailable, got %v", got)
	}
	if got := introReasonForEmpty(5); got != telegram.IntroBudgetExhausted {
		t.Fatalf("candidates present but nothing picked should be IntroBudgetExhausted, got %v", got)
	}
}

func TestIntroAckText(t *testing.T) {
	cases := []struct {
		outcome engram.IntroOutcome
		want    string
	}{
		{engram.IntroGotIt, "✅ Added to your review queue."},
		{engram.IntroKnown, "🧠 Marked as known — won't be quizzed."},
		{engram.IntroTestMe, "🎯 In the queue with a long first interval."},
	}
	for _, c := range cases {
		if got := introAckText(c.outcome); got != c.want {
			t.Fatalf("introAckText(%v) = %q, want %q", c.outcome, got, c.want)
		}
	}
}

func TestTierComplete(t *testing.T) {
	cases := []struct {
		name string
		p    storage.TierProgress
		want bool
	}{
		{"no items", storage.TierProgress{TotalItems: 0}, false},
		{"not fully introduced", storage.TierProgress{TotalItems: 4, IntroducedItems: 3, GoodShapeItems: 4}, false},
		{"introduced but under 80% good shape", storage.TierProgress{TotalItems: 5, IntroducedItems: 5, GoodShapeItems: 3}, false},
		{"exactly 80% good shape", storage.TierProgress{TotalItems: 5, IntroducedItems: 5, GoodShapeItems: 4}, true},
		{"100% good shape", storage.TierProgress{TotalItems: 5, IntroducedItems: 5, GoodShapeItems: 5}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tierComplete(c.p); got != c.want {
				t.Fatalf("tierComplete(%+v) = %v, want %v", c.p, got, c.want)
			}
		})
	}
}
