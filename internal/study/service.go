// Package study is geodrill's v2 service layer (architecture §1.6, §4, §5):
// the single place that wires internal/topics' Generator registry + gating
// service, internal/storage's v2 tables, and engram's Scheduler/lifecycle
// helpers into the four interfaces internal/telegram/v2_types.go declares —
// StudyService (/study introductions), TopicService (/topics browser),
// TrainerV2 (mode-aware exercises), and IntroCapStore (/settings daily
// cap). cmd/bot constructs one Service and hands it to telegram.Config as
// all four fields.
//
// Service follows internal/train.Service's injected-clock/rng style: a
// deterministic *rand.Rand (seeded once at construction, guarded by a
// mutex — math/rand.Rand is not concurrency-safe) and an injectable now
// func() time.Time, so tests can fix both.
//
// # Contract friction: the bridge row (see bridge.go)
//
// exercises.skill_id/content_id and reviews.skill_id/chosen_key/correct_key
// are still NOT NULL (architecture §3.1 schedules dropping them in a later
// migration, 000007, out of this package's scope — internal/study's file
// scope is additive-only and explicitly excludes migrations/). Three of the
// four wave-3 topic generators (specialchars, roadside, words) have no
// natural "skill" counterpart at all, and specialchars/roadside/words
// exercises often have no sampled content either (their payload is
// self-contained). Service satisfies those legacy NOT NULL columns with a
// single idempotently-upserted placeholder deck/skill/content row (bridge.go)
// — real values are used instead whenever a generator supplies them (e.g.
// guesslang's sampled content_id). This is a deliberate, documented shim:
// it means legacy /stats' per-deck breakdown (train.Service.Stats ->
// ReviewStatsByDeck, which has no mode filter) will show one extra
// "v2 bridge (internal)" deck entry once any non-guesslang v2 exercise is
// answered, until 000007 lands and the shim can be deleted outright.
package study

import (
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

// Service implements StudyService, TopicService, TrainerV2, and
// IntroCapStore (internal/telegram/v2_types.go) over internal/storage +
// internal/topics + engram.
type Service struct {
	store  *storage.Store
	sched  *engram.Scheduler
	reg    Registry
	gating *topics.Service

	mu  sync.Mutex // guards rng (math/rand.Rand is not concurrency-safe)
	rng *rand.Rand

	now func() time.Time // injectable clock (defaults to time.Now)

	// bridge* cache the placeholder skill/content ids ensureBridge resolves
	// (bridge.go), populated lazily and at most once per process.
	bridgeOnce      sync.Once
	bridgeErr       error
	bridgeSkillID   uuid.UUID
	bridgeContentID uuid.UUID
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

// locationFor mirrors internal/telegram/internal/train's locationOf: resolve
// a user's IANA timezone, falling back to UTC on an empty or invalid value.
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
