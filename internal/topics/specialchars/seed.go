package specialchars

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
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
// the repo root). Idempotent — engine.Seed is pure upserts keyed on
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

// SeedFromData maps an already-loaded SeedFile onto the generic engine
// seeder (the descriptor's Topic path plus one ItemSeed per char) —
// separated from file I/O so tests can exercise it with data built
// in-process.
func SeedFromData(ctx context.Context, store *storage.Store, sf SeedFile) error {
	items := make([]engine.ItemSeed, len(sf.Chars))
	for i, c := range sf.Chars {
		tier := c.Tier
		items[i] = engine.ItemSeed{
			Key:     c.Char,
			Label:   c.Char,
			Tier:    &tier,
			Payload: payload{Char: c.Char, Script: c.Script, Languages: c.Languages, Note: c.Note},
		}
	}
	if err := engine.Seed(ctx, store, descriptor, engine.SeedData{Items: items}); err != nil {
		return fmt.Errorf("specialchars: %w", err)
	}
	return nil
}
