package roadside

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

// Topic tree constants this package seeds and generates for (architecture
// §6.2): a root "roads" container with a single quiz-bearing child,
// "which-side".
const (
	RootSlug     = "roads"
	RootName     = "Roads"
	rootPosition = 1 // cosmetic sibling ordering among root topics (e.g. languages=0, roads=1, countries=2)

	TopicSlug     = "which-side"
	TopicName     = "Which side of the road"
	topicPosition = 0 // sole child of "roads" for now

	// BaseTier is topics.base_tier for roads/which-side. It is a required,
	// non-null column but otherwise irrelevant here: every item gets an
	// explicit tier from the shared countrytier rubric (the engine's
	// TierFromCountry rule), so nothing ever inherits base_tier. 0 is the
	// neutral default other container/root topics in this codebase use.
	BaseTier = 0

	// FactKeyDrivesOn is the fact_defs.key this package owns (architecture
	// §2.7/§6.2): one 'left'|'right' text fact per country, the single
	// source of truth items.payload.side caches.
	FactKeyDrivesOn = "drives_on"
)

// roadSideSeed mirrors one entry under seeds/road_sides.yaml `road_sides:`.
type roadSideSeed struct {
	Country string `yaml:"country"` // iso_a2, matches an engine.CountrySeed.ISOA2
	Side    string `yaml:"side"`    // "left" | "right"
}

// roadSidesSeedFile is the top-level shape of seeds/road_sides.yaml.
type roadSidesSeedFile struct {
	RoadSides []roadSideSeed `yaml:"road_sides"`
}

// countriesSeedPath and roadSidesSeedPath resolve the absolute paths to this
// package's two seed files relative to this source file, so Seed behaves
// the same whether the caller's working directory is the repo root
// (cmd/bot, cmd/ingest-style tools) or this package's own directory
// (`go test` always runs with cwd set to the package directory, mirrors
// internal/topics/words' seedFilePath).
func countriesSeedPath() string { return seedPath("countries.yaml") }
func roadSidesSeedPath() string { return seedPath("road_sides.yaml") }

func seedPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "seeds", name)
}

// loadRoadSidesFile reads and parses a road_sides.yaml file at path.
func loadRoadSidesFile(path string) (roadSidesSeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return roadSidesSeedFile{}, fmt.Errorf("roadside: read road-sides seed file %s: %w", path, err)
	}
	var sf roadSidesSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return roadSidesSeedFile{}, fmt.Errorf("roadside: parse road-sides seed file %s: %w", path, err)
	}
	return sf, nil
}

// normalizeSide validates a road_sides.yaml `side` value.
func normalizeSide(raw string) (string, error) {
	switch raw {
	case "left", "right":
		return raw, nil
	default:
		return "", fmt.Errorf("invalid road side %q (want \"left\" or \"right\")", raw)
	}
}

// sideCode maps a normalized "left"/"right" fact value to the items.payload
// "side" cache code ("L"/"R", architecture §6.2).
func sideCode(side string) string {
	if side == "right" {
		return "R"
	}
	return "L"
}

// Seed loads seeds/countries.yaml and seeds/road_sides.yaml and seeds the
// countries (with GB-subdivision parent linking), the drives_on fact per
// country, and the roads/which-side topic + items (architecture §6.2) —
// all through the generic engine seeder, with per-item tiers from the
// shared countrytier rubric (engine.SeedData.TierFromCountry). Idempotent:
// countries/topics/items are keyed upserts and facts are replaced
// per-country (engine.SeedCountryFacts), so re-running Seed after a data
// fix converges rather than duplicating or diverging.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFiles(ctx, store, countriesSeedPath(), roadSidesSeedPath())
}

// SeedFromFiles is Seed with explicit seed-file paths, so tests don't depend
// on the working directory the test binary happens to run from (mirrors
// internal/topics/words.SeedFromFile).
func SeedFromFiles(ctx context.Context, store *storage.Store, countriesPath, roadSidesPath string) error {
	countries, err := engine.LoadCountriesFile(countriesPath)
	if err != nil {
		return fmt.Errorf("roadside: %w", err)
	}
	roadSides, err := loadRoadSidesFile(roadSidesPath)
	if err != nil {
		return err
	}

	byISO, err := engine.SeedCountries(ctx, store, countries)
	if err != nil {
		return fmt.Errorf("roadside: %w", err)
	}

	// The drives_on fact per country — the single source of truth the item
	// payloads cache (consistency asserted by the integration test).
	values := make([]engine.FactValue, 0, len(roadSides.RoadSides))
	sideByISO := make(map[string]string, len(roadSides.RoadSides))
	for _, e := range roadSides.RoadSides {
		if _, ok := byISO[e.Country]; !ok {
			return fmt.Errorf("roadside: road-side entry references unknown country %q", e.Country)
		}
		side, err := normalizeSide(e.Side)
		if err != nil {
			return fmt.Errorf("roadside: country %s: %w", e.Country, err)
		}
		val := side
		values = append(values, engine.FactValue{CountryISO: e.Country, Text: &val, Source: "seeds/road_sides.yaml"})
		sideByISO[e.Country] = side
	}
	drivesOn := engine.FactDef{Key: FactKeyDrivesOn, Label: "Drives on", ValueType: "text", Cardinality: "single", Dataset: "baseline"}
	if err := engine.SeedCountryFacts(ctx, store, drivesOn, values, byISO); err != nil {
		return fmt.Errorf("roadside: %w", err)
	}

	// One item per road-side entry, payload caching everything the
	// generator needs (see itemPayload in generator.go).
	items := make([]engine.ItemSeed, 0, len(roadSides.RoadSides))
	for _, e := range roadSides.RoadSides {
		country := byISO[e.Country]
		items = append(items, engine.ItemSeed{
			Key:   e.Country,
			Label: country.Name,
			Payload: itemPayload{
				Side:       sideCode(sideByISO[e.Country]),
				Flag:       country.FlagEmoji,
				Name:       country.Name,
				UNMember:   country.UNMember,
				GGCoverage: country.GGCoverage,
			},
			CountryISO: e.Country,
		})
	}
	if err := engine.Seed(ctx, store, descriptor, engine.SeedData{Items: items, Countries: byISO, TierFromCountry: true}); err != nil {
		return fmt.Errorf("roadside: %w", err)
	}
	return nil
}
