// Command citymaps batch-renders one label-free PNG map per city — a red
// marker at the city's location over a muted Natural Earth basemap with the
// city's own country highlighted — the image behind the "which city is at the
// marker?" question.
//
// It is offline and side-effect-light: it reads the Natural Earth countries
// GeoJSON (fetched by scripts/fetch-naturalearth.sh into the gitignored data/
// tree), the cities and countries seeds, and writes PNGs into -out. No DB, no
// network at render time, no S3 — a later phase uploads the -out directory.
//
// Usage:
//
//	./scripts/fetch-naturalearth.sh
//	go run ./cmd/citymaps                      # render all cities with coords
//	go run ./cmd/citymaps -only de:munich      # render a single city
//	go run ./cmd/citymaps -force               # re-render existing PNGs
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/paulmach/orb"
	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/citymap"
	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// citySeed is the minimal view of a seeds/cities.yaml entry the renderer
// needs. Lat/Lng are pointers so a city that has no coordinates yet (a
// parallel phase supplies them) is distinguishable from one at (0,0) and can
// be skipped rather than mis-rendered.
type citySeed struct {
	Key     string   `yaml:"key"`
	Name    string   `yaml:"name"`
	Country string   `yaml:"country"`
	Lat     *float64 `yaml:"lat"`
	Lng     *float64 `yaml:"lng"`
}

type citiesFile struct {
	Cities []citySeed `yaml:"cities"`
}

func main() {
	nePath := flag.String("ne", "data/naturalearth/ne_10m_admin_0_countries.geojson", "path to the Natural Earth 1:10m admin-0 countries GeoJSON")
	citiesPath := flag.String("cities", "seeds/cities.yaml", "path to seeds/cities.yaml")
	countriesPath := flag.String("countries", "seeds/countries.yaml", "path to seeds/countries.yaml (valid ISO2 set)")
	outDir := flag.String("out", "data/citymaps", "output directory for rendered PNGs")
	force := flag.Bool("force", false, "re-render even when the output PNG already exists")
	only := flag.String("only", "", "render only the city with this key (e.g. de:munich)")
	flag.Parse()

	if err := run(*nePath, *citiesPath, *countriesPath, *outDir, *force, *only); err != nil {
		log.Fatalf("citymaps: %v", err)
	}
}

func run(nePath, citiesPath, countriesPath, outDir string, force bool, only string) error {
	idx, err := citymap.LoadCountryIndex(nePath)
	if err != nil {
		return err
	}
	log.Printf("loaded %d country geometries from %s", len(idx), nePath)

	cities, err := loadCities(citiesPath)
	if err != nil {
		return err
	}

	validISO, err := loadValidISO(countriesPath)
	if err != nil {
		return err
	}

	// Select the cities we will attempt: -only narrows to one; cities without
	// coordinates are skipped with a warning (their coords come in a parallel
	// phase); cities referencing an unknown ISO2 are skipped with a warning.
	var targets []citySeed
	skippedNoCoord := 0
	for _, c := range cities {
		if only != "" && c.Key != only {
			continue
		}
		if c.Lat == nil || c.Lng == nil {
			log.Printf("SKIP %s: no coordinates yet", c.Key)
			skippedNoCoord++
			continue
		}
		if !validISO[c.Country] {
			log.Printf("SKIP %s: country %q not in countries seed", c.Key, c.Country)
			continue
		}
		targets = append(targets, c)
	}
	if only != "" && len(targets) == 0 {
		return fmt.Errorf("city %q not found (or has no coordinates) in %s", only, citiesPath)
	}

	// Fail fast if any target country has no Natural Earth polygon — better a
	// loud error than a batch of blank maps.
	want := make([]string, 0, len(targets))
	for _, c := range targets {
		want = append(want, c.Country)
	}
	if missing := citymap.AuditISOCoverage(idx, want); len(missing) > 0 {
		return fmt.Errorf("natural earth index is missing geometry for %d country code(s): %v", len(missing), missing)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir %s: %w", outDir, err)
	}

	rendered, skipped, failed := 0, 0, 0
	for _, c := range targets {
		outPath := filepath.Join(outDir, citymap.ImageFileName(c.Key))
		if !force {
			if _, err := os.Stat(outPath); err == nil {
				skipped++
				continue
			}
		}
		if err := renderCity(c, idx, outPath); err != nil {
			log.Printf("FAIL %s: %v", c.Key, err)
			failed++
			continue
		}
		rendered++
		log.Printf("OK   %s -> %s", c.Key, outPath)
	}

	fmt.Println()
	fmt.Println("=== citymaps summary ===")
	fmt.Printf("rendered:              %d\n", rendered)
	fmt.Printf("skipped (exists):      %d\n", skipped)
	fmt.Printf("skipped (no coords):   %d\n", skippedNoCoord)
	fmt.Printf("failed:                %d\n", failed)
	if failed > 0 {
		return fmt.Errorf("%d city map(s) failed to render", failed)
	}
	return nil
}

func renderCity(c citySeed, idx map[string]orb.MultiPolygon, outPath string) error {
	dc, err := citymap.Render(citymap.City{
		Key:     c.Key,
		Name:    c.Name,
		Country: c.Country,
		Lat:     *c.Lat,
		Lng:     *c.Lng,
	}, idx)
	if err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if err := citymap.WritePNG(f, dc); err != nil {
		return fmt.Errorf("encode %s: %w", outPath, err)
	}
	return nil
}

func loadCities(path string) ([]citySeed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cities seed %s: %w", path, err)
	}
	var f citiesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse cities seed %s: %w", path, err)
	}
	return f.Cities, nil
}

// loadValidISO returns the uppercase ISO2 set seeds/countries.yaml defines,
// reusing the engine's countries-seed loader (same pattern as cmd/citygen).
func loadValidISO(path string) (map[string]bool, error) {
	countries, err := engine.LoadCountriesFile(path)
	if err != nil {
		return nil, fmt.Errorf("load countries seed: %w", err)
	}
	out := make(map[string]bool, len(countries))
	for _, c := range countries {
		out[c.ISOA2] = true
	}
	return out, nil
}
