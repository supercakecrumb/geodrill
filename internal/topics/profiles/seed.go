package profiles

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Topic tree constants: a "countries" root container — shared with tld's and
// capitals' trees (upserted idempotently by whichever topic seeds first, all
// three packages agree on the same slug/name/position), a "profiles"
// container child (room for the sibling main_religion/region quizzes a
// future task adds per vibe/design-country-profiles.md §3, not built this
// wave), and the one quiz-bearing "language" leaf under it.
const (
	CountriesRootSlug     = "countries"
	CountriesRootName     = "Countries"
	countriesRootPosition = 2 // cosmetic sibling ordering among root topics (languages=0, roads=1, countries=2)

	ProfilesSlug     = "profiles"
	ProfilesName     = "Profiles"
	profilesPosition = 2 // sibling of domains(0)/capitals(1) under "countries"

	// BaseTier is topics.base_tier for the language leaf. It is a required,
	// non-null column but otherwise irrelevant here: every item gets an
	// explicit tier from the shared countrytier rubric (engine's
	// TierFromCountry rule), so nothing ever inherits base_tier (mirrors
	// tld.BaseTier / capitals.BaseTier).
	BaseTier = 0

	LanguageSlug     = "language"
	LanguageName     = "Country → Language"
	languagePosition = 0 // sole child of "profiles" for now

	// The three fact_defs.key values this package owns (design §3): only
	// languages_spoken drives a quiz this wave, but all three are seeded now
	// (cheap, and needed by the future religion/region topics) per the task
	// brief.
	FactKeyRegion          = "region"
	FactKeyMainReligion    = "main_religion"
	FactKeyLanguagesSpoken = "languages_spoken"
)

// languageTopic is the country->language topic's full path (parents first).
// Defined as a function so both the generator descriptor (generator.go) and
// Seed share one definition of the tree — the shared "countries" parent is
// upserted idempotently by whichever topic (this one, tld, or capitals) seeds
// first.
func languageTopic() []engine.TopicNode {
	return []engine.TopicNode{
		{Slug: CountriesRootSlug, Name: CountriesRootName, Position: countriesRootPosition},
		{Slug: ProfilesSlug, Name: ProfilesName, Position: profilesPosition},
		{Slug: LanguageSlug, Name: LanguageName, Position: languagePosition, BaseTier: BaseTier, QuizKind: Kind, ExerciseModes: []string{"single"}, IsQuizzable: true},
	}
}

// profileSeed mirrors one entry under seeds/country_profiles.yaml
// `profiles:`. Languages is ordered primary-first (the first entry is the
// country's primary sign-visible language and this topic's CorrectAnswer);
// Region is one of the fixed 19-value taxonomy (see the seed file's header
// comment) — this package doesn't itself validate membership in that set
// (the seed file is the single source of truth for the taxonomy), but the
// seed integration test asserts the count.
type profileSeed struct {
	Country      string   `yaml:"country"`
	Languages    []string `yaml:"languages"`
	MainReligion string   `yaml:"main_religion"`
	Region       string   `yaml:"region"`
}

// profilesSeedFile is the top-level shape of seeds/country_profiles.yaml.
type profilesSeedFile struct {
	Profiles []profileSeed `yaml:"profiles"`
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

func profilesSeedPath() string  { return seedPath("country_profiles.yaml") }
func countriesSeedPath() string { return seedPath("countries.yaml") }

// loadProfilesFile reads and parses seeds/country_profiles.yaml at path.
func loadProfilesFile(path string) (profilesSeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return profilesSeedFile{}, fmt.Errorf("profiles: read country-profiles seed file %s: %w", path, err)
	}
	var sf profilesSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return profilesSeedFile{}, fmt.Errorf("profiles: parse country-profiles seed file %s: %w", path, err)
	}
	return sf, nil
}

// Seed loads seeds/country_profiles.yaml and upserts the countries/profiles
// tree, all three fact_defs (region, main_religion, languages_spoken) with
// one country_facts row per country per single-valued fact and one row per
// language for the multi-valued languages_spoken fact, and one item per
// country in the country->language topic. Countries themselves are NOT
// seeded here (roadside owns that data): every entry's iso_a2 must already
// resolve via store.GetCountryByISO, or Seed fails loudly rather than
// silently skipping. Run profiles after a country-owning topic in
// cmd/ingest.
//
// Idempotent: topics/items are keyed upserts and each fact is replaced
// per-country (engine.SeedCountryFacts), so re-running Seed after a data fix
// converges rather than duplicating or diverging (mirrors tld.Seed /
// capitals.Seed).
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, profilesSeedPath())
}

// SeedFromFile is Seed with an explicit seed-file path, so tests don't
// depend on the working directory the test binary happens to run from
// (mirrors tld.SeedFromFile / capitals.SeedFromFile).
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	sf, err := loadProfilesFile(path)
	if err != nil {
		return err
	}

	byISO := make(map[string]storage.Country, len(sf.Profiles))
	for _, e := range sf.Profiles {
		c, found, err := store.GetCountryByISO(ctx, e.Country)
		if err != nil {
			return fmt.Errorf("profiles: lookup country %s: %w", e.Country, err)
		}
		if !found {
			return fmt.Errorf("profiles: seeds/country_profiles.yaml references unknown country %q (seed a country-owning topic first)", e.Country)
		}
		byISO[e.Country] = c
	}

	// The three facts per country — the single source of truth item
	// payloads cache (consistency asserted by the integration test).
	regionValues := make([]engine.FactValue, 0, len(sf.Profiles))
	religionValues := make([]engine.FactValue, 0, len(sf.Profiles))
	var languageValues []engine.FactValue
	for _, e := range sf.Profiles {
		region := e.Region
		regionValues = append(regionValues, engine.FactValue{CountryISO: e.Country, Text: &region, Source: "seeds/country_profiles.yaml"})

		religion := e.MainReligion
		religionValues = append(religionValues, engine.FactValue{CountryISO: e.Country, Text: &religion, Source: "seeds/country_profiles.yaml"})

		for _, lang := range e.Languages {
			l := lang
			languageValues = append(languageValues, engine.FactValue{CountryISO: e.Country, Text: &l, Source: "seeds/country_profiles.yaml"})
		}
	}

	regionDef := engine.FactDef{Key: FactKeyRegion, Label: "Region", ValueType: "text", Cardinality: "single", Dataset: "seeds/country_profiles.yaml"}
	if err := engine.SeedCountryFacts(ctx, store, regionDef, regionValues, byISO); err != nil {
		return fmt.Errorf("profiles: %w", err)
	}
	religionDef := engine.FactDef{Key: FactKeyMainReligion, Label: "Main religion", ValueType: "text", Cardinality: "single", Dataset: "seeds/country_profiles.yaml"}
	if err := engine.SeedCountryFacts(ctx, store, religionDef, religionValues, byISO); err != nil {
		return fmt.Errorf("profiles: %w", err)
	}
	languagesDef := engine.FactDef{Key: FactKeyLanguagesSpoken, Label: "Languages spoken", ValueType: "text", Cardinality: "multi", Dataset: "seeds/country_profiles.yaml"}
	if err := engine.SeedCountryFacts(ctx, store, languagesDef, languageValues, byISO); err != nil {
		return fmt.Errorf("profiles: %w", err)
	}

	// One item per country, payload caching everything the generator needs
	// (see itemPayload in generator.go).
	items := make([]engine.ItemSeed, 0, len(sf.Profiles))
	for _, e := range sf.Profiles {
		c := byISO[e.Country]
		items = append(items, engine.ItemSeed{
			Key:   e.Country,
			Label: c.Name,
			Payload: itemPayload{
				Flag:      c.FlagEmoji,
				Name:      c.Name,
				ISOA2:     c.ISOA2,
				ISOA3:     c.ISOA3,
				Region:    e.Region,
				Languages: e.Languages,
			},
			CountryISO: e.Country,
		})
	}

	data := engine.SeedData{Items: items, Countries: byISO, TierFromCountry: true}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: Kind, Topic: languageTopic()}, data); err != nil {
		return fmt.Errorf("profiles: seed %s: %w", LanguageSlug, err)
	}
	return nil
}
