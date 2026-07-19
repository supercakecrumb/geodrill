package engine

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// CountrySeed mirrors one entry under seeds/countries.yaml `countries:` —
// the repo-wide country dataset every country-linked topic seeds from.
// Optional fields (iso_a3, numeric, official_name, parent) are "" when the
// yaml has them null (subdivisions without their own alpha-3/numeric code).
type CountrySeed struct {
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
	Countries []CountrySeed `yaml:"countries"`
}

// LoadCountriesFile reads and parses a seeds/countries.yaml at path.
func LoadCountriesFile(path string) ([]CountrySeed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("engine: read countries seed file %s: %w", path, err)
	}
	var sf countriesSeedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("engine: parse countries seed file %s: %w", path, err)
	}
	return sf.Countries, nil
}

// SeedCountries upserts every country in entries, resolving GB-style
// subdivision parent links in a second pass so the result never depends on
// entries being ordered parent-before-child in the yaml (architecture §2.6).
// Returns the countries keyed by iso_a2 — the lookup Seed and
// SeedCountryFacts resolve against.
func SeedCountries(ctx context.Context, store SeedStore, entries []CountrySeed) (map[string]storage.Country, error) {
	byISO := make(map[string]storage.Country, len(entries))

	// Pass 1: upsert every country with no parent link yet. This guarantees
	// every iso_a2 referenced by a `parent:` field exists in byISO before
	// pass 2 resolves it.
	for _, e := range entries {
		c, err := store.UpsertCountry(ctx, countryFromSeed(e, nil))
		if err != nil {
			return nil, fmt.Errorf("engine: upsert country %s: %w", e.ISOA2, err)
		}
		byISO[e.ISOA2] = c
	}

	// Pass 2: re-upsert subdivisions with their resolved parent_country_id.
	for _, e := range entries {
		if e.Parent == "" {
			continue
		}
		parent, ok := byISO[e.Parent]
		if !ok {
			return nil, fmt.Errorf("engine: country %s references unknown parent %q", e.ISOA2, e.Parent)
		}
		parentID := parent.ID
		c, err := store.UpsertCountry(ctx, countryFromSeed(e, &parentID))
		if err != nil {
			return nil, fmt.Errorf("engine: link parent for country %s: %w", e.ISOA2, err)
		}
		byISO[e.ISOA2] = c
	}

	return byISO, nil
}

func countryFromSeed(e CountrySeed, parentID *uuid.UUID) storage.Country {
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

// FactDef declares one fact_defs row a topic owns (architecture §2.7), e.g.
// roadside's drives_on.
type FactDef struct {
	Key         string
	Label       string
	ValueType   string // "text"|"number"|"bool"
	Unit        string
	Cardinality string // "single"|"multi"
	Dataset     string
}

// FactValue is one country_facts value to write. Exactly one of
// Text/Num/Bool must be non-nil (the DB CHECK enforces it on insert).
type FactValue struct {
	CountryISO string
	Text       *string
	Num        *float64
	Bool       *bool
	Source     string
}

// SeedCountryFacts upserts def and replaces every referenced country's
// values for it with the given ones. Idempotent by construction:
// country_facts has no unique constraint on (country_id, fact_def_id) — it
// is shared with genuinely multi-valued facts — so each referenced
// country's values for this def are deleted once, then all fresh values
// inserted, converging on exactly the given rows on every re-run.
func SeedCountryFacts(ctx context.Context, store SeedStore, def FactDef, values []FactValue, countries map[string]storage.Country) error {
	fd, err := store.UpsertFactDef(ctx, def.Key, def.Label, def.ValueType, def.Unit, def.Cardinality, def.Dataset)
	if err != nil {
		return fmt.Errorf("engine: upsert fact def %q: %w", def.Key, err)
	}

	cleared := make(map[uuid.UUID]bool, len(values))
	for _, v := range values {
		c, ok := countries[v.CountryISO]
		if !ok {
			return fmt.Errorf("engine: fact %q references unknown country %q", def.Key, v.CountryISO)
		}
		if !cleared[c.ID] {
			if err := store.DeleteCountryFactsByDef(ctx, c.ID, fd.ID); err != nil {
				return fmt.Errorf("engine: clear %q facts for %s: %w", def.Key, v.CountryISO, err)
			}
			cleared[c.ID] = true
		}
		if _, err := store.InsertCountryFact(ctx, c.ID, fd.ID, v.Text, v.Num, v.Bool, v.Source, time.Time{}); err != nil {
			return fmt.Errorf("engine: insert %q fact for %s: %w", def.Key, v.CountryISO, err)
		}
	}
	return nil
}
