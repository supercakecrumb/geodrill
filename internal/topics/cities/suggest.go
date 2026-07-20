package cities

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SuggestCity is one city exposed for the inline-mode autocomplete index
// (internal/suggest.CityEntry is built from these by cmd/bot). It is a
// deliberately minimal projection of a seeds/cities.yaml row — only the fields
// the suggestion index needs — so this file stays stable when a later phase
// rewrites generator.go/seed.go and the topic's private payload structs.
type SuggestCity struct {
	Key     string // collision-safe "<iso2>:<slug>" seed key, e.g. "de:munich"
	Name    string // English city name, e.g. "Munich"
	Country string // ISO2 uppercase code, e.g. "DE"
}

// suggestCitySeed is a LOCAL minimal mirror of one seeds/cities.yaml entry —
// intentionally independent of citySeed in seed.go so this file does not couple
// to the cities topic's private seed shape (which a later phase may rewrite).
type suggestCitySeed struct {
	Key     string `yaml:"key"`
	Name    string `yaml:"name"`
	Country string `yaml:"country"`
}

// suggestCitiesFile is the top-level shape of seeds/cities.yaml, restricted to
// the fields SuggestCities cares about.
type suggestCitiesFile struct {
	Cities []suggestCitySeed `yaml:"cities"`
}

// SuggestCities reads seeds/cities.yaml and returns one SuggestCity per entry,
// for building the inline-mode autocomplete city-suggestion source. It reuses
// the package's citiesSeedPath helper so it resolves the seed file the same way
// whether the caller's working directory is the repo root (cmd/bot) or this
// package's own directory (go test). Read-only: it never touches the database.
func SuggestCities() ([]SuggestCity, error) {
	path := citiesSeedPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cities: read cities seed file %s: %w", path, err)
	}
	var sf suggestCitiesFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("cities: parse cities seed file %s: %w", path, err)
	}
	out := make([]SuggestCity, len(sf.Cities))
	for i, c := range sf.Cities {
		// Field-for-field conversion (Go ignores struct tags since 1.8); the
		// two types are kept separate so SuggestCity stays a stable public
		// projection even if the local seed shape gains fields later.
		out[i] = SuggestCity(c)
	}
	return out, nil
}
