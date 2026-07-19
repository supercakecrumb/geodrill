package game

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// ── fakeCatalogStore ─────────────────────────────────────────────────────

type fakeCatalogStore struct {
	byPath   map[string]storage.Topic
	children map[uuid.UUID][]storage.Topic
	items    map[uuid.UUID][]storage.Item
}

func (f *fakeCatalogStore) GetTopicByPath(_ context.Context, path string) (storage.Topic, bool, error) {
	t, ok := f.byPath[path]
	return t, ok, nil
}

func (f *fakeCatalogStore) ListChildTopics(_ context.Context, parentID uuid.UUID) ([]storage.Topic, error) {
	return f.children[parentID], nil
}

func (f *fakeCatalogStore) ListActiveItemsByTopic(_ context.Context, topicID uuid.UUID) ([]storage.Item, error) {
	return f.items[topicID], nil
}

func TestLoadCatalog_FlattensGroupsIntoLanguages(t *testing.T) {
	container := storage.Topic{ID: uuid.New(), Slug: "guess-the-language"}
	romance := storage.Topic{ID: uuid.New(), Slug: "romance"}
	cjk := storage.Topic{ID: uuid.New(), Slug: "cjk"}

	store := &fakeCatalogStore{
		byPath:   map[string]storage.Topic{catalogPath: container},
		children: map[uuid.UUID][]storage.Topic{container.ID: {romance, cjk}},
		items: map[uuid.UUID][]storage.Item{
			romance.ID: {{Key: "spa", Label: "Spanish"}, {Key: "por", Label: "Portuguese"}},
			cjk.ID:     {{Key: "jpn", Label: "Japanese"}},
		},
	}

	catalog, err := LoadCatalog(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(catalog) != 3 {
		t.Fatalf("expected 3 languages, got %d: %+v", len(catalog), catalog)
	}
	byKey := map[string]Language{}
	for _, l := range catalog {
		byKey[l.Key] = l
	}
	if byKey["spa"].Group != "romance" || byKey["por"].Group != "romance" {
		t.Fatalf("expected spa/por grouped under romance, got %+v", byKey)
	}
	if byKey["jpn"].Group != "cjk" {
		t.Fatalf("expected jpn grouped under cjk, got %+v", byKey)
	}
	if byKey["spa"].Label != "Spanish" {
		t.Fatalf("expected label carried through, got %+v", byKey["spa"])
	}
}

func TestLoadCatalog_MissingContainer_ReturnsEmptyNotError(t *testing.T) {
	store := &fakeCatalogStore{}

	catalog, err := LoadCatalog(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(catalog) != 0 {
		t.Fatalf("expected an empty catalog when the container topic doesn't exist, got %+v", catalog)
	}
}

// ── fakeStatsStore ───────────────────────────────────────────────────────

type fakeStatsStore struct {
	rows map[string]storage.GameStats
}

func statsKey(userID uuid.UUID, gameKey string) string { return userID.String() + "|" + gameKey }

func (f *fakeStatsStore) RecordGameRun(_ context.Context, userID uuid.UUID, gameKey string, streak int, at time.Time) (storage.GameStats, error) {
	if f.rows == nil {
		f.rows = map[string]storage.GameStats{}
	}
	key := statsKey(userID, gameKey)
	cur := f.rows[key]
	best := cur.BestStreak
	if streak > best {
		best = streak
	}
	updated := storage.GameStats{UserID: userID, Game: gameKey, BestStreak: best, Runs: cur.Runs + 1, LastPlayedAt: at}
	f.rows[key] = updated
	return updated, nil
}

func (f *fakeStatsStore) GetGameStats(_ context.Context, userID uuid.UUID, gameKey string) (storage.GameStats, bool, error) {
	g, ok := f.rows[statsKey(userID, gameKey)]
	return g, ok, nil
}

func TestEngine_FinishRun_BestStreakOnlyGrows(t *testing.T) {
	stats := &fakeStatsStore{}
	e := NewEngine(&fakeSampler{}, stats)
	userID := uuid.New()
	t1 := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	if _, err := e.FinishRun(context.Background(), userID, 5, t1); err != nil {
		t.Fatalf("FinishRun (1): %v", err)
	}
	after1, found, err := e.Stats(context.Background(), userID)
	if err != nil || !found {
		t.Fatalf("Stats (1): found=%v err=%v", found, err)
	}
	if after1.BestStreak != 5 || after1.Runs != 1 {
		t.Fatalf("expected best=5 runs=1 after the first run, got %+v", after1)
	}

	// A lower streak on the second run must not lower the best.
	if _, err := e.FinishRun(context.Background(), userID, 3, t2); err != nil {
		t.Fatalf("FinishRun (2): %v", err)
	}
	after2, found, err := e.Stats(context.Background(), userID)
	if err != nil || !found {
		t.Fatalf("Stats (2): found=%v err=%v", found, err)
	}
	if after2.BestStreak != 5 {
		t.Fatalf("expected best_streak to stay at 5 after a lower-streak run, got %d", after2.BestStreak)
	}
	if after2.Runs != 2 {
		t.Fatalf("expected runs=2 after a second run, got %d", after2.Runs)
	}
	if !after2.LastPlayedAt.Equal(t2) {
		t.Fatalf("expected last_played_at stamped to the second run's time, got %v", after2.LastPlayedAt)
	}
}

func TestEngine_Stats_NotFoundForAFreshUser(t *testing.T) {
	e := NewEngine(&fakeSampler{}, &fakeStatsStore{})
	_, found, err := e.Stats(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if found {
		t.Fatal("expected found=false for a user who has never played")
	}
}

// ── Engine.NextRound ─────────────────────────────────────────────────────

func TestEngine_NextRound_BuildsARoundWhenContentExists(t *testing.T) {
	catalog := romanceCatalog()
	c1 := storage.Content{ID: uuid.New(), Payload: "El coche es rojo.", Source: "tatoeba#1"}
	sampler := &fakeSampler{queue: map[string][]storage.Content{"spa": {c1}}}
	e := NewEngine(sampler, &fakeStatsStore{})

	round, ok, err := e.NextRound(context.Background(), rand.New(rand.NewSource(1)), uuid.New(), catalog, 0, nil)
	if err != nil {
		t.Fatalf("NextRound: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if round.Correct.Key != "spa" || round.ContentID != c1.ID {
		t.Fatalf("unexpected round: %+v", round)
	}
	if len(round.Options) != distractorCount+1 {
		t.Fatalf("expected %d options, got %d", distractorCount+1, len(round.Options))
	}
}

func TestEngine_NextRound_NoContentAnywhere_ReturnsNotOK(t *testing.T) {
	e := NewEngine(&fakeSampler{}, &fakeStatsStore{})
	round, ok, err := e.NextRound(context.Background(), rand.New(rand.NewSource(1)), uuid.New(), romanceCatalog(), 0, nil)
	if err != nil {
		t.Fatalf("NextRound: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false with an empty corpus, got round %+v", round)
	}
}
