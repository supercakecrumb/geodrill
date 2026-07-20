package cities

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/citymap"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Topic tree constants. Cities gets its OWN root — per vibe/design-cities.md
// §3, "a city is not a country attribute, so it doesn't belong under the
// countries root shared by profiles/TLDs/capitals" — with one quiz-bearing
// leaf under it (the map-based city-recognition question).
const (
	RootSlug     = "cities"
	RootName     = "Cities"
	rootPosition = 3 // sibling ordering among root topics (languages=0, roads=1, countries=2, cities=3)

	// BaseTier is topics.base_tier for the leaf. It is a required, non-null
	// column but otherwise irrelevant here: every item gets an explicit
	// per-city, population-banded tier (seeds/cities.yaml's tier field, set
	// on engine.ItemSeed.Tier), so nothing ever inherits base_tier (mirrors
	// tld.BaseTier / capitals.BaseTier).
	BaseTier = 0

	LeafSlug     = "city-on-map"
	LeafName     = "City on the Map"
	leafPosition = 0

	// legacyLeafSlug/legacyLeafPath name the pre-cutover leaf
	// (cities/city-to-country). They survive ONLY inside migrateLegacyTopic,
	// which renames that row in place (vibe/design-cities-on-map.md §7) so
	// engine.Seed converges the renamed leaf rather than orphaning its items
	// under a second slug.
	legacyLeafSlug = "city-to-country"
	legacyLeafPath = RootSlug + "/" + legacyLeafSlug
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
// collision-safe "<iso2>:<slug>" identifier used as items.key. Population is a
// raw integer (NOT thousands) and drives both the population-descending
// ordering that sets items.position and, via Tier, the item's own items.tier.
// The geo fields (Lat/Lng/Region/Elevation/GeonameID/AltNames) back the map
// image and the intro caption; any of them may be absent for a given city
// (cmd/citygen omits what GeoNames couldn't resolve).
type citySeed struct {
	Key        string   `yaml:"key"`
	Name       string   `yaml:"name"`
	Country    string   `yaml:"country"`
	Population int64    `yaml:"population"`
	Tier       int16    `yaml:"tier"`
	Lat        float64  `yaml:"lat"`
	Lng        float64  `yaml:"lng"`
	Region     string   `yaml:"region"`
	Elevation  *int     `yaml:"elevation"`
	GeonameID  string   `yaml:"geoname_id"`
	AltNames   []string `yaml:"alt_names"`
}

// citiesSeedFile is the top-level shape of seeds/cities.yaml.
type citiesSeedFile struct {
	Cities []citySeed `yaml:"cities"`
}

// cityFactSeed mirrors one entry under seeds/city_facts.yaml `facts:` — only
// the fields the intro caption uses (the file carries more, e.g. source_title
// / wikidata / retrieved, which this loader ignores).
type cityFactSeed struct {
	Key       string `yaml:"key"`
	Blurb     string `yaml:"blurb"`
	SourceURL string `yaml:"source_url"`
}

// cityFactsFile is the top-level shape of seeds/city_facts.yaml.
type cityFactsFile struct {
	Facts []cityFactSeed `yaml:"facts"`
}

// seedPath resolves a path under seeds/ relative to this source file, so both
// Seed and loadLookupTables behave the same whether the caller's working
// directory is the repo root (cmd/bot, cmd/ingest) or this package's own
// directory (`go test` runs with cwd = package dir) — mirrors tld.seedPath /
// capitals.seedPath.
func seedPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "seeds", name)
}

func citiesSeedPath() string    { return seedPath("cities.yaml") }
func cityFactsSeedPath() string { return seedPath("city_facts.yaml") }

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

// loadCityFacts reads seeds/city_facts.yaml and returns key -> fact. Facts are
// an enhancement, not a hard dependency: a MISSING file yields an empty map
// and a nil error, so a fresh checkout without the scraped file still seeds
// (captions just degrade to no-fact). A present-but-malformed file errors.
func loadCityFacts(path string) (map[string]cityFactSeed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]cityFactSeed{}, nil
		}
		return nil, fmt.Errorf("cities: read city facts file %s: %w", path, err)
	}
	var ff cityFactsFile
	if err := yaml.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("cities: parse city facts file %s: %w", path, err)
	}
	out := make(map[string]cityFactSeed, len(ff.Facts))
	for _, f := range ff.Facts {
		out[f.Key] = f
	}
	return out, nil
}

// syncedMapImage returns the city's map-image filename when its garage:// ref
// is present in the synced set (registered in media_files), else "" — the
// presence-at-seed-time decision that keeps the generator IO-free.
func syncedMapImage(key string, synced map[string]bool) string {
	file := citymap.ImageFileName(key)
	if synced[MediaRootRef+"/"+file] {
		return file
	}
	return ""
}

// factFor returns the scraped blurb + source URL for a city, but ONLY for
// tier 0-2 cities (the intro-card fact block is a big-cities-only enhancement,
// vibe/design-cities-on-map.md §2/§7); higher tiers, or cities without a
// scraped entry, get no fact.
func factFor(tier int16, key string, facts map[string]cityFactSeed) (blurb, url string) {
	if tier > 2 {
		return "", ""
	}
	if f, ok := facts[key]; ok {
		return f.Blurb, f.SourceURL
	}
	return "", ""
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

// Seed loads seeds/cities.yaml and upserts the cities/city-on-map tree plus
// one item per city, positioned by population rank (biggest first). Countries
// themselves are NOT seeded here (roadside owns that data): every entry's
// country must already resolve via store.GetCountryByISO, or Seed fails
// loudly. Run cities after a country-owning topic in cmd/ingest.
//
// Idempotent: the one-time legacy rename+reset (migrateLegacyTopic) is keyed
// on the legacy path still existing, and topics/items are keyed upserts, so
// re-running Seed converges rather than duplicating or diverging.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFile(ctx, store, citiesSeedPath())
}

// SeedFromFile is Seed with an explicit seed-file path, so tests don't depend
// on the working directory the test binary happens to run from (mirrors
// tld.SeedFromFile / capitals.SeedFromFile).
func SeedFromFile(ctx context.Context, store *storage.Store, path string) error {
	// One-time in-place migration FIRST: rename the legacy city-to-country
	// leaf to city-on-map and reset per-user progress, so the engine.Seed
	// below converges the renamed row (vibe/design-cities-on-map.md §7).
	if err := migrateLegacyTopic(ctx, store); err != nil {
		return err
	}

	sf, err := loadCitiesFile(path)
	if err != nil {
		return err
	}
	facts, err := loadCityFacts(cityFactsSeedPath())
	if err != nil {
		return err
	}

	// Presence-at-seed-time: the set of city-map images already synced to
	// Garage + registered in media_files, keyed by their garage:// ref. A
	// city's payload gets map_image only when its ref is in this set; a
	// re-seed after syncing more images fills in more map_image values.
	refList, err := store.ListMediaLocalPathsByPrefix(ctx, MediaRootRef+"/")
	if err != nil {
		return fmt.Errorf("cities: list synced city-map refs: %w", err)
	}
	synced := make(map[string]bool, len(refList))
	for _, r := range refList {
		synced[r] = true
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
	// itemPayload). Position = slice index (engine.Seed's convention), so the
	// population-descending sort above is what actually sets items.position —
	// biggest city first.
	items := make([]engine.ItemSeed, 0, len(entries))
	for _, e := range entries {
		c := byISO[e.Country]
		tier := e.Tier // local copy — avoid aliasing the loop variable's address
		mapImage := syncedMapImage(e.Key, synced)
		fact, factURL := factFor(e.Tier, e.Key, facts)

		items = append(items, engine.ItemSeed{
			Key:   e.Key,
			Label: e.Name,
			Tier:  &tier,
			Payload: itemPayload{
				Key:         e.Key,
				CityName:    e.Name,
				Flag:        c.FlagEmoji,
				CountryName: c.Name,
				ISOA2:       c.ISOA2,
				ISOA3:       c.ISOA3,
				Lat:         e.Lat,
				Lng:         e.Lng,
				Region:      e.Region,
				Population:  e.Population,
				ElevationM:  e.Elevation,
				MapImage:    mapImage,
				Fact:        fact,
				FactURL:     factURL,
			},
			// CountryISO resolves country_id via SeedData.Countries. It no
			// longer drives tiering — Tier above always wins (engine.Seed only
			// falls back to TierFromCountry when Tier is nil), and
			// TierFromCountry is off below regardless.
			CountryISO: e.Country,
		})
	}

	data := engine.SeedData{Items: items, Countries: byISO, TierFromCountry: false}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: Kind, Topic: cityTopic()}, data); err != nil {
		return fmt.Errorf("cities: seed %s: %w", LeafSlug, err)
	}
	return nil
}

// migrateLegacyTopic performs the one-time in-place cutover from the legacy
// cities/city-to-country leaf to cities/city-on-map
// (vibe/design-cities-on-map.md §7): it renames the topic row (preserving its
// id and every item/exercise/review reference) and resets per-user progress so
// every city is re-introduced biggest-first. Idempotent by construction —
// keyed on the legacy path still existing, so a fresh DB or an already-migrated
// one is a no-op.
//
// Deliberately does NOT recompute user_tier_progress: after the reset the
// cache is stale-HIGH, which only keeps tiers unlocked (the permissive
// direction) and self-heals on each user's next answer; recomputing here would
// be heavy and would wrongly re-lock tiers users earned partly through cities.
func migrateLegacyTopic(ctx context.Context, store *storage.Store) error {
	legacy, found, err := store.GetTopicByPath(ctx, legacyLeafPath)
	if err != nil {
		return fmt.Errorf("cities: look up legacy topic %q: %w", legacyLeafPath, err)
	}
	if !found {
		return nil // fresh DB or already migrated
	}

	return store.WithTxStore(ctx, func(tx *storage.Store) error {
		openExercises, err := tx.DeleteOpenExercisesByTopic(ctx, legacy.ID)
		if err != nil {
			return fmt.Errorf("cities: delete open exercises for legacy topic: %w", err)
		}
		openIntros, err := tx.DeleteOpenIntroductionsByTopic(ctx, legacy.ID)
		if err != nil {
			return fmt.Errorf("cities: delete open introductions for legacy topic: %w", err)
		}
		userItems, err := tx.DeleteUserItemsByTopic(ctx, legacy.ID)
		if err != nil {
			return fmt.Errorf("cities: delete user_items for legacy topic: %w", err)
		}
		if err := tx.RenameTopic(ctx, legacy.ID, LeafSlug, LeafName); err != nil {
			return fmt.Errorf("cities: rename legacy topic: %w", err)
		}
		slog.Info("cities: migrated legacy topic in place",
			"from", legacyLeafPath, "to", RootSlug+"/"+LeafSlug,
			"open_exercises_deleted", openExercises,
			"open_introductions_deleted", openIntros,
			"user_items_deleted", userItems)
		return nil
	})
}
