package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func factDefFrom(f db.FactDef) FactDef {
	return FactDef{
		ID:          f.ID,
		Key:         f.Key,
		Label:       f.Label,
		ValueType:   f.ValueType,
		Unit:        f.Unit.String,
		Cardinality: f.Cardinality,
		Dataset:     f.Dataset.String,
		CreatedAt:   tsTime(f.CreatedAt),
	}
}

// UpsertFactDef inserts or updates a fact definition, keyed on key (e.g.
// 'drives_on', 'main_religion') — the zero-DDL dataset-absorption seam
// (architecture §2.7).
func (s *Store) UpsertFactDef(ctx context.Context, key, label, valueType, unit, cardinality, dataset string) (FactDef, error) {
	f, err := s.q.UpsertFactDef(ctx, db.UpsertFactDefParams{
		Key:         key,
		Label:       label,
		ValueType:   valueType,
		Unit:        pgText(unit),
		Cardinality: cardinality,
		Dataset:     pgText(dataset),
	})
	if err != nil {
		return FactDef{}, err
	}
	return factDefFrom(f), nil
}

// GetFactDefByKey looks up a fact definition by key.
func (s *Store) GetFactDefByKey(ctx context.Context, key string) (FactDef, bool, error) {
	f, err := s.q.GetFactDefByKey(ctx, key)
	if IsNotFound(err) {
		return FactDef{}, false, nil
	}
	if err != nil {
		return FactDef{}, false, err
	}
	return factDefFrom(f), true, nil
}

// ListFactDefs returns every fact definition, alphabetically by key.
func (s *Store) ListFactDefs(ctx context.Context) ([]FactDef, error) {
	rows, err := s.q.ListFactDefs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]FactDef, len(rows))
	for i, r := range rows {
		out[i] = factDefFrom(r)
	}
	return out, nil
}

func countryFactFrom(cf db.CountryFact) CountryFact {
	return CountryFact{
		ID:         cf.ID,
		CountryID:  cf.CountryID,
		FactDefID:  cf.FactDefID,
		ValText:    stringPtrFromPg(cf.ValText),
		ValNum:     float8Ptr(cf.ValNum),
		ValBool:    boolPtr(cf.ValBool),
		Source:     cf.Source.String,
		ObservedAt: dateTime(cf.ObservedAt),
		CreatedAt:  tsTime(cf.CreatedAt),
	}
}

// InsertCountryFact inserts one typed fact value. Exactly one of
// valText/valNum/valBool must be non-nil (the DB CHECK constraint enforces
// this); the others must be nil.
func (s *Store) InsertCountryFact(ctx context.Context, countryID, factDefID uuid.UUID, valText *string, valNum *float64, valBool *bool, source string, observedAt time.Time) (CountryFact, error) {
	cf, err := s.q.InsertCountryFact(ctx, db.InsertCountryFactParams{
		CountryID:  countryID,
		FactDefID:  factDefID,
		ValText:    pgTextPtr(valText),
		ValNum:     pgFloat8(valNum),
		ValBool:    pgBool(valBool),
		Source:     pgText(source),
		ObservedAt: pgDate(observedAt),
	})
	if err != nil {
		return CountryFact{}, err
	}
	return countryFactFrom(cf), nil
}

// DeleteCountryFactsByDef clears every fact value for one country+def (used
// to replace multi-valued facts wholesale on reseed).
func (s *Store) DeleteCountryFactsByDef(ctx context.Context, countryID, factDefID uuid.UUID) error {
	return s.q.DeleteCountryFactsByDef(ctx, db.DeleteCountryFactsByDefParams{CountryID: countryID, FactDefID: factDefID})
}

// ListCountryFactsByDefKey returns every country's fact value for one
// fact_def, looked up by key (e.g. 'drives_on') — the building block for
// arbitrary-filter joins (architecture §2.7).
func (s *Store) ListCountryFactsByDefKey(ctx context.Context, factKey string) ([]CountryFact, error) {
	rows, err := s.q.ListCountryFactsByDefKey(ctx, factKey)
	if err != nil {
		return nil, err
	}
	out := make([]CountryFact, len(rows))
	for i, r := range rows {
		out[i] = countryFactFrom(r)
	}
	return out, nil
}

// ListFactsForCountry returns every fact value for one country, with each
// row's fact_def key resolved.
func (s *Store) ListFactsForCountry(ctx context.Context, countryID uuid.UUID) ([]CountryFact, error) {
	rows, err := s.q.ListFactsForCountry(ctx, countryID)
	if err != nil {
		return nil, err
	}
	out := make([]CountryFact, len(rows))
	for i, r := range rows {
		out[i] = CountryFact{
			ID:         r.ID,
			CountryID:  r.CountryID,
			FactDefID:  r.FactDefID,
			FactKey:    r.FactKey,
			ValText:    stringPtrFromPg(r.ValText),
			ValNum:     float8Ptr(r.ValNum),
			ValBool:    boolPtr(r.ValBool),
			Source:     r.Source.String,
			ObservedAt: dateTime(r.ObservedAt),
			CreatedAt:  tsTime(r.CreatedAt),
		}
	}
	return out, nil
}
