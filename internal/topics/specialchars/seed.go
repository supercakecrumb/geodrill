package specialchars

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// defaultSeedPath is seeds/special_chars.yaml relative to the repo root
// (matching cmd/ingest's -seeds default convention for seeds/decks.yaml).
const defaultSeedPath = "seeds/special_chars.yaml"

// SeedChar is one entry of seeds/special_chars.yaml.
type SeedChar struct {
	Char      string   `yaml:"char"`
	Script    string   `yaml:"script"`
	Languages []string `yaml:"languages"`
	Tier      int16    `yaml:"tier"`
	Note      string   `yaml:"note"`
}

// SeedFile is the top-level shape of seeds/special_chars.yaml.
type SeedFile struct {
	Topic string     `yaml:"topic"`
	Chars []SeedChar `yaml:"chars"`
}

// LoadSeedFile reads and parses a special_chars.yaml file at path.
func LoadSeedFile(path string) (SeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SeedFile{}, fmt.Errorf("read seed file %s: %w", path, err)
	}
	var sf SeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return SeedFile{}, fmt.Errorf("parse seed file %s: %w", path, err)
	}
	return sf, nil
}

// Seed upserts the languages/special-characters topic (architecture §6.1)
// and its items from seeds/special_chars.yaml, resolved relative to the
// process's working directory (matching cmd/ingest's convention: run from
// the repo root). Idempotent — UpsertTopic/UpsertItem are upserts keyed on
// (parent,slug)/(topic,key), so re-running Seed after editing the yaml just
// updates the existing rows.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, defaultSeedPath)
}

// SeedFromFile is Seed with an explicit seed-file path (used by tests so
// they don't depend on the process's working directory).
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	sf, err := LoadSeedFile(path)
	if err != nil {
		return err
	}
	return SeedFromData(ctx, store, sf)
}

// SeedFromData upserts topics/items from an already-loaded SeedFile — the
// core logic, separated from file I/O so tests can exercise it with data
// built in-process.
func SeedFromData(ctx context.Context, store *storage.Store, sf SeedFile) error {
	root, err := store.UpsertTopic(ctx, nil, "languages", "Languages", 0, 0, "container", []string{"single"}, false, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("upsert languages root topic: %w", err)
	}

	parentID := root.ID
	topic, err := store.UpsertTopic(ctx, &parentID, "special-characters", "Special characters", 0, 2,
		Kind, []string{"single", "set", "text"}, true, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("upsert special-characters topic: %w", err)
	}

	for i, c := range sf.Chars {
		raw, err := json.Marshal(payload{Char: c.Char, Script: c.Script, Languages: c.Languages, Note: c.Note})
		if err != nil {
			return fmt.Errorf("marshal payload for %q: %w", c.Char, err)
		}
		tier := c.Tier
		if _, err := store.UpsertItem(ctx, topic.ID, c.Char, c.Char, &tier, raw, nil, i, true); err != nil {
			return fmt.Errorf("upsert item %q: %w", c.Char, err)
		}
	}
	return nil
}
