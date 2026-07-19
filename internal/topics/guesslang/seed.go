// Package guesslang seeds the languages/guess-the-language topic tree
// (architecture §2.11/§3.4): the shared "languages" root, a
// "guess-the-language" container, one group topic per seeds/decks.yaml deck
// (e.g. "romance", "cjk", ...), and one item per language within its group.
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
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/supercakecrumb/geodrill/internal/content"
	"github.com/supercakecrumb/geodrill/internal/storage"
)

// Kind is the topics.quiz_kind stamped on every group topic this package
// seeds. It no longer has a topics.Generator registered against it (the
// guess-the-language exercise moved into the game zone, internal/game —
// vibe/design-game-zone.md): group topics are seeded with is_quizzable =
// false, so quiz_kind here is purely descriptive provenance, not a live
// registry lookup key.
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
	// UpsertTopic requires a value.
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

// Seed loads seeds/decks.yaml and upserts the languages/guess-the-language
// topic tree (architecture §2.11/§3.4): one child topic per deck group, and
// one item per language within its group. Idempotent: topic and item
// upserts key off (parent,slug) and (topic_id,key) respectively, so
// re-running Seed after an edit to the yaml converges rather than
// duplicating rows.
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

// SeedFromData upserts topics/items from an already-loaded content.SeedFile
// — the core logic, separated from file I/O so tests can exercise it with
// data built in-process (mirrors internal/topics/specialchars.SeedFromData).
func SeedFromData(ctx context.Context, store *storage.Store, sf content.SeedFile) error {
	root, err := store.UpsertTopic(ctx, nil, RootSlug, RootName, 0, 0, "container", []string{"single"}, false, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("guesslang: upsert %q root topic: %w", RootSlug, err)
	}

	rootID := root.ID
	container, err := store.UpsertTopic(ctx, &rootID, ContainerSlug, ContainerName, containerPosition, 0, "container", []string{"single"}, false, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("guesslang: upsert %q container topic: %w", ContainerSlug, err)
	}
	containerID := container.ID

	for gi, d := range sf.Decks {
		// is_quizzable=false (design-game-zone.md "What happens to the FSRS
		// state of language items"): no introductions, no reviews, excluded
		// from /study, /train, tier gating, and the /topics browser (which
		// hides subtrees with no quizzable descendants). The items
		// themselves stay seeded — the game zone reads them directly via
		// internal/game.LoadCatalog for their group structure, names, and
		// keys.
		group, err := store.UpsertTopic(ctx, &containerID, d.Slug, d.Name, gi, BaseTier, Kind, []string{"single"}, false, []byte(`{}`))
		if err != nil {
			return fmt.Errorf("guesslang: upsert group topic %q: %w", d.Slug, err)
		}

		// Sort language codes for deterministic item.position — Go map
		// iteration order over d.Languages is randomized per-run and would
		// otherwise make position (and therefore intro/browse ordering)
		// nondeterministic across re-seeds.
		langs := make([]string, 0, len(d.Languages))
		for lang := range d.Languages {
			langs = append(langs, lang)
		}
		sort.Strings(langs)

		for ii, lang := range langs {
			label := d.Languages[lang]
			payload, err := json.Marshal(itemPayload{Language: lang})
			if err != nil {
				return fmt.Errorf("guesslang: marshal payload for %s/%s: %w", d.Slug, lang, err)
			}
			if _, err := store.UpsertItem(ctx, group.ID, lang, label, nil, payload, nil, ii, true); err != nil {
				return fmt.Errorf("guesslang: upsert item %s/%s: %w", d.Slug, lang, err)
			}
		}
	}
	return nil
}
