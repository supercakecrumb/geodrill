package topics

import (
	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// IntroCandidate pairs an item with the caller's current lifecycle for it
// (mirrors engram.Lifecycle: 0 new, 1 introduced, 2 reviewing, 3 known — an
// absent user_items row means new, architecture §2.3). Callers typically
// build this list from Store.ListCandidateIntroItems (already tier-unlocked
// and new-or-absent), but SelectIntroductions accepts any lifecycle so a
// stray non-new candidate is filtered defensively rather than trusted.
type IntroCandidate struct {
	Item      storage.Item
	Lifecycle int16
}

// SelectIntroductions is a thin composition over engram.NextIntroductions:
// it translates candidates (already ordered by the caller as a tier-then-
// within-topic-position topic round-robin — the priority order architecture
// §1.4/§4.2 expects the caller to supply, see ListCandidateIntroItems) into
// engram.IntroItem, applies the engine's daily-budget and New-only filter,
// and translates the winners back to storage.Item — preserving the caller's
// order. introducedToday is how many intros were
// already delivered today (Store.CountIntroductionsToday); dailyCap is the
// user's daily_intro_cap (0 = unlimited). Returns an empty non-nil slice
// when the budget is exhausted or nothing is New.
func SelectIntroductions(candidates []IntroCandidate, introducedToday int, dailyCap int) []storage.Item {
	byID := make(map[engram.SkillID]storage.Item, len(candidates))
	items := make([]engram.IntroItem, len(candidates))
	for i, c := range candidates {
		id := engram.SkillID(c.Item.ID.String())
		items[i] = engram.IntroItem{SkillID: id, Lifecycle: engram.Lifecycle(c.Lifecycle)}
		byID[id] = c.Item
	}

	picked := engram.NextIntroductions(items, introducedToday, engram.IntroConfig{MaxNewPerDay: dailyCap})

	out := make([]storage.Item, 0, len(picked))
	for _, p := range picked {
		out = append(out, byID[p.SkillID])
	}
	return out
}
