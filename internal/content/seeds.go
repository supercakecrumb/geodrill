package content

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SeedDeck is one deck entry from seeds/decks.yaml: a slug, a display name,
// and its ISO-639-3 -> human-label language map.
type SeedDeck struct {
	Slug      string            `yaml:"slug"`
	Name      string            `yaml:"name"`
	Languages map[string]string `yaml:"languages"`
}

// SeedFile is the top-level shape of seeds/decks.yaml (architecture contract
// §6).
type SeedFile struct {
	Decks []SeedDeck `yaml:"decks"`
}

// LoadSeeds reads and parses a decks.yaml file at path.
func LoadSeeds(path string) (SeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SeedFile{}, fmt.Errorf("read seeds file %s: %w", path, err)
	}
	var sf SeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return SeedFile{}, fmt.Errorf("parse seeds file %s: %w", path, err)
	}
	return sf, nil
}

// LanguageLabels returns a language -> label map merged across every deck in
// the seed file. If two decks disagree on a label for the same language, the
// first deck encountered (in file order) wins.
func (sf SeedFile) LanguageLabels() map[string]string {
	out := make(map[string]string)
	for _, d := range sf.Decks {
		for lang, label := range d.Languages {
			if _, exists := out[lang]; !exists {
				out[lang] = label
			}
		}
	}
	return out
}

// Languages returns every distinct language code referenced across all
// decks, in the order first encountered.
func (sf SeedFile) Languages() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, d := range sf.Decks {
		for lang := range d.Languages {
			if _, ok := seen[lang]; ok {
				continue
			}
			seen[lang] = struct{}{}
			out = append(out, lang)
		}
	}
	return out
}
