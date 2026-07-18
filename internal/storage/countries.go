package storage

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func countryFrom(c db.Country) Country {
	return Country{
		ID:              c.ID,
		ISOA2:           c.IsoA2.String,
		ISOA3:           c.IsoA3.String,
		NumericCode:     c.NumericCode.String,
		Name:            c.Name,
		OfficialName:    c.OfficialName.String,
		FlagEmoji:       c.FlagEmoji.String,
		ParentCountryID: c.ParentCountryID,
		IsSubdivision:   c.IsSubdivision,
		UNMember:        c.UnMember,
		GGCoverage:      c.GgCoverage,
		CreatedAt:       tsTime(c.CreatedAt),
	}
}

// UpsertCountry inserts or updates a country, keyed on ISO alpha-2 (empty
// isoA2 always inserts a new row — see UpsertCountry query comment;
// subdivisions without an alpha-2 code should dedup by iso_a3 caller-side
// before calling this).
func (s *Store) UpsertCountry(ctx context.Context, c Country) (Country, error) {
	r, err := s.q.UpsertCountry(ctx, db.UpsertCountryParams{
		IsoA2:           pgText(c.ISOA2),
		IsoA3:           pgText(c.ISOA3),
		NumericCode:     pgText(c.NumericCode),
		Name:            c.Name,
		OfficialName:    pgText(c.OfficialName),
		FlagEmoji:       pgText(c.FlagEmoji),
		ParentCountryID: c.ParentCountryID,
		IsSubdivision:   c.IsSubdivision,
		UnMember:        c.UNMember,
		GgCoverage:      c.GGCoverage,
	})
	if err != nil {
		return Country{}, err
	}
	return countryFrom(r), nil
}

// GetCountryByISO looks up a country by ISO alpha-2 code.
func (s *Store) GetCountryByISO(ctx context.Context, isoA2 string) (Country, bool, error) {
	c, err := s.q.GetCountryByISO(ctx, pgText(isoA2))
	if IsNotFound(err) {
		return Country{}, false, nil
	}
	if err != nil {
		return Country{}, false, err
	}
	return countryFrom(c), true, nil
}

// GetCountryByISOA3 looks up a country by ISO alpha-3 code.
func (s *Store) GetCountryByISOA3(ctx context.Context, isoA3 string) (Country, bool, error) {
	c, err := s.q.GetCountryByISOA3(ctx, pgText(isoA3))
	if IsNotFound(err) {
		return Country{}, false, nil
	}
	if err != nil {
		return Country{}, false, err
	}
	return countryFrom(c), true, nil
}

// GetCountryByID looks up a country by primary key.
func (s *Store) GetCountryByID(ctx context.Context, id uuid.UUID) (Country, bool, error) {
	c, err := s.q.GetCountryByID(ctx, id)
	if IsNotFound(err) {
		return Country{}, false, nil
	}
	if err != nil {
		return Country{}, false, err
	}
	return countryFrom(c), true, nil
}

// ListCountries returns every country, alphabetically by name.
func (s *Store) ListCountries(ctx context.Context) ([]Country, error) {
	rows, err := s.q.ListCountries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Country, len(rows))
	for i, r := range rows {
		out[i] = countryFrom(r)
	}
	return out, nil
}

// ListCountriesByFlags filters by the first-class un_member/gg_coverage
// booleans (e.g. the road-side audit's "every gg_coverage country" check).
func (s *Store) ListCountriesByFlags(ctx context.Context, unMember, ggCoverage bool) ([]Country, error) {
	rows, err := s.q.ListCountriesByFlags(ctx, db.ListCountriesByFlagsParams{UnMember: unMember, GgCoverage: ggCoverage})
	if err != nil {
		return nil, err
	}
	out := make([]Country, len(rows))
	for i, r := range rows {
		out[i] = countryFrom(r)
	}
	return out, nil
}
