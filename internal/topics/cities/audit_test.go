package cities

// Audits over the committed seed data (vibe/design-cities-on-map.md §9):
//
//   - (a) unconditional (no DB): structural invariants on seeds/cities.yaml
//     and seeds/city_facts.yaml, plus a rendered-caption length bound.
//   - (b) DB-gated (GEODRILL_TEST_DATABASE_URL + freshSchema, same fuse the
//     other cities integration tests use): pre-register a couple garage://
//     map refs, seed, and assert map_image presence tracks the registered set.
//
// WARNING: auditFreshSchema drops every table — the target DB name must
// contain "test" (auditTestDSN's safety fuse), never the live/dev DB.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/citymap"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
)

// tierForPopulation is the population->tier band from cmd/citygen
// (vibe/design-cities-on-map.md §9). Kept local so the audit is independent of
// whatever produced the committed file.
func tierForPopulation(p int64) int16 {
	switch {
	case p >= 5_000_000:
		return 0
	case p >= 2_000_000:
		return 1
	case p >= 1_000_000:
		return 2
	case p >= 500_000:
		return 3
	case p >= 200_000:
		return 4
	case p >= 75_000:
		return 5
	default:
		return 6
	}
}

// TestAuditCitiesSeed is the (a) unconditional audit — runs without a DB.
func TestAuditCitiesSeed(t *testing.T) {
	sf, err := loadCitiesFile(citiesSeedPath())
	if err != nil {
		t.Fatalf("loadCitiesFile: %v", err)
	}
	if len(sf.Cities) == 0 {
		t.Fatalf("no cities loaded")
	}

	// Country flag/name for faithful caption rendering (also confirms every
	// city's country resolves).
	countries, err := engine.LoadCountriesFile(seedPath("countries.yaml"))
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	type cc struct{ name, flag string }
	byISO := make(map[string]cc, len(countries))
	for _, c := range countries {
		byISO[c.ISOA2] = cc{c.Name, c.FlagEmoji}
	}

	facts, err := loadCityFacts(cityFactsSeedPath())
	if err != nil {
		t.Fatalf("loadCityFacts: %v", err)
	}

	seen := make(map[string]bool, len(sf.Cities))
	for _, e := range sf.Cities {
		if seen[e.Key] {
			t.Fatalf("duplicate city key %q", e.Key)
		}
		seen[e.Key] = true

		// lat/lng: absent (both 0) is fine; otherwise must be in range and not
		// exactly (0,0).
		if !(e.Lat == 0 && e.Lng == 0) {
			if e.Lat < -90 || e.Lat > 90 || e.Lng < -180 || e.Lng > 180 {
				t.Fatalf("city %q has out-of-range lat/lng (%v,%v)", e.Key, e.Lat, e.Lng)
			}
		}

		// tier must match its population band.
		if want := tierForPopulation(e.Population); e.Tier != want {
			t.Fatalf("city %q pop=%d tier=%d, want tier %d", e.Key, e.Population, e.Tier, want)
		}

		country, ok := byISO[e.Country]
		if !ok {
			t.Fatalf("city %q references unknown country %q", e.Key, e.Country)
		}

		// Rendered caption must stay under Telegram's cap.
		fact, factURL := factFor(e.Tier, e.Key, facts)
		p := itemPayload{
			Key: e.Key, CityName: e.Name, Flag: country.flag, CountryName: country.name,
			ISOA2: e.Country, Region: e.Region, Population: e.Population,
			ElevationM: e.Elevation, Fact: fact, FactURL: factURL,
		}
		if n := utf8.RuneCountInString(introCaption(p)); n >= captionLimit {
			t.Fatalf("city %q caption rune count = %d, want < %d", e.Key, n, captionLimit)
		}
	}

	// Every facts key must exist in cities.yaml with a bounded blurb + a source.
	for _, f := range mustFacts(t) {
		if !seen[f.Key] {
			t.Fatalf("city_facts key %q not present in cities.yaml", f.Key)
		}
		if f.Blurb == "" || utf8.RuneCountInString(f.Blurb) > 400 {
			t.Fatalf("city_facts %q blurb length %d out of (0,400]", f.Key, utf8.RuneCountInString(f.Blurb))
		}
		if f.SourceURL == "" {
			t.Fatalf("city_facts %q has no source_url", f.Key)
		}
	}
}

// mustFacts reads the raw facts slice (loadCityFacts returns a map, losing
// nothing we need, but the audit iterates the file's own list to catch a key
// that maps to a city yet still fails the blurb/source checks).
func mustFacts(t *testing.T) []cityFactSeed {
	t.Helper()
	data, err := os.ReadFile(cityFactsSeedPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read facts: %v", err)
	}
	var ff cityFactsFile
	if err := yaml.Unmarshal(data, &ff); err != nil {
		t.Fatalf("parse facts: %v", err)
	}
	return ff.Facts
}

// ── (b) DB-gated presence audit ────────────────────────────────────────────

func auditTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GEODRILL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GEODRILL_TEST_DATABASE_URL to run cities audit(b)")
	}
	name := dsn
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+3:]
	}
	if i := strings.IndexByte(name, '?'); i >= 0 {
		name = name[:i]
	}
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = strings.Trim(name[i+1:], "/")
	}
	if !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("refusing to run destructive test against database %q: name must contain \"test\"", name)
	}
	return dsn
}

func auditFreshSchema(t *testing.T, dsn string) {
	t.Helper()
	url := storage.MigrateURL(dsn)
	if err := storage.MigrateUp(url); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if err := storage.MigrateDown(url); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := storage.MigrateUp(url); err != nil {
		t.Fatalf("migrate up (again): %v", err)
	}
}

// TestAuditMapImagePresence is the (b) DB-gated audit: map_image is set at seed
// time for every city that HAS coordinates (non-zero lat OR lng) and left empty
// for coordinate-less cities — the on-demand rendering gating (maps render
// lazily at first send, so no media_files pre-registration is required).
func TestAuditMapImagePresence(t *testing.T) {
	dsn := auditTestDSN(t)
	auditFreshSchema(t, dsn)

	ctx := context.Background()
	store, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if err := roadside.Seed(ctx, store); err != nil {
		t.Fatalf("seed roadside (countries): %v", err)
	}

	// Seed with NO city-map media pre-registered — map_image must still be set
	// for every city with coordinates purely from the seed data.
	if err := SeedFromFile(ctx, store, citiesSeedPath()); err != nil {
		t.Fatalf("seed cities: %v", err)
	}

	// Coordinates straight from the seed file drive the expected gating.
	sf, err := loadCitiesFile(citiesSeedPath())
	if err != nil {
		t.Fatalf("loadCitiesFile: %v", err)
	}
	coordsByKey := make(map[string][2]float64, len(sf.Cities))
	for _, e := range sf.Cities {
		coordsByKey[e.Key] = [2]float64{e.Lat, e.Lng}
	}

	topic, found, err := store.GetTopicByPath(ctx, RootSlug+"/"+LeafSlug)
	if err != nil || !found {
		t.Fatalf("get topic: found=%v err=%v", found, err)
	}
	items, err := store.ListItemsByTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}

	checkedWithMap, checkedNoMap := 0, 0
	for _, it := range items {
		var p itemPayload
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			t.Fatalf("item %s: unmarshal payload: %v", it.Key, err)
		}
		coords := coordsByKey[it.Key]
		hasCoords := !(coords[0] == 0 && coords[1] == 0)
		if hasCoords {
			want := citymap.ImageFileName(it.Key)
			if p.MapImage != want {
				t.Fatalf("city %q has coords but map_image = %q, want %q", it.Key, p.MapImage, want)
			}
			checkedWithMap++
		} else {
			if p.MapImage != "" {
				t.Fatalf("coordinate-less city %q map_image = %q, want empty", it.Key, p.MapImage)
			}
			checkedNoMap++
		}
	}
	if checkedWithMap == 0 {
		t.Fatalf("expected at least one city with coordinates to carry a map_image")
	}
	t.Logf("map_image gating: %d cities with maps, %d without", checkedWithMap, checkedNoMap)
}
