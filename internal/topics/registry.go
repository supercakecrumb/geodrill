// Package topics is the topic-quiz-generator framework for geodrill v2: the
// conflict-avoidance seam that lets every quiz mechanic (special characters,
// road sides, common words, guess-the-language, ...) live in its own
// subpackage under internal/topics/<name>/ and register itself by
// topics.quiz_kind, so neither internal/train nor internal/telegram ever
// switch on a topic slug (architecture §3/§8, task W2.3).
//
// # Adding a new topic
//
// A topic worker implements Generator in its own package, keyed by the
// quiz_kind it handles (topics.Topic.QuizKind), and calls Register — from an
// init() in that package, or explicit wiring in cmd/bot — so this package
// and internal/train never import a topic package by name. Get/Kinds are how
// callers discover what's registered without a compile-time dependency on
// any individual topic.
//
// # Content access stays out of this package
//
// This package never touches a database: Generator is pure orchestration
// (build an exercise / intro card from the arguments it's given). A
// Generator implementation that needs DB-backed content (e.g. sampling a
// sentence for a language) should declare its OWN narrow interface for that
// need, e.g.:
//
//	type ContentSampler interface {
//	    SampleContent(ctx context.Context, userID uuid.UUID, key string) (storage.Content, bool, error)
//	}
//
// ...and carry an implementation of it as a field on the concrete generator
// struct, injected by the caller (cmd/bot) at construction time — the same
// pattern internal/train already uses for *storage.Store. Keeping the
// storage dependency on the topic's own struct (rather than on Generator or
// the registry) is what lets two topic workers land in the same wave without
// ever touching each other's files.
package topics

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// Option is one single-choice button (quiz.ModeSingle): a stable answer key
// plus its display label.
type Option struct {
	Key   string
	Label string
}

// OptionSet is one set-choice button (quiz.ModeSet): asserts a whole set of
// answer keys at once (e.g. "ø" -> {nor,dan}).
type OptionSet struct {
	Keys  []string
	Label string
}

// Exercise is a generator's mode-aware answer to "quiz this item" — shaped so
// ModeSingle, ModeSet, and ModeText all serialize cleanly into the v2
// exercises columns (mode, prompt, options jsonb, correct_answer, is_media;
// architecture §2.5):
//
//   - ModeSingle populates Options; CorrectAnswer is the correct option's Key.
//   - ModeSet populates OptionSets; CorrectAnswer is the correct set,
//     serialized (e.g. via quiz.CanonSet) by the caller that persists it.
//   - ModeText populates Accept (the accepted spellings for quiz.TextMatcher)
//     and leaves Options/OptionSets empty; CorrectAnswer is the canonical
//     spelling.
//
// MediaPath set (non-empty) means the exercise is a photo message
// (is_media=true, decision 6) instead of a text prompt.
type Exercise struct {
	Mode          quiz.Mode
	Prompt        string
	MediaPath     string
	Options       []Option
	OptionSets    []OptionSet
	Accept        []string
	CorrectAnswer string
	ContentID     *uuid.UUID
	Source        string
}

// ExerciseRequest carries everything a Generator needs to build one Exercise
// for a single item.
type ExerciseRequest struct {
	User     storage.User
	Topic    storage.Topic
	Item     storage.Item
	Siblings []storage.Item // active sibling items in the same topic (distractor pool)
	Mode     quiz.Mode
}

// IntroCard is the rendered introduction (teaching blurb) shown before an
// item ever enters the review queue (architecture §5.1). MediaPath set
// (non-empty) means it renders as a photo message instead of text.
type IntroCard struct {
	Text      string
	MediaPath string
}

// ErrNoContent is the sentinel a Generator returns from BuildExercise when
// item cannot currently be quizzed (e.g. its content pool is empty). Callers
// (internal/train) treat this like the pre-v2 buildExercise "found=false"
// path: skip the item and try the next due/introducible candidate rather
// than failing the whole request.
var ErrNoContent = fmt.Errorf("topics: no content available for item")

// Generator builds exercises and introduction cards for one quiz_kind. An
// implementation is registered once (via Register) and looked up by
// topics.quiz_kind (via Get) — nothing in internal/train or
// internal/telegram switches on a topic slug directly.
type Generator interface {
	// Kind returns the quiz_kind this generator handles (matches
	// topics.quiz_kind in the DB).
	Kind() string

	// BuildExercise assembles a mode-aware exercise for req.Item within its
	// topic. Deterministic given rng: the same rng state and req always
	// produce the same Exercise. Returns ErrNoContent when the item can't
	// currently be quizzed.
	BuildExercise(ctx context.Context, rng *rand.Rand, req ExerciseRequest) (Exercise, error)

	// BuildIntro renders the introduction card for item (the teaching blurb
	// shown before its first exercise, architecture §5.1).
	BuildIntro(ctx context.Context, item storage.Item) (IntroCard, error)
}

// TipProvider is an optional capability a Generator may additionally
// implement to supply post-answer recognition tips (mirroring
// internal/tips's role for the legacy language-id quiz). Modeled as a
// separate interface — rather than a method on Generator every topic must
// implement — so topics without tips (e.g. road sides) aren't forced to
// return a no-op. Callers should type-assert a Generator returned by Get:
//
//	if tp, ok := gen.(topics.TipProvider); ok {
//	    tips = tp.Tips()
//	}
type TipProvider interface {
	Tips() quiz.TipProvider
}

// ── registry ────────────────────────────────────────────────────────────────

var (
	registryMu sync.Mutex
	registry   = make(map[string]Generator)
)

// Register adds g to the registry under g.Kind(). Panics if a Generator is
// already registered for that kind — two topics sharing a quiz_kind is a
// programmer error caught at wiring time (cmd/bot init), never something to
// handle gracefully at runtime.
func Register(g Generator) {
	registryMu.Lock()
	defer registryMu.Unlock()

	kind := g.Kind()
	if _, exists := registry[kind]; exists {
		panic(fmt.Sprintf("topics: duplicate Generator registration for quiz_kind %q", kind))
	}
	registry[kind] = g
}

// Get looks up the Generator registered for kind. ok=false means no
// Generator has registered for it (unknown or not-yet-wired quiz_kind).
func Get(kind string) (Generator, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()

	g, ok := registry[kind]
	return g, ok
}

// Kinds returns every registered quiz_kind, sorted ascending.
func Kinds() []string {
	registryMu.Lock()
	defer registryMu.Unlock()

	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
