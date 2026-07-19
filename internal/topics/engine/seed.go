package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/countrytier"
)

// SeedStore is the narrow slice of storage the generic seeder needs —
// declared locally (the topics-package convention) so Seed depends on an
// interface, not *storage.Store, keeping seeding unit-testable without a
// database. *storage.Store satisfies it directly.
type SeedStore interface {
	UpsertTopic(ctx context.Context, parentID *uuid.UUID, slug, name string, position int, baseTier int16, quizKind string, exerciseModes []string, isQuizzable bool, config []byte) (storage.Topic, error)
	UpsertItem(ctx context.Context, topicID uuid.UUID, key, label string, tier *int16, payload []byte, countryID *uuid.UUID, position int, active bool) (storage.Item, error)
	UpsertCountry(ctx context.Context, c storage.Country) (storage.Country, error)
	UpsertFactDef(ctx context.Context, key, label, valueType, unit, cardinality, dataset string) (storage.FactDef, error)
	DeleteCountryFactsByDef(ctx context.Context, countryID, factDefID uuid.UUID) error
	InsertCountryFact(ctx context.Context, countryID, factDefID uuid.UUID, valText *string, valNum *float64, valBool *bool, source string, observedAt time.Time) (storage.CountryFact, error)
}

// ItemSeed is one item row for Seed to upsert. Position is the item's index
// in the SeedData.Items slice (file order), Active is always true.
type ItemSeed struct {
	Key   string
	Label string

	// Tier is the explicit items.tier override; nil inherits the topic's
	// base_tier — unless CountryISO is set and SeedData.TierFromCountry is
	// on, in which case nil means "compute via countrytier".
	Tier *int16

	// Payload is marshaled to items.payload jsonb; typically the topic's
	// own payload struct (so the JSON shape is exactly what the topic's
	// Parse expects back). nil seeds {}.
	Payload any

	// CountryISO links the item to a country (items.country_id), resolved
	// against SeedData.Countries. "" = not country-linked.
	CountryISO string
}

// SeedData is the per-run payload for Seed: the items plus, for
// country-linked topics, the resolved country lookup and the tier rule.
type SeedData struct {
	Items []ItemSeed

	// Countries resolves ItemSeed.CountryISO (typically SeedCountries'
	// return). nil is fine when no item is country-linked.
	Countries map[string]storage.Country

	// TierFromCountry applies the shared countrytier rubric to every
	// country-linked item with a nil Tier (architecture §4's country
	// table): tier = countrytier.Tier(iso, un_member, gg_coverage).
	TierFromCountry bool
}

// Seed is the ONE generic topic seeder: it upserts the descriptor's topic
// path (parents first, each keyed on (parent, slug)) and then every item
// (keyed on (topic, key), position = slice order). Idempotent — pure
// upserts, so re-running after a data edit converges rather than
// duplicating rows.
func Seed(ctx context.Context, store SeedStore, d Descriptor, data SeedData) error {
	if len(d.Topic) == 0 {
		return fmt.Errorf("engine: descriptor %q has no topic path to seed", d.QuizKind)
	}

	var parentID *uuid.UUID
	var leaf storage.Topic
	for _, n := range d.Topic {
		if n.Slug == "" {
			return fmt.Errorf("engine: descriptor %q has a topic node with an empty slug", d.QuizKind)
		}
		t, err := store.UpsertTopic(ctx, parentID, n.Slug, n.Name, n.Position, n.BaseTier, n.quizKind(), n.exerciseModes(), n.IsQuizzable, n.config())
		if err != nil {
			return fmt.Errorf("engine: upsert topic %q: %w", n.Slug, err)
		}
		id := t.ID
		parentID = &id
		leaf = t
	}

	for i, it := range data.Items {
		payload := []byte(`{}`)
		if it.Payload != nil {
			raw, err := json.Marshal(it.Payload)
			if err != nil {
				return fmt.Errorf("engine: marshal payload for item %q: %w", it.Key, err)
			}
			payload = raw
		}

		tier := it.Tier
		var countryID *uuid.UUID
		if it.CountryISO != "" {
			c, ok := data.Countries[it.CountryISO]
			if !ok {
				return fmt.Errorf("engine: item %q references unknown country %q", it.Key, it.CountryISO)
			}
			id := c.ID
			countryID = &id
			if tier == nil && data.TierFromCountry {
				tv := countrytier.Tier(it.CountryISO, c.UNMember, c.GGCoverage)
				tier = &tv
			}
		}

		if _, err := store.UpsertItem(ctx, leaf.ID, it.Key, it.Label, tier, payload, countryID, i, true); err != nil {
			return fmt.Errorf("engine: upsert item %q: %w", it.Key, err)
		}
	}
	return nil
}
