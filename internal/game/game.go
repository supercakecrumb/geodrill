// Package game implements geodrill's game zone (vibe/design-game-zone.md):
// ephemeral, non-scheduled quiz sessions, distinct from the FSRS study
// pipeline (internal/study). A sentence is not SRS material — there is no
// value in "repeating" a random sentence — so guess-the-language lives here
// instead, as a game: a quick test of recognition ability, with streaks and
// a personal best.
//
// Today the zone has one game, Language Roulette (round.go): an endless
// streak run over the languages seeded by internal/topics/guesslang
// (is_quizzable=false — the language items carry only the group structure,
// names, and payload the game needs, never introductions/reviews). Only
// aggregate stats persist (game_stats, one row per user per game key); a
// run's in-progress state (streak, open answer, used content ids) lives
// in-memory per chat in internal/telegram/game.go, mirroring how open
// exercises are tracked there.
package game

import (
	"context"
	"math/rand"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/guesslang"
)

// LanguageRoulette is this package's game_stats.game key for the one game
// implemented today (design doc "Persistence"). The future timed
// city-listing game (design doc "The game zone") will get its own key
// alongside this one.
const LanguageRoulette = "language_roulette"

// catalogPath is the topic path guesslang.Seed builds the language tree
// under (languages/guess-the-language). Reused here — rather than
// duplicated as a string literal — so the game and the seeder can never
// drift apart.
var catalogPath = guesslang.RootSlug + "/" + guesslang.ContainerSlug

// CatalogStore is the narrow read interface LoadCatalog needs over the
// seeded languages/guess-the-language topic tree (guesslang.Seed):
// *storage.Store already satisfies this directly.
type CatalogStore interface {
	GetTopicByPath(ctx context.Context, path string) (storage.Topic, bool, error)
	ListChildTopics(ctx context.Context, parentID uuid.UUID) ([]storage.Topic, error)
	ListActiveItemsByTopic(ctx context.Context, topicID uuid.UUID) ([]storage.Item, error)
}

// LoadCatalog reads every active language item under
// languages/guess-the-language into a flat []Language, grouped by their
// group topic's slug (Language.Group — the difficulty ramp's "same group"
// test, design doc table). Returns an empty, non-error catalog when the
// container topic doesn't exist yet (e.g. seeding hasn't run) — callers
// treat an empty catalog the same as "no content available".
func LoadCatalog(ctx context.Context, store CatalogStore) ([]Language, error) {
	container, found, err := store.GetTopicByPath(ctx, catalogPath)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	groups, err := store.ListChildTopics(ctx, container.ID)
	if err != nil {
		return nil, err
	}
	var out []Language
	for _, g := range groups {
		items, err := store.ListActiveItemsByTopic(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, Language{Key: it.Key, Label: it.Label, Group: g.Slug})
		}
	}
	return out, nil
}

// StatsStore is the narrow game_stats persistence Engine needs
// (*storage.Store's RecordGameRun/GetGameStats already satisfy this
// directly).
type StatsStore interface {
	RecordGameRun(ctx context.Context, userID uuid.UUID, gameKey string, streak int, at time.Time) (storage.GameStats, error)
	GetGameStats(ctx context.Context, userID uuid.UUID, gameKey string) (storage.GameStats, bool, error)
}

// Engine runs Language Roulette rounds and persists its aggregate stats.
// Round-building is deterministic given the injected rng (the same testing
// pattern internal/topics generators use); the only real nondeterminism is
// the database's own random content sampling (ContentSampler), the same
// caveat every topic generator already carries.
type Engine struct {
	sampler ContentSampler
	stats   StatsStore
}

// NewEngine builds an Engine over sampler (content) and stats (game_stats
// persistence) — cmd/bot wires both directly to the same *storage.Store.
func NewEngine(sampler ContentSampler, stats StatsStore) *Engine {
	return &Engine{sampler: sampler, stats: stats}
}

// NextRound builds one round for a run in progress: streak is the run's
// current streak (drives the difficulty ramp) and used is the set of
// content ids already shown this run (no repeat sentences within a run,
// design doc). ok=false means no eligible sentence exists anywhere in
// catalog right now (e.g. no content ingested yet) — the caller should
// treat the game as unavailable rather than send a broken round.
func (e *Engine) NextRound(ctx context.Context, rng *rand.Rand, userID uuid.UUID, catalog []Language, streak int, used map[uuid.UUID]bool) (Round, bool, error) {
	correct, content, ok, err := pickCorrect(ctx, e.sampler, rng, userID, catalog, used)
	if err != nil || !ok {
		return Round{}, ok, err
	}
	return buildRound(rng, catalog, correct, content, streak), true, nil
}

// FinishRun persists the end of a Language Roulette run (design doc
// "Persistence"): best_streak only ever grows, runs increments by one, and
// last_played_at is stamped to at. Returns the updated aggregate for the
// "final streak, personal best, runs played" closer.
func (e *Engine) FinishRun(ctx context.Context, userID uuid.UUID, streak int, at time.Time) (storage.GameStats, error) {
	return e.stats.RecordGameRun(ctx, userID, LanguageRoulette, streak, at)
}

// Stats returns userID's persisted Language Roulette aggregate. found=false
// means they've never finished a run (no game_stats row yet).
func (e *Engine) Stats(ctx context.Context, userID uuid.UUID) (storage.GameStats, bool, error) {
	return e.stats.GetGameStats(ctx, userID, LanguageRoulette)
}
