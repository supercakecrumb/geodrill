// Package guesslang seeds the languages/guess-the-language topic tree
// (architecture §2.11/§3.4): the shared "languages" root, a
// "guess-the-language" container, one group topic per seeds/decks.yaml deck
// (e.g. "romance", "cjk", ...), and one item per language within its group
// — one engine.Seed call per deck, each with the full three-node path.
//
// The guess-the-language exercise itself no longer lives here: sentences
// are not SRS material, so it moved into the game zone as Language
// Roulette (internal/game — vibe/design-game-zone.md fixes the design).
// This package's remaining job is purely the seed: group topics are
// upserted with is_quizzable = false (no introductions, no reviews, hidden
// from /study, /train, and the /topics browser), while the language items
// stay seeded — they carry the group structure, names, and payload
// internal/game.LoadCatalog reads to build the game's catalog.
package guesslang

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/supercakecrumb/geodrill/internal/content"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Kind is the topics.quiz_kind stamped on every group topic this package
// seeds. It no longer has a topics.Generator registered against it (the
// guess-the-language exercise moved into the game zone, internal/game —
// vibe/design-game-zone.md): group topics are seeded with is_quizzable =
// false, so quiz_kind here is purely descriptive provenance, not a live
// registry lookup key — and this package's descriptors are seed-only
// (QuizKind + Topic; no Parse, no Generator is ever constructed).
const Kind = "language_id"

// Topic tree constants this package seeds for (architecture §2.11/§3.4,
// design-game-zone.md): the shared "languages" container root, a
// "guess-the-language" container underneath it, and one group topic per
// seeds/decks.yaml deck (e.g. "romance", "cjk", ...) — group topics carry
// the language items the game zone reads (internal/game.LoadCatalog), not
// a live study-pipeline quiz.
const (
	RootSlug = "languages"
	RootName = "Languages"

	ContainerSlug = "guess-the-language"
	ContainerName = "Guess the language"
	// containerPosition is this container's sibling ordering under
	// "languages" (cosmetic; special-characters=0, guess-the-language=1,
	// common-words=2, per internal/topics/words' own position comment).
	containerPosition = 1

	// BaseTier is topics.base_tier for every group topic under
	// languages/guess-the-language. Vestigial now that these topics are
	// is_quizzable=false (no tier gating ever reads it), kept only because
	// the topics row requires a value.
	BaseTier = 1
)

// itemPayload is the items.payload shape for a language_id item: the
// language code, cached alongside items.key (which already carries it) per
// the task's exact payload shape.
type itemPayload struct {
	Language string `json:"language"`
}

// seedsPath resolves the absolute path to seeds/decks.yaml relative to this
// source file, so Seed behaves the same whether the caller's working
// directory is the repo root (cmd/bot, cmd/ingest-style tools) or this
// package's own directory (`go test` always runs with cwd set to the
// package directory — mirrors internal/topics/roadside's seedPath).
func seedsPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "seeds", "decks.yaml")
}

// Seed loads seeds/decks.yaml and seeds the languages/guess-the-language
// topic tree (architecture §2.11/§3.4): one child topic per deck group, and
// one item per language within its group. Idempotent: engine.Seed is pure
// upserts keyed off (parent,slug) and (topic_id,key), so re-running Seed
// after an edit to the yaml converges rather than duplicating rows.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, seedsPath())
}

// SeedFromFile is Seed with an explicit seed-file path, so tests don't
// depend on the working directory the test binary happens to run from
// (mirrors internal/topics/words.SeedFromFile).
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	sf, err := content.LoadSeeds(path)
	if err != nil {
		return fmt.Errorf("guesslang: %w", err)
	}
	return SeedFromData(ctx, store, sf)
}

// SeedFromData seeds topics/items from an already-loaded content.SeedFile —
// the core logic, separated from file I/O so tests can exercise it with
// data built in-process (mirrors internal/topics/specialchars.SeedFromData).
// Each deck becomes one engine.Seed call whose descriptor path ends in that
// deck's group topic; the shared root/container parents are re-upserted per
// deck (idempotent, converges to the same rows).
func SeedFromData(ctx context.Context, store *storage.Store, sf content.SeedFile) error {
	for gi, d := range sf.Decks {
		// is_quizzable=false (design-game-zone.md "What happens to the FSRS
		// state of language items"): no introductions, no reviews, excluded
		// from /study, /train, tier gating, and the /topics browser (which
		// hides subtrees with no quizzable descendants). The items
		// themselves stay seeded — the game zone reads them directly via
		// internal/game.LoadCatalog for their group structure, names, and
		// keys.
		desc := engine.Descriptor{
			QuizKind: Kind,
			Topic: []engine.TopicNode{
				{Slug: RootSlug, Name: RootName},
				{Slug: ContainerSlug, Name: ContainerName, Position: containerPosition},
				{Slug: d.Slug, Name: d.Name, Position: gi, BaseTier: BaseTier, QuizKind: Kind, IsQuizzable: false},
			},
		}

		// Sort language codes for deterministic item.position — Go map
		// iteration order over d.Languages is randomized per-run and would
		// otherwise make position (and therefore browse ordering)
		// nondeterministic across re-seeds.
		langs := make([]string, 0, len(d.Languages))
		for lang := range d.Languages {
			langs = append(langs, lang)
		}
		sort.Strings(langs)

		items := make([]engine.ItemSeed, len(langs))
		for ii, lang := range langs {
			items[ii] = engine.ItemSeed{
				Key:     lang,
				Label:   d.Languages[lang],
				Payload: itemPayload{Language: lang},
			}
		}

		if err := engine.Seed(ctx, store, desc, engine.SeedData{Items: items}); err != nil {
			return fmt.Errorf("guesslang: seed group %q: %w", d.Slug, err)
		}
	}
	return nil
}
