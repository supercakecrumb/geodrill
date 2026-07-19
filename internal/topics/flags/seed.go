package flags

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// Topic tree constants (design §2): flags gets its OWN root (mirrors
// cities' "a [flag] is not shared with the countries root" choice — flags
// isn't a country-attribute quiz like tld/capitals/profiles, it's a single
// perceptual-recognition topic), one quiz-bearing leaf under it.
const (
	RootSlug     = "flags"
	RootName     = "Flags"
	rootPosition = 4 // sibling ordering among root topics (languages=0, roads=1, countries=2, cities=3, flags=4)

	// BaseTier is topics.base_tier for the leaf. It is a required, non-null
	// column but otherwise irrelevant here: every item gets an explicit tier
	// from tierFor (this package's countrytier wrapper), so nothing ever
	// inherits base_tier (mirrors tld.BaseTier / cities.BaseTier).
	BaseTier = 0

	LeafSlug = "guess-the-flag"
	LeafName = "Guess the Flag"

	leafPosition = 0
)

// flagsTopic is the topic's full path (parents first), shared by the
// generator descriptor (generator.go) and Seed (mirrors tld/capitals/cities'
// *Topic() functions). Both item shapes (single + confusable-group) share
// this ONE topic/quiz_kind — mirrors specialchars' single/subgroup split —
// so exercise_modes lists every mode either shape ever builds under:
// "autocomplete" (ModeText, single items' main mode) and "set" (ModeSet,
// confusable-group items' only mode). modeRotationOrder
// (internal/study.buildExerciseForItem) tries both per turn and keeps
// whichever one actually builds for that item's shape, so listing both here
// is what makes each shape land on its own intended mode (see generator.go's
// package doc).
func flagsTopic() []engine.TopicNode {
	return []engine.TopicNode{
		{Slug: RootSlug, Name: RootName, Position: rootPosition},
		{Slug: LeafSlug, Name: LeafName, Position: leafPosition, BaseTier: BaseTier, QuizKind: Kind, ExerciseModes: []string{"autocomplete", "set"}, IsQuizzable: true},
	}
}

// tierFor is this package's countrytier wrapper (design §7): subdivisions
// have no UN membership at all, so the shared rubric alone would put every
// GB-* subdivision at tier 4 — flags instead pins them at tier 3
// (subdivisions are a step up from ordinary tier-2 UN members with
// coverage, but far more recognizable than truly obscure tier-4
// dependencies/historical flags), short-circuiting before the shared
// helper ever runs.
func tierFor(c storage.Country) int16 {
	if c.IsSubdivision {
		return 3
	}
	return countrytier.Tier(c.ISOA2, c.UNMember, c.GGCoverage)
}

// flagSeed mirrors one entry under seeds/flags.yaml `flags:` (an ordinary
// single item).
type flagSeed struct {
	Country string `yaml:"country"` // iso_a2, resolved against an already-seeded storage.Country
	Image   string `yaml:"image"`   // bare filename under the media root, e.g. "fr.png"
}

// confusableGroupSeed mirrors one entry under seeds/flags.yaml
// `confusable_groups:` (design §3/§5a).
type confusableGroupSeed struct {
	Group     string   `yaml:"group"` // stable machine key (unused as items.key material directly — see Seed)
	Countries []string `yaml:"countries"`
	Images    []string `yaml:"images"` // same order/length as Countries
	Label     string   `yaml:"label"`
}

// flagsSeedFile is the top-level shape of seeds/flags.yaml.
type flagsSeedFile struct {
	Flags            []flagSeed            `yaml:"flags"`
	ConfusableGroups []confusableGroupSeed `yaml:"confusable_groups"`
}

// seedPath resolves a path under seeds/ relative to this source file, so
// both Seed and loadLookupTables behave the same whether the caller's
// working directory is the repo root (cmd/bot, cmd/ingest) or this
// package's own directory (`go test` runs with cwd = package dir) — mirrors
// tld.seedPath / capitals.seedPath / cities.seedPath.
func seedPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "seeds", name)
}

func flagsSeedPath() string     { return seedPath("flags.yaml") }
func countriesSeedPath() string { return seedPath("countries.yaml") }

// loadFlagsFile reads and parses seeds/flags.yaml at path.
func loadFlagsFile(path string) (flagsSeedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return flagsSeedFile{}, fmt.Errorf("flags: read flags seed file %s: %w", path, err)
	}
	var sf flagsSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return flagsSeedFile{}, fmt.Errorf("flags: parse flags seed file %s: %w", path, err)
	}
	return sf, nil
}

// Seed loads seeds/flags.yaml and upserts the flags/guess-the-flag tree plus
// one item per single flag and one item per confusable group. Countries
// themselves are NOT seeded here (roadside owns that data): every entry's
// country must already resolve via store.GetCountryByISO, or Seed fails
// loudly rather than silently skipping. Run flags after a country-owning
// topic in cmd/ingest.
//
// Idempotent: topics/items are keyed upserts (mirrors tld.Seed /
// capitals.Seed / cities.Seed), so re-running Seed after a data fix
// converges rather than duplicating or diverging.
func Seed(ctx context.Context, store *storage.Store) error {
	return SeedFromFiles(ctx, store, countriesSeedPath(), flagsSeedPath())
}

// SeedFromFiles is Seed with explicit seed-file paths, so tests don't depend
// on the working directory the test binary happens to run from (mirrors
// roadside.SeedFromFiles).
func SeedFromFiles(ctx context.Context, store *storage.Store, countriesPath, flagsPath string) error {
	_ = countriesPath // resolved per-country via store.GetCountryByISO below, kept for signature symmetry with roadside.SeedFromFiles
	sf, err := loadFlagsFile(flagsPath)
	if err != nil {
		return err
	}

	// design §3's exclusivity rule: a country in a confusable group has no
	// standalone single item. Checked up front so a data-file regression
	// fails loudly at seed time, not just in the audit test.
	inGroup := make(map[string]bool)
	for _, g := range sf.ConfusableGroups {
		for _, c := range g.Countries {
			if inGroup[c] {
				return fmt.Errorf("flags: country %q appears in more than one confusable group", c)
			}
			inGroup[c] = true
		}
	}
	for _, e := range sf.Flags {
		if inGroup[e.Country] {
			return fmt.Errorf("flags: country %q has both a single item and a confusable-group membership (design §3 exclusivity rule)", e.Country)
		}
	}

	// Resolve every referenced country (singles + group members) against the
	// already-seeded country data.
	isoSet := make(map[string]bool, len(sf.Flags)+4*len(sf.ConfusableGroups))
	for _, e := range sf.Flags {
		isoSet[e.Country] = true
	}
	for _, g := range sf.ConfusableGroups {
		for _, c := range g.Countries {
			isoSet[c] = true
		}
	}
	byISO := make(map[string]storage.Country, len(isoSet))
	for iso := range isoSet {
		c, found, err := store.GetCountryByISO(ctx, iso)
		if err != nil {
			return fmt.Errorf("flags: lookup country %s: %w", iso, err)
		}
		if !found {
			return fmt.Errorf("flags: seeds/flags.yaml references unknown country %q (seed a country-owning topic first)", iso)
		}
		byISO[iso] = c
	}

	items := make([]engine.ItemSeed, 0, len(sf.Flags)+len(sf.ConfusableGroups))
	for _, e := range sf.Flags {
		c := byISO[e.Country]
		tier := tierFor(c)
		items = append(items, engine.ItemSeed{
			Key:   e.Country,
			Label: c.Name,
			Tier:  &tier,
			Payload: itemPayload{
				FlagEmoji:     c.FlagEmoji,
				Image:         e.Image,
				Name:          c.Name,
				ISOA2:         c.ISOA2,
				ISOA3:         c.ISOA3,
				IsSubdivision: c.IsSubdivision,
			},
			CountryISO: e.Country,
		})
	}
	for _, g := range sf.ConfusableGroups {
		canon := quiz.CanonSet(g.Countries...)

		// design §7: confusable-group items take the MAX (harder) tier
		// among their members.
		var maxTier int16
		for _, iso := range g.Countries {
			if t := tierFor(byISO[iso]); t > maxTier {
				maxTier = t
			}
		}

		items = append(items, engine.ItemSeed{
			Key:   strings.Join(canon, ","),
			Label: g.Label,
			Tier:  &maxTier,
			Payload: itemPayload{
				Countries: append([]string(nil), g.Countries...),
				Images:    append([]string(nil), g.Images...),
				Label:     g.Label,
			},
			// CountryISO deliberately left unset: a confusable-group item
			// has no single country_id (design §3) — its member countries
			// live only in the payload.
		})
	}

	data := engine.SeedData{Items: items, Countries: byISO}
	if err := engine.Seed(ctx, store, engine.Descriptor{QuizKind: Kind, Topic: flagsTopic()}, data); err != nil {
		return fmt.Errorf("flags: seed %s: %w", LeafSlug, err)
	}
	return nil
}
