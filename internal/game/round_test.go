package game

import (
	"context"
	"math/rand"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// fakeSampler is a deterministic, in-memory ContentSampler double (mirrors
// internal/topics/guesslang's own generator_test.go fake): each key has a
// queue of contents returned in order across successive calls, so tests can
// simulate a corpus running dry for one language. SampleContentAny shares
// the same queue as SampleContent (the game never writes reviews, so
// SampleContent's exclusion is inert in practice — see round.go's
// ContentSampler doc).
type fakeSampler struct {
	queue map[string][]storage.Content
}

func (f *fakeSampler) SampleContent(_ context.Context, _ uuid.UUID, key string) (storage.Content, bool, error) {
	q := f.queue[key]
	if len(q) == 0 {
		return storage.Content{}, false, nil
	}
	f.queue[key] = q[1:]
	return q[0], true, nil
}

func (f *fakeSampler) SampleContentAny(ctx context.Context, key string) (storage.Content, bool, error) {
	return f.SampleContent(ctx, uuid.Nil, key)
}

func mkLang(key, label, group string) Language {
	return Language{Key: key, Label: label, Group: group}
}

// ── pickDistractors ──────────────────────────────────────────────────────

func romanceCatalog() []Language {
	return []Language{
		mkLang("spa", "Spanish", "romance"),
		mkLang("por", "Portuguese", "romance"),
		mkLang("ita", "Italian", "romance"),
		mkLang("fra", "French", "romance"),
		mkLang("cat", "Catalan", "romance"),
		mkLang("ron", "Romanian", "romance"),
		mkLang("rus", "Russian", "cyrillic"),
		mkLang("ukr", "Ukrainian", "cyrillic"),
		mkLang("jpn", "Japanese", "cjk"),
		mkLang("kor", "Korean", "cjk"),
	}
}

func hasSameGroup(distractors []Language, group string) bool {
	for _, d := range distractors {
		if d.Group == group {
			return true
		}
	}
	return false
}

func allSameGroup(distractors []Language, group string) bool {
	for _, d := range distractors {
		if d.Group != group {
			return false
		}
	}
	return true
}

func assertDistinctAndExcludesCorrect(t *testing.T, distractors []Language, correct Language) {
	t.Helper()
	if len(distractors) != distractorCount {
		t.Fatalf("expected %d distractors, got %d: %+v", distractorCount, len(distractors), distractors)
	}
	seen := map[string]bool{correct.Key: true}
	for _, d := range distractors {
		if seen[d.Key] {
			t.Fatalf("duplicate or correct-language distractor %q among %+v", d.Key, distractors)
		}
		seen[d.Key] = true
	}
}

func TestPickDistractors_LowStreak_CrossGroup(t *testing.T) {
	catalog := romanceCatalog()
	correct := mkLang("spa", "Spanish", "romance")
	rng := rand.New(rand.NewSource(1))

	distractors := pickDistractors(rng, catalog, correct, 0)
	assertDistinctAndExcludesCorrect(t, distractors, correct)
	if hasSameGroup(distractors, correct.Group) {
		t.Fatalf("streak 0-4 must pick distractors from different groups, got %+v", distractors)
	}
}

func TestPickDistractors_MidStreak_AtLeastOneSameGroup(t *testing.T) {
	catalog := romanceCatalog()
	correct := mkLang("spa", "Spanish", "romance")
	rng := rand.New(rand.NewSource(1))

	distractors := pickDistractors(rng, catalog, correct, streakCloserAt)
	assertDistinctAndExcludesCorrect(t, distractors, correct)
	if !hasSameGroup(distractors, correct.Group) {
		t.Fatalf("streak >= %d must include at least one same-group distractor, got %+v", streakCloserAt, distractors)
	}
}

func TestPickDistractors_HighStreak_AllSameGroupWhenEnoughSiblings(t *testing.T) {
	catalog := romanceCatalog() // romance has 5 siblings for "spa" (>= 3)
	correct := mkLang("spa", "Spanish", "romance")
	rng := rand.New(rand.NewSource(1))

	distractors := pickDistractors(rng, catalog, correct, streakHardestAt)
	assertDistinctAndExcludesCorrect(t, distractors, correct)
	if !allSameGroup(distractors, correct.Group) {
		t.Fatalf("streak >= %d with >=%d siblings must use only same-group distractors, got %+v",
			streakHardestAt, minGroupSiblingsForHardest, distractors)
	}
}

func TestPickDistractors_HighStreak_FallsBackWithoutEnoughSiblings(t *testing.T) {
	// "rus" has only one sibling (ukr) in the cyrillic group — fewer than
	// minGroupSiblingsForHardest, so streak 15+ must fall back to the 5-14
	// policy (at least one same-group, not necessarily all three).
	catalog := romanceCatalog()
	correct := mkLang("rus", "Russian", "cyrillic")
	rng := rand.New(rand.NewSource(1))

	distractors := pickDistractors(rng, catalog, correct, streakHardestAt)
	assertDistinctAndExcludesCorrect(t, distractors, correct)
	if !hasSameGroup(distractors, correct.Group) {
		t.Fatalf("expected the fallback policy to still include the one same-group sibling, got %+v", distractors)
	}
}

func TestPickDistractors_NoSiblingsAtAll_FallsBackToCrossGroup(t *testing.T) {
	// A singleton group has zero siblings: even at a high streak, "at least
	// one same-group" can never be satisfied, so it must degrade gracefully
	// (still return distractorCount valid distractors) rather than error.
	catalog := append(romanceCatalog(), mkLang("fin", "Finnish", "uralic"))
	correct := mkLang("fin", "Finnish", "uralic")
	rng := rand.New(rand.NewSource(1))

	for _, streak := range []int{0, streakCloserAt, streakHardestAt} {
		distractors := pickDistractors(rng, catalog, correct, streak)
		assertDistinctAndExcludesCorrect(t, distractors, correct)
	}
}

// ── buildRound / determinism ──────────────────────────────────────────────

func TestBuildRound_DeterministicGivenSameSeed(t *testing.T) {
	catalog := romanceCatalog()
	correct := mkLang("spa", "Spanish", "romance")
	content := storage.Content{ID: uuid.New(), Payload: "El coche es rojo.", Source: "tatoeba#1"}

	round1 := buildRound(rand.New(rand.NewSource(42)), catalog, correct, content, 3)
	round2 := buildRound(rand.New(rand.NewSource(42)), catalog, correct, content, 3)

	if len(round1.Options) != len(round2.Options) {
		t.Fatalf("option count differs across identical seeds: %d vs %d", len(round1.Options), len(round2.Options))
	}
	for i := range round1.Options {
		if round1.Options[i] != round2.Options[i] {
			t.Fatalf("option order differs across identical seeds at index %d: %+v vs %+v", i, round1.Options, round2.Options)
		}
	}
	if round1.Correct != correct {
		t.Fatalf("Correct = %+v, want %+v", round1.Correct, correct)
	}
	if round1.ContentID != content.ID || round1.Prompt != content.Payload || round1.Source != content.Source {
		t.Fatalf("round content fields not carried through: %+v", round1)
	}
}

func TestBuildRound_OptionsIncludeCorrectExactlyOnce(t *testing.T) {
	catalog := romanceCatalog()
	correct := mkLang("jpn", "Japanese", "cjk")
	content := storage.Content{ID: uuid.New(), Payload: "これは本です。"}

	round := buildRound(rand.New(rand.NewSource(7)), catalog, correct, content, 0)
	count := 0
	for _, o := range round.Options {
		if o == correct {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected the correct language exactly once among options, got %d in %+v", count, round.Options)
	}
	if len(round.Options) != distractorCount+1 {
		t.Fatalf("expected %d options, got %d", distractorCount+1, len(round.Options))
	}
}

// ── pickCorrect ────────────────────────────────────────────────────────────

func TestPickCorrect_PicksTheOnlyLanguageWithContent(t *testing.T) {
	catalog := []Language{mkLang("spa", "Spanish", "romance"), mkLang("por", "Portuguese", "romance")}
	c1 := storage.Content{ID: uuid.New(), Payload: "Hola."}
	sampler := &fakeSampler{queue: map[string][]storage.Content{"spa": {c1}}}

	lang, content, ok, err := pickCorrect(context.Background(), sampler, rand.New(rand.NewSource(1)), uuid.New(), catalog, nil)
	if err != nil {
		t.Fatalf("pickCorrect: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if lang.Key != "spa" || content.ID != c1.ID {
		t.Fatalf("expected spa/%v, got %s/%v", c1.ID, lang.Key, content.ID)
	}
}

func TestPickCorrect_SkipsAlreadyUsedContent_RetriesSameLanguage(t *testing.T) {
	catalog := []Language{mkLang("spa", "Spanish", "romance")}
	c1 := storage.Content{ID: uuid.New(), Payload: "Hola uno."}
	c2 := storage.Content{ID: uuid.New(), Payload: "Hola dos."}
	sampler := &fakeSampler{queue: map[string][]storage.Content{"spa": {c1, c2}}}
	used := map[uuid.UUID]bool{c1.ID: true}

	_, content, ok, err := pickCorrect(context.Background(), sampler, rand.New(rand.NewSource(1)), uuid.New(), catalog, used)
	if err != nil {
		t.Fatalf("pickCorrect: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if content.ID != c2.ID {
		t.Fatalf("expected the second, unused sentence %v, got %v", c2.ID, content.ID)
	}
}

func TestPickCorrect_MovesToNextLanguageWhenExhausted(t *testing.T) {
	// spa's only sentence is already used; por has a fresh one. Regardless
	// of shuffle order, the only valid pick is por/c2.
	catalog := []Language{mkLang("spa", "Spanish", "romance"), mkLang("por", "Portuguese", "romance")}
	c1 := storage.Content{ID: uuid.New(), Payload: "Hola."}
	c2 := storage.Content{ID: uuid.New(), Payload: "Ola."}
	sampler := &fakeSampler{queue: map[string][]storage.Content{"spa": {c1}, "por": {c2}}}
	used := map[uuid.UUID]bool{c1.ID: true}

	for seed := int64(0); seed < 10; seed++ {
		lang, content, ok, err := pickCorrect(context.Background(), sampler, rand.New(rand.NewSource(seed)), uuid.New(), catalog, used)
		if err != nil {
			t.Fatalf("pickCorrect (seed %d): %v", seed, err)
		}
		if !ok || lang.Key != "por" || content.ID != c2.ID {
			t.Fatalf("seed %d: expected por/%v, got ok=%v %s/%v", seed, c2.ID, ok, lang.Key, content.ID)
		}
		sampler.queue = map[string][]storage.Content{"spa": {c1}, "por": {c2}} // reset for next seed
	}
}

func TestPickCorrect_NoContentAnywhere_ReturnsNotOK(t *testing.T) {
	catalog := romanceCatalog()
	sampler := &fakeSampler{queue: map[string][]storage.Content{}}

	_, _, ok, err := pickCorrect(context.Background(), sampler, rand.New(rand.NewSource(1)), uuid.New(), catalog, nil)
	if err != nil {
		t.Fatalf("pickCorrect: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when no language has any content")
	}
}
