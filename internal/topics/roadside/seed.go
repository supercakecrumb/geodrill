package roadside

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
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
	// explicit tier override from countryTier below, so nothing ever
	// inherits base_tier. 0 is the neutral default other container/root
	// topics in this codebase use.
	BaseTier = 0

	// FactKeyDrivesOn is the fact_defs.key this package owns (architecture
	// §2.7/§6.2): one 'left'|'right' text fact per country, the single
	// source of truth items.payload.side caches.
	FactKeyDrivesOn = "drives_on"
)

// tier0ISO is the task-specified tier-0 set: universally-known countries
// (architecture §4 rubric, hardcoded per the task brief rather than derived).
var tier0ISO = map[string]bool{
	"US": true, "GB": true, "FR": true, "DE": true, "JP": true,
	"CA": true, "AU": true, "IT": true, "ES": true,
}

// tier1ISO is every G20 country member NOT already in tier0ISO (the task
// brief's "remaining G20 members" rule). The G20 itself also counts the EU
// as a member, but the EU is not a country, so it is excluded here.
var tier1ISO = map[string]bool{
	"AR": true, "BR": true, "CN": true, "IN": true, "ID": true,
	"MX": true, "RU": true, "SA": true, "ZA": true, "KR": true, "TR": true,
}

// countryTier implements the road-side tier rubric (architecture §4 table,
// task brief §1d): checked in this order, first match wins.
//   - tier 0: the 9 universally-known countries in tier0ISO
//   - tier 1: every other G20 member, in tier1ISO
//   - tier 2: any other UN member with GeoGuessr coverage
//   - tier 3: any other UN member without GeoGuessr coverage
//   - tier 4: everything else — territories, dependencies, subdivisions
//     (!un_member), regardless of coverage
func countryTier(iso2 string, unMember, ggCoverage bool) int16 {
	switch {
	case tier0ISO[iso2]:
		return 0
	case tier1ISO[iso2]:
		return 1
	case unMember && ggCoverage:
		return 2
	case unMember && !ggCoverage:
		return 3
	default:
		return 4
	}
}

// countrySeed mirrors one entry under seeds/countries.yaml `countries:`.
// Optional fields (iso_a3, numeric, official_name, parent) are "" when the
// yaml has them null (subdivisions without their own alpha-3/numeric code).
type countrySeed struct {
	ISOA2        string `yaml:"iso_a2"`
	ISOA3        string `yaml:"iso_a3"`
	Numeric      string `yaml:"numeric"`
	Name         string `yaml:"name"`
	OfficialName string `yaml:"official_name"`
	FlagEmoji    string `yaml:"flag_emoji"`
	Parent       string `yaml:"parent"` // iso_a2 of the parent country, e.g. GB-ENG -> "GB"
	Subdivision  bool   `yaml:"subdivision"`
	UNMember     bool   `yaml:"un_member"`
	GGCoverage   bool   `yaml:"gg_coverage"`
}

// countriesSeedFile is the top-level shape of seeds/countries.yaml.
type countriesSeedFile struct {
	Countries []countrySeed `yaml:"countries"`
}

// roadSideSeed mirrors one entry under seeds/road_sides.yaml `road_sides:`.
type roadSideSeed struct {
	Country string `yaml:"country"` // iso_a2, matches a countrySeed.ISOA2
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

// loadCountriesFile reads and parses seeds/countries.yaml at path.
func loadCountriesFile(path string) (countriesSeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return countriesSeedFile{}, fmt.Errorf("roadside: read countries seed file %s: %w", path, err)
	}
	var sf countriesSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return countriesSeedFile{}, fmt.Errorf("roadside: parse countries seed file %s: %w", path, err)
	}
	return sf, nil
}

// loadRoadSidesFile reads and parses seeds/road_sides.yaml at path.
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

// Seed loads seeds/countries.yaml and seeds/road_sides.yaml and upserts the
// countries (with GB-subdivision parent linking), the drives_on fact per
// country, and the roads/which-side topic + items (architecture §6.2).
// Idempotent: countries/topics/items are keyed upserts, and each country's
// drives_on fact is deleted then reinserted on every call, so re-running
// Seed after a data fix converges rather than duplicating or diverging.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFiles(ctx, store, countriesSeedPath(), roadSidesSeedPath())
}

// SeedFromFiles is Seed with explicit seed-file paths, so tests don't depend
// on the working directory the test binary happens to run from (mirrors
// internal/topics/words.SeedFromFile).
func SeedFromFiles(ctx context.Context, store *storage.Store, countriesPath, roadSidesPath string) error {
	countries, err := loadCountriesFile(countriesPath)
	if err != nil {
		return err
	}
	roadSides, err := loadRoadSidesFile(roadSidesPath)
	if err != nil {
		return err
	}

	byISO, err := seedCountries(ctx, store, countries.Countries)
	if err != nil {
		return err
	}

	drivesOn, err := store.UpsertFactDef(ctx, FactKeyDrivesOn, "Drives on", "text", "", "single", "baseline")
	if err != nil {
		return fmt.Errorf("roadside: upsert drives_on fact def: %w", err)
	}

	sideByISO, err := seedRoadSideFacts(ctx, store, byISO, drivesOn.ID, roadSides.RoadSides)
	if err != nil {
		return err
	}

	return seedTopicAndItems(ctx, store, byISO, sideByISO, roadSides.RoadSides)
}

// seedCountries upserts every country in entries, resolving GB-style
// subdivision parent links in a second pass so the result never depends on
// entries being ordered parent-before-child in the yaml.
func seedCountries(ctx context.Context, store *storage.Store, entries []countrySeed) (map[string]storage.Country, error) {
	byISO := make(map[string]storage.Country, len(entries))

	// Pass 1: upsert every country with no parent link yet. This guarantees
	// every iso_a2 referenced by a `parent:` field exists in byISO before
	// pass 2 resolves it.
	for _, e := range entries {
		c, err := store.UpsertCountry(ctx, countryFromSeed(e, nil))
		if err != nil {
			return nil, fmt.Errorf("roadside: upsert country %s: %w", e.ISOA2, err)
		}
		byISO[e.ISOA2] = c
	}

	// Pass 2: re-upsert subdivisions with their resolved parent_country_id
	// (architecture §2.6: GB-ENG/GB-SCT/GB-WLS -> GB).
	for _, e := range entries {
		if e.Parent == "" {
			continue
		}
		parent, ok := byISO[e.Parent]
		if !ok {
			return nil, fmt.Errorf("roadside: country %s references unknown parent %q", e.ISOA2, e.Parent)
		}
		parentID := parent.ID
		c, err := store.UpsertCountry(ctx, countryFromSeed(e, &parentID))
		if err != nil {
			return nil, fmt.Errorf("roadside: link parent for country %s: %w", e.ISOA2, err)
		}
		byISO[e.ISOA2] = c
	}

	return byISO, nil
}

func countryFromSeed(e countrySeed, parentID *uuid.UUID) storage.Country {
	return storage.Country{
		ISOA2:           e.ISOA2,
		ISOA3:           e.ISOA3,
		NumericCode:     e.Numeric,
		Name:            e.Name,
		OfficialName:    e.OfficialName,
		FlagEmoji:       e.FlagEmoji,
		ParentCountryID: parentID,
		IsSubdivision:   e.Subdivision,
		UNMember:        e.UNMember,
		GGCoverage:      e.GGCoverage,
	}
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

// seedRoadSideFacts upserts one drives_on country_facts row per entry,
// keyed by the country resolved from byISO. Returns the normalized
// "left"/"right" side per iso_a2, for seedTopicAndItems to cache into
// items.payload.
func seedRoadSideFacts(ctx context.Context, store *storage.Store, byISO map[string]storage.Country, drivesOnDefID uuid.UUID, entries []roadSideSeed) (map[string]string, error) {
	sideByISO := make(map[string]string, len(entries))
	for _, e := range entries {
		country, ok := byISO[e.Country]
		if !ok {
			return nil, fmt.Errorf("roadside: road-side entry references unknown country %q", e.Country)
		}
		side, err := normalizeSide(e.Side)
		if err != nil {
			return nil, fmt.Errorf("roadside: country %s: %w", e.Country, err)
		}

		// Idempotent: country_facts has no unique constraint on
		// (country_id, fact_def_id) — it's shared with genuinely
		// multi-valued facts — so a reseed must explicitly clear this
		// country's drives_on fact before inserting the fresh one, or it
		// would accumulate a duplicate row on every call (breaking the
		// "exactly one drives_on fact per country" invariant the
		// integration test asserts).
		if err := store.DeleteCountryFactsByDef(ctx, country.ID, drivesOnDefID); err != nil {
			return nil, fmt.Errorf("roadside: clear drives_on fact for %s: %w", e.Country, err)
		}
		val := side
		if _, err := store.InsertCountryFact(ctx, country.ID, drivesOnDefID, &val, nil, nil, "seeds/road_sides.yaml", time.Time{}); err != nil {
			return nil, fmt.Errorf("roadside: insert drives_on fact for %s: %w", e.Country, err)
		}
		sideByISO[e.Country] = side
	}
	return sideByISO, nil
}

// sideCode maps a normalized "left"/"right" fact value to the items.payload
// "side" cache code ("L"/"R", architecture §6.2).
func sideCode(side string) string {
	if side == "right" {
		return "R"
	}
	return "L"
}

// seedTopicAndItems upserts the roads/which-side topic tree and one item
// per road-side entry, with items.payload caching everything Generator
// needs so it never has to query the DB (see itemPayload in generator.go).
func seedTopicAndItems(ctx context.Context, store *storage.Store, byISO map[string]storage.Country, sideByISO map[string]string, entries []roadSideSeed) error {
	root, err := store.UpsertTopic(ctx, nil, RootSlug, RootName, rootPosition, 0, "container", []string{"single"}, false, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("roadside: upsert root topic %q: %w", RootSlug, err)
	}

	rootID := root.ID
	topic, err := store.UpsertTopic(ctx, &rootID, TopicSlug, TopicName, topicPosition, BaseTier, Kind, []string{"single"}, true, []byte(`{}`))
	if err != nil {
		return fmt.Errorf("roadside: upsert topic %q: %w", TopicSlug, err)
	}

	for i, e := range entries {
		country, ok := byISO[e.Country]
		if !ok {
			return fmt.Errorf("roadside: road-side entry references unknown country %q", e.Country)
		}
		side := sideByISO[e.Country]

		raw, err := json.Marshal(itemPayload{
			Side:       sideCode(side),
			Flag:       country.FlagEmoji,
			Name:       country.Name,
			UNMember:   country.UNMember,
			GGCoverage: country.GGCoverage,
		})
		if err != nil {
			return fmt.Errorf("roadside: marshal payload for %s: %w", e.Country, err)
		}

		tier := countryTier(e.Country, country.UNMember, country.GGCoverage)
		countryID := country.ID
		if _, err := store.UpsertItem(ctx, topic.ID, e.Country, country.Name, &tier, raw, &countryID, i, true); err != nil {
			return fmt.Errorf("roadside: upsert item %s: %w", e.Country, err)
		}
	}
	return nil
}
