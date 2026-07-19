package cities

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Topic tree constants. Cities gets its OWN root — per
// vibe/design-cities.md §3, "a city is not a country attribute, so it
// doesn't belong under the countries root shared by profiles/TLDs/capitals"
// — with one quiz-bearing leaf under it (this first slice is a single
// direction, unlike tld/capitals' sibling pairs).
const (
	RootSlug     = "cities"
	RootName     = "Cities"
	rootPosition = 3 // sibling ordering among root topics (languages=0, roads=1, countries=2, cities=3)

	// BaseTier is topics.base_tier for the leaf. It is a required, non-null
	// column but otherwise irrelevant here: every item gets an explicit tier
	// from the shared countrytier rubric (engine's TierFromCountry rule), so
	// nothing ever inherits base_tier (mirrors tld.BaseTier / capitals.BaseTier).
	BaseTier = 0

	LeafSlug     = "city-to-country"
	LeafName     = "City → Country"
	leafPosition = 0
)

// cityTopic is the topic's full path (parents first). Defined as a function
// so both the generator descriptor (generator.go) and Seed share one
// definition of the tree (mirrors tld/capitals' *Topic() functions).
func cityTopic() []engine.TopicNode {
	return []engine.TopicNode{
		{Slug: RootSlug, Name: RootName, Position: rootPosition},
		{Slug: LeafSlug, Name: LeafName, Position: leafPosition, BaseTier: BaseTier, QuizKind: Kind, ExerciseModes: []string{"autocomplete"}, IsQuizzable: true},
	}
}

// citySeed mirrors one entry under seeds/cities.yaml `cities:`. Key is the
// collision-safe "<iso2>:<slug>" identifier the seed file's header comment
// documents (cities collide across countries and even within one — unlike
// tld/capitals, which have exactly one item per country, so this is used as
// items.key rather than the country iso2). Population is thousands
// (approximate metro or city population, per the seed file's header) and
// drives the population-descending ordering that sets items.position.
type citySeed struct {
	Key        string `yaml:"key"`
	Name       string `yaml:"name"`
	Country    string `yaml:"country"`
	Population int64  `yaml:"population"`
}

// citiesSeedFile is the top-level shape of seeds/cities.yaml.
type citiesSeedFile struct {
	Cities []citySeed `yaml:"cities"`
}

// seedPath resolves a path under seeds/ relative to this source file, so
// both Seed and loadLookupTables behave the same whether the caller's
// working directory is the repo root (cmd/bot, cmd/ingest) or this
// package's own directory (`go test` runs with cwd = package dir) — mirrors
// tld.seedPath / capitals.seedPath.
func seedPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "seeds", name)
}

func citiesSeedPath() string    { return seedPath("cities.yaml") }
func countriesSeedPath() string { return seedPath("countries.yaml") }

// loadCitiesFile reads and parses seeds/cities.yaml at path.
func loadCitiesFile(path string) (citiesSeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return citiesSeedFile{}, fmt.Errorf("cities: read cities seed file %s: %w", path, err)
	}
	var sf citiesSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return citiesSeedFile{}, fmt.Errorf("cities: parse cities seed file %s: %w", path, err)
	}
	return sf, nil
}

// sortByPopulationDesc returns a copy of entries ordered by population
// descending (biggest city first, per design §1's "studied biggest ->
// smallest"), with a stable tiebreak on Key so re-running Seed after a data
// edit never reorders two cities that happen to share a population figure —
// a pure function so it's unit-testable without a database.
func sortByPopulationDesc(entries []citySeed) []citySeed {
	sorted := make([]citySeed, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Population != sorted[j].Population {
			return sorted[i].Population > sorted[j].Population
		}
		return sorted[i].Key < sorted[j].Key
	})
	return sorted
}

// Seed loads seeds/cities.yaml and upserts the cities/city-to-country tree
// plus one item per city, positioned by population rank (biggest first).
// Countries themselves are NOT seeded here (roadside owns that data): every
// entry's country must already resolve via store.GetCountryByISO, or Seed
// fails loudly rather than silently skipping. Run cities after a
// country-owning topic in cmd/ingest.
//
// Idempotent: topics/items are keyed upserts (mirrors tld.Seed /
// capitals.Seed), so re-running Seed after a data fix converges rather than
// duplicating or diverging.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, citiesSeedPath())
}

// SeedFromFile is Seed with an explicit seed-file path, so tests don't
// depend on the working directory the test binary happens to run from
// (mirrors tld.SeedFromFile / capitals.SeedFromFile).
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	sf, err := loadCitiesFile(path)
	if err != nil {
		return err
	}
	entries := sortByPopulationDesc(sf.Cities)

	// Resolve every referenced country against the already-seeded country
	// data, once per distinct iso2 (a country hosts several cities).
	byISO := make(map[string]storage.Country)
	for _, e := range entries {
		if _, ok := byISO[e.Country]; ok {
			continue
		}
		c, found, err := store.GetCountryByISO(ctx, e.Country)
		if err != nil {
			return fmt.Errorf("cities: lookup country %s: %w", e.Country, err)
		}
		if !found {
			return fmt.Errorf("cities: seeds/cities.yaml references unknown country %q (seed a country-owning topic first)", e.Country)
		}
		byISO[e.Country] = c
	}

	// One item per city, payload caching everything the generator needs (see
	// itemPayload). Position = slice index (engine.Seed's convention), so
	// the population-descending sort above is what actually sets
	// items.position — biggest city first.
	items := make([]engine.ItemSeed, 0, len(entries))
	for _, e := range entries {
		c := byISO[e.Country]
		items = append(items, engine.ItemSeed{
			Key:   e.Key,
			Label: e.Name,
			Payload: itemPayload{
				CityName:    e.Name,
				Flag:        c.FlagEmoji,
				CountryName: c.Name,
				ISOA2:       c.ISOA2,
				ISOA3:       c.ISOA3,
			},
			CountryISO: e.Country,
		})
	}

	data := engine.SeedData{Items: items, Countries: byISO, TierFromCountry: true}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: Kind, Topic: cityTopic()}, data); err != nil {
		return fmt.Errorf("cities: seed %s: %w", LeafSlug, err)
	}
	return nil
}
