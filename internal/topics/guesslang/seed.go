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

// Topic tree constants this package seeds and generates for (architecture
// §2.11/§3.4): the shared "languages" container root, a "guess-the-language"
// container underneath it, and one quiz-bearing group topic per
// seeds/decks.yaml deck (e.g. "romance", "cjk", ...).
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
	// languages/guess-the-language (architecture §4 rubric: languages are
	// "very common signal", tier 1 — the legacy behavior of quizzing every
	// opted-in deck's languages with no further gating).
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
		group, err := store.UpsertTopic(ctx, &containerID, d.Slug, d.Name, gi, BaseTier, Kind, []string{"single"}, true, []byte(`{}`))
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
