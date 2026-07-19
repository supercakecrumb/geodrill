package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// ── fake SeedStore ─────────────────────────────────────────────────────────

type topicCall struct {
	ParentID      *uuid.UUID
	Slug, Name    string
	Position      int
	BaseTier      int16
	QuizKind      string
	ExerciseModes []string
	IsQuizzable   bool
	Config        string
}

type itemCall struct {
	TopicID   uuid.UUID
	Key       string
	Label     string
	Tier      *int16
	Payload   string
	CountryID *uuid.UUID
	Position  int
	Active    bool
}

type factCall struct {
	CountryID uuid.UUID
	DefID     uuid.UUID
	Text      *string
	Num       *float64
	Bool      *bool
	Source    string
}

type fakeStore struct {
	topics       []topicCall
	topicIDs     map[string]uuid.UUID // slug -> stable id
	items        []itemCall
	countryCalls []storage.Country
	countryIDs   map[string]uuid.UUID // iso_a2 -> stable id
	factDefID    uuid.UUID
	deletes      []uuid.UUID // country ids cleared
	facts        []factCall
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		topicIDs:   map[string]uuid.UUID{},
		countryIDs: map[string]uuid.UUID{},
		factDefID:  uuid.New(),
	}
}

func (f *fakeStore) UpsertTopic(_ context.Context, parentID *uuid.UUID, slug, name string, position int, baseTier int16, quizKind string, exerciseModes []string, isQuizzable bool, config []byte) (storage.Topic, error) {
	f.topics = append(f.topics, topicCall{parentID, slug, name, position, baseTier, quizKind, exerciseModes, isQuizzable, string(config)})
	id, ok := f.topicIDs[slug]
	if !ok {
		id = uuid.New()
		f.topicIDs[slug] = id
	}
	return storage.Topic{ID: id, Slug: slug}, nil
}

func (f *fakeStore) UpsertItem(_ context.Context, topicID uuid.UUID, key, label string, tier *int16, payload []byte, countryID *uuid.UUID, position int, active bool) (storage.Item, error) {
	f.items = append(f.items, itemCall{topicID, key, label, tier, string(payload), countryID, position, active})
	return storage.Item{ID: uuid.New(), TopicID: topicID, Key: key}, nil
}

func (f *fakeStore) UpsertCountry(_ context.Context, c storage.Country) (storage.Country, error) {
	f.countryCalls = append(f.countryCalls, c)
	id, ok := f.countryIDs[c.ISOA2]
	if !ok {
		id = uuid.New()
		f.countryIDs[c.ISOA2] = id
	}
	c.ID = id
	return c, nil
}

func (f *fakeStore) UpsertFactDef(_ context.Context, key, label, valueType, unit, cardinality, dataset string) (storage.FactDef, error) {
	return storage.FactDef{ID: f.factDefID, Key: key}, nil
}

func (f *fakeStore) DeleteCountryFactsByDef(_ context.Context, countryID, _ uuid.UUID) error {
	f.deletes = append(f.deletes, countryID)
	return nil
}

func (f *fakeStore) InsertCountryFact(_ context.Context, countryID, factDefID uuid.UUID, valText *string, valNum *float64, valBool *bool, source string, _ time.Time) (storage.CountryFact, error) {
	f.facts = append(f.facts, factCall{countryID, factDefID, valText, valNum, valBool, source})
	return storage.CountryFact{}, nil
}

var _ SeedStore = (*fakeStore)(nil)

// ── Seed ───────────────────────────────────────────────────────────────────

func seedDescriptor() Descriptor {
	return Descriptor{
		QuizKind: "test_kind",
		Topic: []TopicNode{
			{Slug: "root", Name: "Root"},
			{Slug: "mid", Name: "Mid", Position: 1},
			{Slug: "leaf", Name: "Leaf", Position: 2, BaseTier: 2, QuizKind: "test_kind", ExerciseModes: []string{"single", "text"}, IsQuizzable: true},
		},
	}
}

func TestSeed_TopicPathParentsFirst(t *testing.T) {
	f := newFakeStore()
	if err := Seed(context.Background(), f, seedDescriptor(), SeedData{}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(f.topics) != 3 {
		t.Fatalf("topic upserts = %d, want 3", len(f.topics))
	}
	if f.topics[0].ParentID != nil {
		t.Fatalf("root upserted with a parent: %+v", f.topics[0])
	}
	if f.topics[1].ParentID == nil || *f.topics[1].ParentID != f.topicIDs["root"] {
		t.Fatalf("mid's parent = %v, want root's id", f.topics[1].ParentID)
	}
	if f.topics[2].ParentID == nil || *f.topics[2].ParentID != f.topicIDs["mid"] {
		t.Fatalf("leaf's parent = %v, want mid's id", f.topics[2].ParentID)
	}

	// Container defaults for parents; explicit values for the leaf.
	for _, parent := range f.topics[:2] {
		if parent.QuizKind != "container" || parent.IsQuizzable {
			t.Fatalf("parent node should default to a non-quizzable container: %+v", parent)
		}
		if !reflect.DeepEqual(parent.ExerciseModes, []string{"single"}) {
			t.Fatalf("parent modes = %v, want default [single]", parent.ExerciseModes)
		}
		if parent.Config != "{}" {
			t.Fatalf("parent config = %q, want {}", parent.Config)
		}
	}
	leaf := f.topics[2]
	if leaf.QuizKind != "test_kind" || !leaf.IsQuizzable || leaf.BaseTier != 2 || leaf.Position != 2 {
		t.Fatalf("leaf upsert = %+v", leaf)
	}
	if !reflect.DeepEqual(leaf.ExerciseModes, []string{"single", "text"}) {
		t.Fatalf("leaf modes = %v", leaf.ExerciseModes)
	}
}

func TestSeed_Items(t *testing.T) {
	f := newFakeStore()
	countries := map[string]storage.Country{
		"US": {ID: uuid.New(), ISOA2: "US", UNMember: true, GGCoverage: true},
		"PL": {ID: uuid.New(), ISOA2: "PL", UNMember: true, GGCoverage: true},
	}
	tier4 := int16(4)
	type payload struct {
		A string `json:"a"`
	}
	data := SeedData{
		Items: []ItemSeed{
			{Key: "plain", Label: "Plain", Payload: payload{A: "x"}},
			{Key: "US", Label: "United States", CountryISO: "US"},
			{Key: "PL", Label: "Poland", CountryISO: "PL"},
			{Key: "override", Label: "Override", Tier: &tier4, CountryISO: "US"},
		},
		Countries:       countries,
		TierFromCountry: true,
	}
	if err := Seed(context.Background(), f, seedDescriptor(), data); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(f.items) != 4 {
		t.Fatalf("item upserts = %d, want 4", len(f.items))
	}
	leafID := f.topicIDs["leaf"]
	for i, it := range f.items {
		if it.TopicID != leafID {
			t.Fatalf("item %d attached to topic %v, want the leaf", i, it.TopicID)
		}
		if it.Position != i {
			t.Fatalf("item %d position = %d, want slice order", i, it.Position)
		}
		if !it.Active {
			t.Fatalf("item %d not active", i)
		}
	}

	var p payload
	if err := json.Unmarshal([]byte(f.items[0].Payload), &p); err != nil || p.A != "x" {
		t.Fatalf("payload round-trip = %q err=%v", f.items[0].Payload, err)
	}
	if f.items[0].Tier != nil || f.items[0].CountryID != nil {
		t.Fatalf("plain item should have nil tier and no country: %+v", f.items[0])
	}

	// TierFromCountry: US is rubric tier 0, PL (UN member with coverage) tier 2.
	if f.items[1].Tier == nil || *f.items[1].Tier != 0 {
		t.Fatalf("US tier = %v, want 0 (countrytier rubric)", f.items[1].Tier)
	}
	if f.items[1].CountryID == nil || *f.items[1].CountryID != countries["US"].ID {
		t.Fatalf("US country_id = %v", f.items[1].CountryID)
	}
	if f.items[2].Tier == nil || *f.items[2].Tier != 2 {
		t.Fatalf("PL tier = %v, want 2 (countrytier rubric)", f.items[2].Tier)
	}
	// Explicit per-item tier wins over the country rubric.
	if f.items[3].Tier == nil || *f.items[3].Tier != 4 {
		t.Fatalf("override tier = %v, want the explicit 4", f.items[3].Tier)
	}
}

func TestSeed_UnknownCountry(t *testing.T) {
	f := newFakeStore()
	data := SeedData{Items: []ItemSeed{{Key: "XX", Label: "Nowhere", CountryISO: "XX"}}}
	err := Seed(context.Background(), f, seedDescriptor(), data)
	if err == nil || !strings.Contains(err.Error(), "unknown country") {
		t.Fatalf("err = %v, want unknown-country error", err)
	}
}

func TestSeed_EmptyPath(t *testing.T) {
	if err := Seed(context.Background(), newFakeStore(), Descriptor{QuizKind: "x"}, SeedData{}); err == nil {
		t.Fatalf("expected error for a descriptor with no topic path")
	}
}

// ── SeedCountries ──────────────────────────────────────────────────────────

func TestSeedCountries_ParentLinking(t *testing.T) {
	f := newFakeStore()
	entries := []CountrySeed{
		// Child listed BEFORE its parent: linking must still resolve.
		{ISOA2: "GB-ENG", Name: "England", Parent: "GB", Subdivision: true},
		{ISOA2: "GB", ISOA3: "GBR", Name: "United Kingdom", UNMember: true, GGCoverage: true},
	}
	byISO, err := SeedCountries(context.Background(), f, entries)
	if err != nil {
		t.Fatalf("SeedCountries: %v", err)
	}
	if len(byISO) != 2 {
		t.Fatalf("byISO size = %d, want 2", len(byISO))
	}
	// Pass 1 upserts both without parents, pass 2 re-upserts the subdivision.
	if len(f.countryCalls) != 3 {
		t.Fatalf("country upserts = %d, want 3 (2 + 1 relink)", len(f.countryCalls))
	}
	last := f.countryCalls[2]
	if last.ISOA2 != "GB-ENG" || last.ParentCountryID == nil || *last.ParentCountryID != f.countryIDs["GB"] {
		t.Fatalf("relink call = %+v, want GB-ENG parented to GB", last)
	}
	if byISO["GB-ENG"].ParentCountryID == nil || *byISO["GB-ENG"].ParentCountryID != byISO["GB"].ID {
		t.Fatalf("returned GB-ENG not parented: %+v", byISO["GB-ENG"])
	}
}

func TestSeedCountries_UnknownParent(t *testing.T) {
	_, err := SeedCountries(context.Background(), newFakeStore(), []CountrySeed{{ISOA2: "XX-SUB", Parent: "XX"}})
	if err == nil || !strings.Contains(err.Error(), "unknown parent") {
		t.Fatalf("err = %v, want unknown-parent error", err)
	}
}

// ── SeedCountryFacts ───────────────────────────────────────────────────────

func TestSeedCountryFacts_ClearOncePerCountry(t *testing.T) {
	f := newFakeStore()
	us := storage.Country{ID: uuid.New(), ISOA2: "US"}
	gb := storage.Country{ID: uuid.New(), ISOA2: "GB"}
	countries := map[string]storage.Country{"US": us, "GB": gb}
	v1, v2, v3 := "en", "es", "en"
	values := []FactValue{
		{CountryISO: "US", Text: &v1, Source: "s"},
		{CountryISO: "US", Text: &v2, Source: "s"}, // multi-valued: same country twice
		{CountryISO: "GB", Text: &v3, Source: "s"},
	}
	def := FactDef{Key: "languages_spoken", Label: "Languages spoken", ValueType: "text", Cardinality: "multi", Dataset: "test"}
	if err := SeedCountryFacts(context.Background(), f, def, values, countries); err != nil {
		t.Fatalf("SeedCountryFacts: %v", err)
	}
	// Exactly one clear per referenced country — a reseed must never delete
	// a value inserted earlier in the same run.
	if len(f.deletes) != 2 {
		t.Fatalf("deletes = %d, want 2 (once per country)", len(f.deletes))
	}
	if len(f.facts) != 3 {
		t.Fatalf("inserts = %d, want 3", len(f.facts))
	}
	if f.facts[0].CountryID != us.ID || *f.facts[0].Text != "en" || f.facts[1].CountryID != us.ID || *f.facts[1].Text != "es" {
		t.Fatalf("US fact inserts wrong: %+v", f.facts[:2])
	}
}

func TestSeedCountryFacts_UnknownCountry(t *testing.T) {
	v := "x"
	err := SeedCountryFacts(context.Background(), newFakeStore(), FactDef{Key: "k"}, []FactValue{{CountryISO: "XX", Text: &v}}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown country") {
		t.Fatalf("err = %v, want unknown-country error", err)
	}
}
