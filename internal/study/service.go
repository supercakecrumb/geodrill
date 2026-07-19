// Package study is geodrill's service layer (architecture §1.6, §4, §5):
// the single place that wires internal/topics' Generator registry + gating
// service, internal/storage's tables, and engram's Scheduler/lifecycle
// helpers into the four interfaces internal/telegram/services.go declares —
// StudyService (/study introductions), TopicService (/topics browser),
// Trainer (mode-aware exercises), and IntroCapStore (/settings daily
// cap). cmd/bot constructs one Service and hands it to telegram.Config as
// all four fields.
//
// Service uses the same injected-clock/rng style geodrill's exercise
// engines have always used: a deterministic *rand.Rand (seeded once at
// construction, guarded by a mutex — math/rand.Rand is not
// concurrency-safe) and an injectable now func() time.Time, so tests can
// fix both.
package study

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// *storage.Store implements telegram.IntroCapStore directly (GetIntroCap/
// SetIntroCap, internal/storage/introcap.go) — no Service wrapper is
// needed; cmd/bot wires telegram.Config.IntroCapStore to the *storage.Store
// it already constructs. This assertion lives here (not in internal/storage,
// which cannot import internal/telegram without an import cycle, since
// internal/telegram already imports internal/storage).
var _ telegram.IntroCapStore = (*storage.Store)(nil)

// Registry is the narrow slice of internal/topics' package-level Generator
// registry Service needs (Get by quiz_kind) — declared locally so Service
// depends on an interface, not the global topics.Get function, keeping unit
// tests free to inject fake generators without mutating process-wide
// registry state. GlobalRegistry adapts the real thing for production
// wiring (cmd/bot).
type Registry interface {
	Get(kind string) (topics.Generator, bool)
}

// globalRegistry adapts internal/topics' package-level Register/Get to the
// Registry interface.
type globalRegistry struct{}

func (globalRegistry) Get(kind string) (topics.Generator, bool) { return topics.Get(kind) }

// GlobalRegistry is the production Registry: internal/topics' real,
// process-wide Generator registry (populated once at cmd/bot startup via
// topics.Register).
var GlobalRegistry Registry = globalRegistry{}

// Service implements StudyService, TopicService, Trainer, and
// IntroCapStore (internal/telegram/services.go) over internal/storage +
// internal/topics + engram.
type Service struct {
	store  *storage.Store
	sched  *engram.Scheduler
	reg    Registry
	gating *topics.Service

	mu  sync.Mutex // guards rng (math/rand.Rand is not concurrency-safe)
	rng *rand.Rand

	now func() time.Time // injectable clock (defaults to time.Now)
}

// New builds a Service. store/sched are shared with the rest of the app;
// reg is normally study.GlobalRegistry (a fake in tests); seed seeds the
// shuffle RNG deterministically; now defaults to time.Now when nil.
func New(store *storage.Store, sched *engram.Scheduler, reg Registry, now func() time.Time, seed int64) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:  store,
		sched:  sched,
		reg:    reg,
		gating: topics.NewService(store),
		rng:    rand.New(rand.NewSource(seed)),
		now:    now,
	}
}

// Now returns the service clock.
func (s *Service) Now() time.Time { return s.now() }

// ── shared helpers ──────────────────────────────────────────────────────

// locationFor resolves a user's IANA timezone, falling back to UTC on an
// empty or invalid value.
func locationFor(u storage.User) *time.Location {
	if u.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// startOfDay returns midnight of t's calendar date in loc.
func startOfDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// dayBounds returns the [from, to) local-day bounds "now" falls in, for
// Store.CountIntroductionsToday.
func dayBounds(now time.Time, loc *time.Location) (from, to time.Time) {
	from = startOfDay(now, loc)
	return from, from.AddDate(0, 0, 1)
}

// tierComplete applies the architecture §4.1 tier-complete policy (100%
// introduced AND >= topics.TierCompleteShare in good shape) to a freshly
// recomputed TierProgress row — the one piece RecomputeTierProgressForTier
// leaves to the caller since it's policy, not a stored fact.
func tierComplete(p storage.TierProgress) bool {
	if p.TotalItems == 0 || p.IntroducedItems != p.TotalItems {
		return false
	}
	return float64(p.GoodShapeItems) >= float64(p.TotalItems)*topics.TierCompleteShare
}

// currentTierFrom derives a user's "current tier" and the highest tier that
// exists in the item catalog from a full per-tier progress list — the shape
// Store.RecomputeTierProgress returns: one row per tier that has ANY items
// in the catalog (via its LEFT JOIN to user_items), whether or not this
// particular user has touched it yet.
//
// Definition of "current tier" (documented here since it's a policy call,
// same spirit as tierComplete): tiers unlock as a SET, not a ladder
// (topics.UnlockedTiers: {0,1} ∪ {n+2 : tier n complete}) — completing tier
// n unlocks tier n+2 independently per tier, so two tiers can be open at
// once and finish out of order. A single scalar can't represent that set
// exactly, so "current tier" picks the compact, honest compression: the
// LOWEST tier that is not yet complete — i.e. the frontier the user still
// has to work through. A brand-new user has 0 introduced items everywhere,
// so tier 0 (not tier 1, even though {0,1} are both unlocked from the
// start) reads as the current tier: it's the lowest thing still open. Once
// every tier that exists is complete (everything mastered), there is no
// "lowest incomplete" tier left, so current tier falls back to the highest
// tier that exists.
func currentTierFrom(progress []storage.TierProgress) (tier, maxTier int) {
	haveIncomplete := false
	for i, p := range progress {
		t := int(p.Tier)
		if i == 0 || t > maxTier {
			maxTier = t
		}
		if !tierComplete(p) && (!haveIncomplete || t < tier) {
			tier = t
			haveIncomplete = true
		}
	}
	if !haveIncomplete {
		tier = maxTier
	}
	return tier, maxTier
}

// CurrentTier reports userID's current tier and the highest tier that
// exists in the item catalog (see currentTierFrom), for /stats' "Tier X of
// Y" line. Reuses Store.RecomputeTierProgress — the same on-the-fly
// per-tier query the gating cache (internal/topics) is itself built from —
// instead of adding new SQL: it already returns exactly the shape needed
// (every existing tier, ascending, per-user introduced/good-shape counts).
func (s *Service) CurrentTier(ctx context.Context, userID uuid.UUID) (tier, maxTier int, err error) {
	progress, err := s.store.RecomputeTierProgress(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	tier, maxTier = currentTierFrom(progress)
	return tier, maxTier, nil
}

// RecomputeTiers refreshes the entire cached per-tier progress
// (user_tier_progress) for one user and re-derives each tier's Complete flag
// (tierComplete). It exists for the /settings GeoGuessr-only toggle: tier
// totals depend on the per-user gg_only flag (the tier queries filter on
// items.gg_relevant when it's set), so flipping the setting must invalidate
// and rebuild the whole gating cache immediately — otherwise AllowedTiers
// (which reads the cache) would gate against stale totals until the next
// answer happened to recompute a tier. Store.RecomputeTierProgress already
// reads the user's live gg_only via its users join, so this simply persists
// what it returns. Per-answer recompute stays single-tier (finishAnswer);
// this whole-catalog rebuild is only for the rare toggle.
func (s *Service) RecomputeTiers(ctx context.Context, userID uuid.UUID) error {
	rows, err := s.store.RecomputeTierProgress(ctx, userID)
	if err != nil {
		return err
	}
	for _, p := range rows {
		p.Complete = tierComplete(p)
		if err := s.store.UpsertTierProgress(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// toInt16Slice converts []int (topics.Service.AllowedTiers' return type) to
// []int16 (Store.ListCandidateIntroItems' parameter type).
func toInt16Slice(tiers []int) []int16 {
	out := make([]int16, len(tiers))
	for i, t := range tiers {
		out[i] = int16(t)
	}
	return out
}

// allowedTierSet is toInt16Slice's counterpart as a lookup set, for the
// TopicService locked-tier badge.
func allowedTierSet(tiers []int) map[int16]bool {
	set := make(map[int16]bool, len(tiers))
	for _, t := range tiers {
		set[int16(t)] = true
	}
	return set
}

// max0 clamps n to a minimum of 0.
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
