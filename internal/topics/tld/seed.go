package tld

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Topic tree constants (design §2): a "countries" root container — shared
// with design-country-profiles.md's tree, upserted idempotently by whichever
// topic seeds first — a "domains" container child, and the two quiz-bearing
// direction-topics under it.
const (
	CountriesRootSlug     = "countries"
	CountriesRootName     = "Countries"
	countriesRootPosition = 2 // cosmetic sibling ordering among root topics (languages=0, roads=1, countries=2)

	DomainsSlug     = "domains"
	DomainsName     = "Domains"
	domainsPosition = 0 // sole child of "countries" from this package's perspective

	// BaseTier is topics.base_tier for the two direction-topics. It is a
	// required, non-null column but otherwise irrelevant here: every item gets
	// an explicit tier from the shared countrytier rubric (engine's
	// TierFromCountry rule), so nothing ever inherits base_tier (mirrors
	// roadside.BaseTier).
	BaseTier = 0

	TLDToCountrySlug     = "tld-to-country"
	TLDToCountryName     = "TLD → Country"
	tldToCountryPosition = 0

	CountryToTLDSlug     = "country-to-tld"
	CountryToTLDName     = "Country → TLD"
	countryToTLDPosition = 1

	// FactKeyTLD is the fact_defs.key this package owns (design §1/§4): one
	// ccTLD text fact per country, the single source of truth items.payload.tld
	// caches.
	FactKeyTLD = "tld"
)

// tldToCountryTopic and countryToTLDTopic are the two direction-topics' full
// paths (parents first). Defined as functions so both the generator
// descriptors (generator.go) and Seed share one definition of the tree — the
// shared countries/domains parents are upserted idempotently by whichever
// direction seeds first.
func tldToCountryTopic() []engine.TopicNode {
	return []engine.TopicNode{
		{Slug: CountriesRootSlug, Name: CountriesRootName, Position: countriesRootPosition},
		{Slug: DomainsSlug, Name: DomainsName, Position: domainsPosition},
		{Slug: TLDToCountrySlug, Name: TLDToCountryName, Position: tldToCountryPosition, BaseTier: BaseTier, QuizKind: KindTLDToCountry, ExerciseModes: []string{"autocomplete"}, IsQuizzable: true},
	}
}

func countryToTLDTopic() []engine.TopicNode {
	return []engine.TopicNode{
		{Slug: CountriesRootSlug, Name: CountriesRootName, Position: countriesRootPosition},
		{Slug: DomainsSlug, Name: DomainsName, Position: domainsPosition},
		{Slug: CountryToTLDSlug, Name: CountryToTLDName, Position: countryToTLDPosition, BaseTier: BaseTier, QuizKind: KindCountryToTLD, ExerciseModes: []string{"text"}, IsQuizzable: true},
	}
}

// tldSeed mirrors one entry under seeds/tlds.yaml `tlds:`. Note is optional
// free text describing a quirk about the domain (e.g. Tuvalu's ".tv" being
// licensed to television sites) surfaced in the intro card.
type tldSeed struct {
	Country string `yaml:"country"` // iso_a2, resolved against an already-seeded storage.Country
	TLD     string `yaml:"tld"`     // e.g. ".tv", always dot-prefixed
	Note    string `yaml:"note"`
}

// tldsSeedFile is the top-level shape of seeds/tlds.yaml.
type tldsSeedFile struct {
	TLDs []tldSeed `yaml:"tlds"`
}

// seedPath resolves a path under seeds/ relative to this source file, so both
// Seed and loadLookupTables behave the same whether the caller's working
// directory is the repo root (cmd/bot, cmd/ingest) or this package's own
// directory (`go test` runs with cwd = package dir) — mirrors
// roadside.seedPath.
func seedPath(name string) string {
	return engine.SeedPath(name)
}

func tldsSeedPath() string      { return seedPath("tlds.yaml") }
func countriesSeedPath() string { return seedPath("countries.yaml") }

// loadTLDsFile reads and parses seeds/tlds.yaml at path.
func loadTLDsFile(path string) (tldsSeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tldsSeedFile{}, fmt.Errorf("tld: read tlds seed file %s: %w", path, err)
	}
	var sf tldsSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return tldsSeedFile{}, fmt.Errorf("tld: parse tlds seed file %s: %w", path, err)
	}
	return sf, nil
}

// Seed loads seeds/tlds.yaml and upserts, for both direction-topics, the
// countries/domains tree, the tld fact_def + one tld country_facts row per
// entry, and one item per (direction-topic, country) — design §5. Countries
// themselves are NOT seeded here (roadside/country-profiles own that data):
// every entry's iso_a2 must already resolve via store.GetCountryByISO, or
// Seed fails loudly rather than silently skipping. Run tld after a
// country-owning topic in cmd/ingest.
//
// Idempotent: topics/items are keyed upserts and the tld fact is replaced
// per-country (engine.SeedCountryFacts), so re-running Seed after a data fix
// converges rather than duplicating or diverging (mirrors roadside.Seed).
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, tldsSeedPath())
}

// SeedFromFile is Seed with an explicit seed-file path, so tests don't depend
// on the working directory the test binary happens to run from (mirrors
// roadside.SeedFromFiles).
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	sf, err := loadTLDsFile(path)
	if err != nil {
		return err
	}

	// Resolve every referenced country against the already-seeded country
	// data (this package does not own countries) into the lookup the engine
	// seeder and the fact seeder both need.
	byISO := make(map[string]storage.Country, len(sf.TLDs))
	for _, e := range sf.TLDs {
		c, found, err := store.GetCountryByISO(ctx, e.Country)
		if err != nil {
			return fmt.Errorf("tld: lookup country %s: %w", e.Country, err)
		}
		if !found {
			return fmt.Errorf("tld: seeds/tlds.yaml references unknown country %q (seed a country-owning topic first)", e.Country)
		}
		byISO[e.Country] = c
	}

	// The tld fact per country — the single source of truth item payloads
	// cache (consistency asserted by the integration test).
	factValues := make([]engine.FactValue, 0, len(sf.TLDs))
	for _, e := range sf.TLDs {
		val := e.TLD
		factValues = append(factValues, engine.FactValue{CountryISO: e.Country, Text: &val, Source: "seeds/tlds.yaml"})
	}
	tldDef := engine.FactDef{Key: FactKeyTLD, Label: "Country-code TLD", ValueType: "text", Cardinality: "single", Dataset: "seeds/tlds.yaml"}
	if err := engine.SeedCountryFacts(ctx, store, tldDef, factValues, byISO); err != nil {
		return fmt.Errorf("tld: %w", err)
	}

	// One item per country, payload caching everything the generator needs
	// (see itemPayload). The same item set feeds both direction-topics — the
	// two directions differ only in how their descriptor's Parse maps the
	// payload, not in the payload itself.
	items := make([]engine.ItemSeed, 0, len(sf.TLDs))
	for _, e := range sf.TLDs {
		c := byISO[e.Country]
		items = append(items, engine.ItemSeed{
			Key:   e.Country,
			Label: c.Name,
			Payload: itemPayload{
				Flag:  c.FlagEmoji,
				Name:  c.Name,
				ISOA2: c.ISOA2,
				ISOA3: c.ISOA3,
				TLD:   e.TLD,
				Note:  e.Note,
			},
			CountryISO: e.Country,
		})
	}

	data := engine.SeedData{Items: items, Countries: byISO, TierFromCountry: true}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: KindTLDToCountry, Topic: tldToCountryTopic()}, data); err != nil {
		return fmt.Errorf("tld: seed %s: %w", TLDToCountrySlug, err)
	}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: KindCountryToTLD, Topic: countryToTLDTopic()}, data); err != nil {
		return fmt.Errorf("tld: seed %s: %w", CountryToTLDSlug, err)
	}
	return nil
}
