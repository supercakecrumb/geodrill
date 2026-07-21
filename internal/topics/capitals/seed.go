package capitals

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Topic tree constants: a "countries" root container — shared with
// tld's tree (upserted idempotently by whichever topic seeds first, both
// packages agree on the same slug/name/position), a "capitals" container
// child, and the two quiz-bearing direction-topics under it. Mirrors
// internal/topics/tld's constants exactly, one level down.
const (
	CountriesRootSlug     = "countries"
	CountriesRootName     = "Countries"
	countriesRootPosition = 2 // cosmetic sibling ordering among root topics (languages=0, roads=1, countries=2)

	CapitalsSlug     = "capitals"
	CapitalsName     = "Capitals"
	capitalsPosition = 1 // sibling of "domains" (0) under "countries"

	// BaseTier is topics.base_tier for the two direction-topics. It is a
	// required, non-null column but otherwise irrelevant here: every item
	// gets an explicit tier from the shared countrytier rubric (engine's
	// TierFromCountry rule), so nothing ever inherits base_tier (mirrors
	// tld.BaseTier).
	BaseTier = 0

	CountryToCapitalSlug     = "country-to-capital"
	CountryToCapitalName     = "Country → Capital"
	countryToCapitalPosition = 0

	CapitalToCountrySlug     = "capital-to-country"
	CapitalToCountryName     = "Capital → Country"
	capitalToCountryPosition = 1

	// FactKeyCapital is the fact_defs.key this package owns: one primary
	// capital-city text fact per country, the single source of truth
	// items.payload.capital caches (mirrors tld.FactKeyTLD).
	FactKeyCapital = "capital"
)

// countryToCapitalTopic and capitalToCountryTopic are the two
// direction-topics' full paths (parents first). Defined as functions so
// both the generator descriptors (generator.go) and Seed share one
// definition of the tree — the shared countries parent is upserted
// idempotently by whichever direction (or the tld package) seeds first.
func countryToCapitalTopic() []engine.TopicNode {
	return []engine.TopicNode{
		{Slug: CountriesRootSlug, Name: CountriesRootName, Position: countriesRootPosition},
		{Slug: CapitalsSlug, Name: CapitalsName, Position: capitalsPosition},
		{Slug: CountryToCapitalSlug, Name: CountryToCapitalName, Position: countryToCapitalPosition, BaseTier: BaseTier, QuizKind: KindCountryToCapital, ExerciseModes: []string{"autocomplete"}, IsQuizzable: true},
	}
}

func capitalToCountryTopic() []engine.TopicNode {
	return []engine.TopicNode{
		{Slug: CountriesRootSlug, Name: CountriesRootName, Position: countriesRootPosition},
		{Slug: CapitalsSlug, Name: CapitalsName, Position: capitalsPosition},
		{Slug: CapitalToCountrySlug, Name: CapitalToCountryName, Position: capitalToCountryPosition, BaseTier: BaseTier, QuizKind: KindCapitalToCountry, ExerciseModes: []string{"autocomplete"}, IsQuizzable: true},
	}
}

// capitalRoleSeed mirrors one entry under seeds/capitals.yaml's per-country
// `capitals:` list. Role is descriptive only (official, seat-of-government,
// legislative, judicial, executive, de-facto) — this package doesn't branch
// on its value, it's only ever surfaced indirectly via the entry's Note.
type capitalRoleSeed struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"`
}

// capitalSeed mirrors one entry under seeds/capitals.yaml `entries:`.
// Capitals is ordered primary-first; entries with no capital at all
// (uninhabited or disputed territories) have an empty Capitals and only a
// Note.
type capitalSeed struct {
	Country  string            `yaml:"country"` // iso_a2, resolved against an already-seeded storage.Country
	Capitals []capitalRoleSeed `yaml:"capitals"`
	Note     string            `yaml:"note"`
}

// capitalsSeedFile is the top-level shape of seeds/capitals.yaml.
type capitalsSeedFile struct {
	Entries []capitalSeed `yaml:"entries"`
}

// seedPath resolves a path under seeds/ relative to this source file, so
// both Seed and loadLookupTables behave the same whether the caller's
// working directory is the repo root (cmd/bot, cmd/ingest) or this
// package's own directory (`go test` runs with cwd = package dir) — mirrors
// tld.seedPath.
func seedPath(name string) string {
	return engine.SeedPath(name)
}

func capitalsSeedPath() string  { return seedPath("capitals.yaml") }
func countriesSeedPath() string { return seedPath("countries.yaml") }

// loadCapitalsFile reads and parses seeds/capitals.yaml at path.
func loadCapitalsFile(path string) (capitalsSeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return capitalsSeedFile{}, fmt.Errorf("capitals: read capitals seed file %s: %w", path, err)
	}
	var sf capitalsSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return capitalsSeedFile{}, fmt.Errorf("capitals: parse capitals seed file %s: %w", path, err)
	}
	return sf, nil
}

// Seed loads seeds/capitals.yaml and upserts, for both direction-topics, the
// countries/capitals tree, the capital fact_def + one primary-capital
// country_facts row per country that has a capital, and one item per
// (direction-topic, country-with-a-capital). Countries themselves are NOT
// seeded here (roadside/tld own that data): every entry's iso_a2 must
// already resolve via store.GetCountryByISO, or Seed fails loudly rather
// than silently skipping. Run capitals after a country-owning topic in
// cmd/ingest.
//
// Entries with no `capitals:` list (Antarctica, Bouvet Island, Western
// Sahara, Heard/McDonald Islands, Svalbard, French Southern Territories, US
// Minor Outlying Islands) contribute no fact and no item in either
// direction — there is nothing to ask.
//
// Idempotent: topics/items are keyed upserts and the capital fact is
// replaced per-country (engine.SeedCountryFacts), so re-running Seed after
// a data fix converges rather than duplicating or diverging (mirrors
// tld.Seed).
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, capitalsSeedPath())
}

// SeedFromFile is Seed with an explicit seed-file path, so tests don't
// depend on the working directory the test binary happens to run from
// (mirrors tld.SeedFromFile).
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	sf, err := loadCapitalsFile(path)
	if err != nil {
		return err
	}

	// Only entries with at least one listed capital ever produce a fact or
	// an item — resolve just those against the already-seeded country data.
	byISO := make(map[string]storage.Country)
	entries := make([]capitalSeed, 0, len(sf.Entries))
	for _, e := range sf.Entries {
		if len(e.Capitals) == 0 {
			continue
		}
		c, found, err := store.GetCountryByISO(ctx, e.Country)
		if err != nil {
			return fmt.Errorf("capitals: lookup country %s: %w", e.Country, err)
		}
		if !found {
			return fmt.Errorf("capitals: seeds/capitals.yaml references unknown country %q (seed a country-owning topic first)", e.Country)
		}
		byISO[e.Country] = c
		entries = append(entries, e)
	}

	// The capital fact per country — the single source of truth item
	// payloads cache (consistency asserted by the integration test). Always
	// the PRIMARY capital (capitals[0]).
	factValues := make([]engine.FactValue, 0, len(entries))
	for _, e := range entries {
		val := e.Capitals[0].Name
		factValues = append(factValues, engine.FactValue{CountryISO: e.Country, Text: &val, Source: "seeds/capitals.yaml"})
	}
	capitalDef := engine.FactDef{Key: FactKeyCapital, Label: "Capital city", ValueType: "text", Cardinality: "single", Dataset: "seeds/capitals.yaml"}
	if err := engine.SeedCountryFacts(ctx, store, capitalDef, factValues, byISO); err != nil {
		return fmt.Errorf("capitals: %w", err)
	}

	// One item per country-with-a-capital, payload caching everything the
	// generator needs (see itemPayload). The same item set feeds both
	// direction-topics — the two directions differ only in how their
	// descriptor's Parse maps the payload, not in the payload itself
	// (mirrors tld.Seed).
	items := make([]engine.ItemSeed, 0, len(entries))
	for _, e := range entries {
		c := byISO[e.Country]
		items = append(items, engine.ItemSeed{
			Key:   e.Country,
			Label: c.Name,
			Payload: itemPayload{
				Flag:    c.FlagEmoji,
				Name:    c.Name,
				ISOA2:   c.ISOA2,
				ISOA3:   c.ISOA3,
				Capital: e.Capitals[0].Name,
				Note:    e.Note,
			},
			CountryISO: e.Country,
		})
	}

	data := engine.SeedData{Items: items, Countries: byISO, TierFromCountry: true}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: KindCountryToCapital, Topic: countryToCapitalTopic()}, data); err != nil {
		return fmt.Errorf("capitals: seed %s: %w", CountryToCapitalSlug, err)
	}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: KindCapitalToCountry, Topic: capitalToCountryTopic()}, data); err != nil {
		return fmt.Errorf("capitals: seed %s: %w", CapitalToCountrySlug, err)
	}
	return nil
}
