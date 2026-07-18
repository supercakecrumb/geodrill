package words

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// Topic tree constants this package seeds and generates for (architecture
// §6.3): the shared "languages" container root and this topic's own
// quiz-bearing child.
const (
	RootSlug  = "languages"
	RootName  = "Languages"
	TopicSlug = "common-words"
	TopicName = "Common words"
	// QuizKind matches topics.Topic.QuizKind / Generator.Kind() — the seam
	// that lets internal/study pick this package's Generator without ever
	// switching on a topic slug.
	QuizKind = "word_language"
	// BaseTier is topics.base_tier for languages/common-words (architecture
	// §4 rubric: tier 2, "common"). Individual words may still override via
	// items.tier (the yaml `tier` field).
	BaseTier = 2
	// topicPosition is this topic's sibling ordering under "languages"
	// (cosmetic; special-characters and guess-the-language are expected to
	// occupy 0 and 1).
	topicPosition = 2
)

// wordEntry mirrors one entry under seeds/common_words.yaml `words:`. Audit
// and Note are corpus-audit bookkeeping only (architecture §6.3 audit task)
// — they are never written into items.payload.
type wordEntry struct {
	Word     string `yaml:"word"`
	Language string `yaml:"language"`
	Meaning  string `yaml:"meaning"`
	Tier     int16  `yaml:"tier"`
	Audit    string `yaml:"audit,omitempty"` // "waived" = kept despite a failed corpus-audit floor/collision check
	Note     string `yaml:"note,omitempty"`  // required alongside audit:waived — the justification
}

// seedFile is the top-level shape of seeds/common_words.yaml.
type seedFile struct {
	Topic string      `yaml:"topic"`
	Words []wordEntry `yaml:"words"`
}

// seedFilePath resolves the absolute path to seeds/common_words.yaml
// relative to this source file, so Seed behaves the same whether the
// caller's working directory is the repo root (cmd/bot, cmd/ingest-style
// tools) or this package's own directory (`go test`).
func seedFilePath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "seeds", "common_words.yaml")
}

// loadSeedFile reads and parses the seed YAML at path.
func loadSeedFile(path string) (seedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return seedFile{}, fmt.Errorf("words: read seed file %s: %w", path, err)
	}
	var sf seedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return seedFile{}, fmt.Errorf("words: parse seed file %s: %w", path, err)
	}
	return sf, nil
}

// Seed loads seeds/common_words.yaml and upserts the "languages" container
// topic, the "languages/common-words" quiz topic, and every word item
// (architecture §6.3). Idempotent: topic and item upserts key off
// (parent,slug) and (topic_id,key) respectively, so re-running Seed after an
// edit to the yaml converges rather than duplicating rows.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, seedFilePath())
}

// SeedFromFile is Seed with an explicit seed-file path, so tests don't
// depend on the working directory the test binary happens to run from.
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	sf, err := loadSeedFile(path)
	if err != nil {
		return err
	}

	root, err := store.UpsertTopic(ctx, nil, RootSlug, RootName, 0, 0, "container", []string{"single"}, false, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("words: upsert root topic %q: %w", RootSlug, err)
	}

	rootID := root.ID
	topic, err := store.UpsertTopic(ctx, &rootID, TopicSlug, TopicName, topicPosition, BaseTier, QuizKind, []string{"single"}, true, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("words: upsert topic %q: %w", TopicSlug, err)
	}

	for i, w := range sf.Words {
		key := w.Language + ":" + w.Word
		payload, err := json.Marshal(itemPayload{Word: w.Word, Language: w.Language, Meaning: w.Meaning})
		if err != nil {
			return fmt.Errorf("words: marshal payload for %s: %w", key, err)
		}
		tier := w.Tier
		if _, err := store.UpsertItem(ctx, topic.ID, key, w.Word, &tier, payload, nil, i, true); err != nil {
			return fmt.Errorf("words: upsert item %s: %w", key, err)
		}
	}
	return nil
}
