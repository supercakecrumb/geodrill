// Command citygen regenerates seeds/cities.yaml from two inputs:
//
//   - the EXISTING seeds/cities.yaml, whose curated entries carry hand-picked
//     English exonyms (Munich, Moscow, Vienna, ...) this tool must preserve
//     verbatim — GeoNames' own asciiname for those rows is often a native
//     transliteration, not the exonym a GeoGuessr player would type;
//   - the raw GeoNames cities15000 dump (data/geonames/cities15000.txt by
//     default, fetched by scripts/fetch-cities.sh), which supplies accurate
//     population figures and a long tail of additional cities far beyond the
//     451 originally curated by hand.
//
// # Exonym-preserving merge
//
// For every curated city, this tool looks for a GeoNames row in the SAME
// country whose name, asciiname, or any alternatename equals the curated
// English name (case-insensitive) — see bestCandidate for how ties among
// several such rows are resolved (preferring a row whose OWN name/asciiname
// matches over one that only lists the name as an alternatename, to avoid
// e.g. Shenzhen's alternatenames swallowing a "Bao'an" match). A match's
// population replaces the curated (unreliable-scale) population figure, and
// the matched geonameid is "claimed" so the same real-world city can't also
// appear as a separate long-tail row. The curated Name and Key are always
// kept as-is — never replaced by GeoNames' own name for that row.
//
// Curated cities with no GeoNames match keep their existing Name and
// Population verbatim (every value already surviving in the committed seed
// file is a real, if occasionally city-proper-vs-metro-inconsistent, raw
// population count — never actually "thousands" despite the pre-rewrite
// header comment's claim; see this tool's own report at the end of a run
// for exactly how many entries this affected).
//
// Every GeoNames row NOT claimed by a curated city is a long-tail candidate:
// keyed and named from its own asciiname/country, capped per-country (see
// perCountryCap) to keep the seed file's size and per-country balance sane.
// The cap only trims long-tail rows — every curated city survives
// regardless of how large its country's curated count already is.
//
// Every output city gets an explicit population-banded tier (see tierBands)
// — this is what lets internal/topics/cities/seed.go set a per-CITY tier
// instead of inheriting the tier of the city's country.
//
// Regeneration: ./scripts/fetch-cities.sh && go run ./cmd/citygen
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// perCountryCap bounds how many NEW (long-tail, non-curated) GeoNames rows
// citygen adds per country — curated cities are exempt (see package doc).
const perCountryCap = 30

// tierBand is one row of the population->tier lookup table (see tierBands),
// checked lowBound-inclusive, highest bound first.
type tierBand struct {
	minPopulation int64
	tier          int16
}

// tierBands implements the population-banded tier rubric from the kanban
// card: tier 0 is the biggest metros, tier 6 the smallest towns GeoNames'
// own cities15000 floor (population >= 15,000) still covers. Checked in
// order, first (highest) match wins.
var tierBands = []tierBand{
	{5_000_000, 0},
	{2_000_000, 1},
	{1_000_000, 2},
	{500_000, 3},
	{200_000, 4},
	{75_000, 5},
	{0, 6},
}

func tierFor(population int64) int16 {
	for _, b := range tierBands {
		if population >= b.minPopulation {
			return b.tier
		}
	}
	return 6
}

// elevationFor is the effective-elevation rule for a GeoNames cities15000
// row: prefer the surveyed elevation column (15) when it parses to an
// integer; else fall back to the dem column (16, an SRTM-derived model
// value) when it parses AND isn't the -9999 "no data" sentinel; else the
// elevation is unknown (nil). Pure and unit-tested in isolation because the
// col15/col16/-9999 fallback logic is easy to get subtly wrong.
func elevationFor(elevField, demField string) *int {
	if v, err := strconv.Atoi(strings.TrimSpace(elevField)); err == nil {
		return &v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(demField)); err == nil && v != -9999 {
		return &v
	}
	return nil
}

// regionFor looks up the admin1 (state/province/region) name for a GeoNames
// row's country + admin1 code (e.g. "DE" + "02" -> admin1Names["DE.02"] ->
// "Bavaria"). Returns "" when the code is blank or unmapped.
func regionFor(admin1Names map[string]string, country, admin1Code string) string {
	if admin1Code == "" {
		return ""
	}
	return admin1Names[country+"."+admin1Code]
}

// altNamesFor returns a small, deduplicated (case-insensitive, against both
// each other and cityName) alt-names list from a matched GeoNames row's own
// Name and ASCIIName — deliberately NOT the full alternatenames column,
// which would bloat the seed file. Empties and anything matching cityName
// are dropped.
func altNamesFor(cityName, rowName, rowASCIIName string) []string {
	seen := map[string]bool{normalize(cityName): true}
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		n := normalize(s)
		if seen[n] {
			return
		}
		seen[n] = true
		out = append(out, s)
	}
	add(rowName)
	add(rowASCIIName)
	return out
}

// bestCandidate selects the best-matching GeoNames row for a curated city
// among candidates that all normalize to the same target name via Name,
// ASCIIName, or an alternatename (see buildNameIndex/nameIndex). It PREFERS
// rows whose OWN Name or ASCIIName equals target (a "main-name match") over
// rows that only listed target as one of many alternatenames — an
// unrelated, much larger place can easily "contain" a smaller place's name
// in its own long alternatenames list (e.g. Shenzhen's alternatenames
// include "Bao'an"; New York City's include "manhattan"; Hong Kong's
// include "Victoria"; Yerevan's include "Erebuni" — all real false matches
// this rule exists to prevent). Only falls back to the best
// alternatename-only match when there is no main-name match at all. Ties
// within whichever pool is used are broken by largest population, same as
// before this rule existed. candidates must be non-empty.
func bestCandidate(candidates []*geoRow, target string) *geoRow {
	var mainMatches, altMatches []*geoRow
	for _, cand := range candidates {
		if normalize(cand.Name) == target || normalize(cand.ASCIIName) == target {
			mainMatches = append(mainMatches, cand)
		} else {
			altMatches = append(altMatches, cand)
		}
	}
	pool := mainMatches
	if len(pool) == 0 {
		pool = altMatches
	}
	best := pool[0]
	for _, cand := range pool[1:] {
		if cand.Population > best.Population {
			best = cand
		}
	}
	return best
}

// oldCitySeed is the pre-existing seeds/cities.yaml schema — read once, at
// the start of a regeneration run, to recover the curated entries'
// hand-picked names/populations AND any hand-patched geographic fields
// (lat/lng/region/elevation/geoname_id/alt_names) that must survive a
// regen for curated cities citygen can't match against GeoNames.
type oldCitySeed struct {
	Key        string   `yaml:"key"`
	Name       string   `yaml:"name"`
	Country    string   `yaml:"country"`
	Population int64    `yaml:"population"`
	Lat        float64  `yaml:"lat"`
	Lng        float64  `yaml:"lng"`
	Region     string   `yaml:"region"`
	Elevation  *int     `yaml:"elevation"`
	GeonameID  string   `yaml:"geoname_id"`
	AltNames   []string `yaml:"alt_names"`
}

type oldCitiesFile struct {
	Cities []oldCitySeed `yaml:"cities"`
}

// outputCity is the new seeds/cities.yaml schema this tool writes: adds
// Tier, and Population is now always a raw (not "thousands") integer. Adds
// per-city geographic fields (lat/lng/region/elevation/geoname_id/alt_names)
// for the upcoming map-based cities feature.
type outputCity struct {
	Key        string   `yaml:"key"`
	Name       string   `yaml:"name"`
	Country    string   `yaml:"country"`
	Population int64    `yaml:"population"`
	Tier       int16    `yaml:"tier"`
	Lat        float64  `yaml:"lat,omitempty"`
	Lng        float64  `yaml:"lng,omitempty"`
	Region     string   `yaml:"region,omitempty"`
	Elevation  *int     `yaml:"elevation,omitempty"`
	GeonameID  string   `yaml:"geoname_id,omitempty"`
	AltNames   []string `yaml:"alt_names,omitempty"`
}

type outputCitiesFile struct {
	Cities []outputCity `yaml:"cities"`
}

// geoRow is one parsed data row from the GeoNames cities15000 TSV dump.
// Column indices (0-based, verified against a sample row from the actual
// download before trusting them): 0 geonameid, 1 name, 2 asciiname,
// 3 alternatenames (comma-separated), 4 latitude, 5 longitude,
// 8 country_code, 10 admin1 code, 14 population, 15 elevation, 16 dem
// (17 timezone, unused, is NOT dem — verified against sample rows before
// trusting these indices, since the two are easy to swap off-by-one).
type geoRow struct {
	ID         string
	Name       string
	ASCIIName  string
	AltNames   []string
	Country    string // ISO2 uppercase
	Population int64
	Lat        float64 // 0 when HasCoords is false
	Lng        float64 // 0 when HasCoords is false
	HasCoords  bool
	Admin1Code string // e.g. "02" for a "DE.02" admin1 code; "" when absent
	Elevation  *int   // effective elevation (see elevationFor); nil when unknown
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns an ASCII city name into the lowercase, hyphenated slug half
// of a "<iso2lower>:<slug>" key. Only used for NEW (long-tail) cities —
// curated cities keep their existing, already-reviewed Key untouched.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "city"
	}
	return s
}

// normalize is the casefold used to match a curated English name against a
// GeoNames name/asciiname/alternatename.
func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "citygen: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	geonamesPath := flag.String("geonames", "data/geonames/cities15000.txt", "path to the raw GeoNames cities15000 TSV dump")
	admin1Path := flag.String("admin1", "data/geonames/admin1CodesASCII.txt", "path to the raw GeoNames admin1CodesASCII TSV dump")
	citiesPath := flag.String("cities", "seeds/cities.yaml", "path to the existing seeds/cities.yaml to preserve curated exonyms from")
	countriesPath := flag.String("countries", "seeds/countries.yaml", "path to seeds/countries.yaml (source of the valid ISO2 set)")
	outPath := flag.String("out", "", "output path (default: same as -cities)")
	cap := flag.Int("cap", perCountryCap, "max NEW (non-curated) long-tail cities kept per country")
	flag.Parse()
	if *outPath == "" {
		*outPath = *citiesPath
	}

	validISO, err := loadValidISO(*countriesPath)
	if err != nil {
		return err
	}

	oldFile, err := loadOldCities(*citiesPath)
	if err != nil {
		return err
	}

	rows, err := loadGeonames(*geonamesPath, validISO)
	if err != nil {
		return err
	}

	admin1Names, err := loadAdmin1Names(*admin1Path)
	if err != nil {
		return err
	}

	nameIndex := buildNameIndex(rows)

	result, err := merge(oldFile.Cities, rows, nameIndex, *cap, admin1Names)
	if err != nil {
		return err
	}

	if err := writeCities(*outPath, result.cities); err != nil {
		return err
	}

	report(result)
	return nil
}

// loadValidISO returns the uppercase ISO2 set seeds/countries.yaml defines —
// the authoritative "does this country exist in our system" check GeoNames
// rows must pass to be considered at all.
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

func loadOldCities(path string) (oldCitiesFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return oldCitiesFile{}, fmt.Errorf("read existing cities seed %s: %w", path, err)
	}
	var f oldCitiesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return oldCitiesFile{}, fmt.Errorf("parse existing cities seed %s: %w", path, err)
	}
	return f, nil
}

// loadGeonames parses the GeoNames cities15000 TSV, keeping only rows whose
// country_code is a valid ISO2 (per validISO) and whose population is
// positive (a handful of dump rows carry an empty/zero population and are
// useless as either a match candidate or a long-tail addition).
func loadGeonames(path string, validISO map[string]bool) ([]geoRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open geonames dump %s: %w", path, err)
	}
	defer f.Close()

	var rows []geoRow
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 18 {
			return nil, fmt.Errorf("geonames dump %s line %d: expected >= 18 tab-separated fields, got %d (schema mismatch — stop and report)", path, lineNo, len(fields))
		}
		country := strings.ToUpper(strings.TrimSpace(fields[8]))
		if !validISO[country] {
			continue
		}
		pop, err := strconv.ParseInt(strings.TrimSpace(fields[14]), 10, 64)
		if err != nil || pop <= 0 {
			continue
		}
		var alts []string
		if fields[3] != "" {
			alts = strings.Split(fields[3], ",")
		}
		lat, latErr := strconv.ParseFloat(strings.TrimSpace(fields[4]), 64)
		lng, lngErr := strconv.ParseFloat(strings.TrimSpace(fields[5]), 64)
		hasCoords := latErr == nil && lngErr == nil
		if !hasCoords {
			lat, lng = 0, 0
		}
		rows = append(rows, geoRow{
			ID:         fields[0],
			Name:       fields[1],
			ASCIIName:  fields[2],
			AltNames:   alts,
			Country:    country,
			Population: pop,
			Lat:        lat,
			Lng:        lng,
			HasCoords:  hasCoords,
			Admin1Code: strings.TrimSpace(fields[10]),
			Elevation:  elevationFor(fields[15], fields[16]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan geonames dump %s: %w", path, err)
	}
	return rows, nil
}

// loadAdmin1Names parses the GeoNames admin1CodesASCII.txt TSV (columns:
// code, name, asciiname, geonameid — no header) into a code -> name map,
// e.g. "DE.02" -> "Bavaria". Uses the UTF-8 name column (index 1), not
// asciiname, since region names are just as displayable natively as city
// names are.
func loadAdmin1Names(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open admin1 codes dump %s: %w", path, err)
	}
	defer f.Close()

	out := make(map[string]string)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return nil, fmt.Errorf("admin1 codes dump %s line %d: expected >= 2 tab-separated fields, got %d (schema mismatch — stop and report)", path, lineNo, len(fields))
		}
		code := strings.TrimSpace(fields[0])
		if code == "" {
			continue
		}
		out[code] = strings.TrimSpace(fields[1])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan admin1 codes dump %s: %w", path, err)
	}
	return out, nil
}

// buildNameIndex maps country -> normalized name -> every geoRow (by
// pointer into rows) whose name, asciiname, or any alternatename normalizes
// to that string. A row can appear under several keys (name + asciiname +
// each alternatename).
func buildNameIndex(rows []geoRow) map[string]map[string][]*geoRow {
	idx := make(map[string]map[string][]*geoRow)
	add := func(country, name string, r *geoRow) {
		if name == "" {
			return
		}
		n := normalize(name)
		byName, ok := idx[country]
		if !ok {
			byName = make(map[string][]*geoRow)
			idx[country] = byName
		}
		byName[n] = append(byName[n], r)
	}
	for i := range rows {
		r := &rows[i]
		add(r.Country, r.Name, r)
		add(r.Country, r.ASCIIName, r)
		for _, alt := range r.AltNames {
			add(r.Country, alt, r)
		}
	}
	return idx
}

// mergeResult is everything report needs, kept together so run() stays a
// thin pipeline.
type mergeResult struct {
	cities            []outputCity
	matchedCount      int
	unmatchedCount    int
	unmatchedHandling []string // human-readable notes, one per unmatched curated city
	longTailAdded     int
	perTierCount      map[int16]int
	perCountryCurated map[string]int
}

// merge is the exonym-preserving merge described in the package doc: match
// every curated city against nameIndex, claim its geonameid, then fold in a
// capped long tail of every unclaimed GeoNames row.
func merge(curated []oldCitySeed, rows []geoRow, nameIndex map[string]map[string][]*geoRow, cap int, admin1Names map[string]string) (mergeResult, error) {
	claimed := make(map[string]bool)
	usedKeys := make(map[string]outputCity, len(curated))
	perCountryCurated := make(map[string]int)

	res := mergeResult{perTierCount: make(map[int16]int), perCountryCurated: perCountryCurated}

	// Pass 1: curated cities keep their Key/Name; population comes from the
	// best (largest-population) GeoNames match in-country, if any.
	for _, c := range curated {
		if _, dup := usedKeys[c.Key]; dup {
			return mergeResult{}, fmt.Errorf("existing seeds/cities.yaml has duplicate key %q", c.Key)
		}
		population := c.Population
		// Default to the existing seed's own geographic fields — preserved
		// verbatim when this curated city has no GeoNames match this run
		// (e.g. a one-time hand-patched coordinate must survive a regen).
		lat, lng := c.Lat, c.Lng
		region := c.Region
		elevation := c.Elevation
		geonameID := c.GeonameID
		altNames := c.AltNames
		matched := false
		if candidates, ok := nameIndex[c.Country][normalize(c.Name)]; ok && len(candidates) > 0 {
			best := bestCandidate(candidates, normalize(c.Name))
			population = best.Population
			claimed[best.ID] = true
			matched = true
			lat, lng = best.Lat, best.Lng
			region = regionFor(admin1Names, best.Country, best.Admin1Code)
			elevation = best.Elevation
			geonameID = best.ID
			altNames = altNamesFor(c.Name, best.Name, best.ASCIIName)
		}
		if matched {
			res.matchedCount++
		} else {
			res.unmatchedCount++
			res.unmatchedHandling = append(res.unmatchedHandling,
				fmt.Sprintf("%s (%s): no GeoNames match in-country; kept existing seed population %d as-is (already a raw, if occasionally city-proper-vs-metro, count)", c.Name, c.Country, c.Population))
		}

		out := outputCity{
			Key: c.Key, Name: c.Name, Country: c.Country, Population: population, Tier: tierFor(population),
			Lat: lat, Lng: lng, Region: region, Elevation: elevation, GeonameID: geonameID, AltNames: altNames,
		}
		usedKeys[c.Key] = out
		perCountryCurated[c.Country]++
	}

	// Pass 2: long-tail candidates are every unclaimed row. Group by
	// country, dedupe by generated key within the group (keep the larger
	// population on a slug collision), drop anything whose key already
	// belongs to a curated city, then cap per country.
	byCountry := make(map[string][]*geoRow)
	for i := range rows {
		r := &rows[i]
		if claimed[r.ID] {
			continue
		}
		byCountry[r.Country] = append(byCountry[r.Country], r)
	}

	countries := make([]string, 0, len(byCountry))
	for country := range byCountry {
		countries = append(countries, country)
	}
	sort.Strings(countries)

	longTail := make([]outputCity, 0)
	for _, country := range countries {
		candidateRows := byCountry[country]

		byKey := make(map[string]*geoRow, len(candidateRows))
		for _, r := range candidateRows {
			key := strings.ToLower(country) + ":" + slugify(r.ASCIIName)
			if _, isCurated := usedKeys[key]; isCurated {
				continue // curated city already owns this key
			}
			if existing, ok := byKey[key]; !ok || r.Population > existing.Population {
				byKey[key] = r
			}
		}

		deduped := make([]*geoRow, 0, len(byKey))
		for _, r := range byKey {
			deduped = append(deduped, r)
		}
		sort.SliceStable(deduped, func(i, j int) bool {
			if deduped[i].Population != deduped[j].Population {
				return deduped[i].Population > deduped[j].Population
			}
			return deduped[i].ID < deduped[j].ID
		})

		remaining := cap - perCountryCurated[country]
		if remaining < 0 {
			remaining = 0
		}
		if remaining > len(deduped) {
			remaining = len(deduped)
		}

		for _, r := range deduped[:remaining] {
			key := strings.ToLower(country) + ":" + slugify(r.ASCIIName)
			longTail = append(longTail, outputCity{
				Key:        key,
				Name:       r.ASCIIName,
				Country:    country,
				Population: r.Population,
				Tier:       tierFor(r.Population),
				Lat:        r.Lat,
				Lng:        r.Lng,
				Region:     regionFor(admin1Names, r.Country, r.Admin1Code),
				Elevation:  r.Elevation,
				GeonameID:  r.ID,
				AltNames:   altNamesFor(r.ASCIIName, r.Name, r.ASCIIName),
			})
		}
	}
	res.longTailAdded = len(longTail)

	all := make([]outputCity, 0, len(usedKeys)+len(longTail))
	for _, c := range curated {
		all = append(all, usedKeys[c.Key])
	}
	all = append(all, longTail...)

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Population != all[j].Population {
			return all[i].Population > all[j].Population
		}
		return all[i].Key < all[j].Key
	})

	for _, c := range all {
		res.perTierCount[c.Tier]++
	}
	res.cities = all
	return res, nil
}

func writeCities(path string, cities []outputCity) error {
	var b strings.Builder
	b.WriteString(headerComment())
	f := outputCitiesFile{Cities: cities}
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return fmt.Errorf("encode cities yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close yaml encoder: %w", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func headerComment() string {
	return `# Cities dataset — population-tiered world cities for a map-based city
# question.
#
# Schema:
#   key: <iso2-lowercase>:<city-slug> — collision-safe unique identifier
#   name: English exonym for curated cities (GeoGuessr players' name for the
#     place); GeoNames asciiname for the long-tail cities added beyond the
#     original curated set
#   country: ISO2 uppercase code (must exist in seeds/countries.yaml)
#   population: raw integer population (NOT thousands)
#   tier: 0-6, population-banded (see table below) — drives items.tier
#     directly; cities are tiered by their OWN population, not their
#     country's tier
#   lat, lng: decimal degrees from the matched GeoNames row; absent when no
#     GeoNames row could be matched/parsed for this city
#   region: admin-1 (state/province/region) name, resolved via GeoNames'
#     admin1CodesASCII.txt; "" when the code is blank or unmapped
#   elevation: meters, GeoNames' surveyed elevation column when present,
#     else its dem column (an SRTM-derived model value, NOT a survey — used
#     only as a fallback, and only when it isn't the -9999 "no data"
#     sentinel); absent when neither is available
#   geoname_id: the matched GeoNames geonameid, for traceability/re-matching
#   alt_names: small list of the matched GeoNames row's own name/asciiname
#     when they differ from this entry's name (NOT the full alternatenames
#     column, to keep the file from ballooning)
#
# Tier bands (raw population p):
#   tier 0: p >= 5,000,000              (the biggest metros)
#   tier 1: 2,000,000 <= p < 5,000,000
#   tier 2: 1,000,000 <= p < 2,000,000
#   tier 3:   500,000 <= p < 1,000,000
#   tier 4:   200,000 <= p <   500,000
#   tier 5:    75,000 <= p <   200,000
#   tier 6: p < 75,000                  (down to the 15,000 dataset floor)
#
# Sources: GeoNames cities15000 (https://www.geonames.org/), every populated
# place with population >= 15,000 (~26k rows worldwide), and GeoNames
# admin1CodesASCII.txt (admin-1 code -> region name lookup). Data © GeoNames,
# licensed CC-BY 4.0 (https://creativecommons.org/licenses/by/4.0/).
#
# This file is a DERIVED, curated/capped subset — the raw GeoNames dumps
# themselves are never committed (data/ is gitignored). The original 451
# hand-curated cities keep their reviewed English exonyms and Key even after
# a regeneration (cmd/citygen matches them against GeoNames by name to pull
# in an accurate population, but never renames or re-keys them); cities
# beyond that curated set use GeoNames' own asciiname, capped at 30 per
# country so the file doesn't balloon (the cap never removes a curated
# city). A curated city with no GeoNames match this run keeps whatever
# geographic fields it already carried (e.g. a one-time hand patch)
# unchanged.
#
# Regenerate with: ./scripts/fetch-cities.sh && go run ./cmd/citygen
`
}

func report(res mergeResult) {
	fmt.Printf("citygen: %d total cities (%d curated matched, %d curated unmatched, %d long-tail added)\n",
		len(res.cities), res.matchedCount, res.unmatchedCount, res.longTailAdded)

	fmt.Println("per-tier counts:")
	for tier := int16(0); tier <= 6; tier++ {
		fmt.Printf("  tier %d: %d\n", tier, res.perTierCount[tier])
	}

	if res.unmatchedCount > 0 {
		fmt.Println("unmatched curated cities (kept as-is):")
		for _, note := range res.unmatchedHandling {
			fmt.Println("  - " + note)
		}
	}

	samples := map[int16]string{0: "tier 0", 3: "tier 3", 6: "tier 6"}
	for _, tier := range []int16{0, 3, 6} {
		fmt.Printf("sample %s cities:\n", samples[tier])
		count := 0
		for _, c := range res.cities {
			if c.Tier != tier {
				continue
			}
			fmt.Printf("  %s | %s, %s | pop=%d\n", c.Key, c.Name, c.Country, c.Population)
			count++
			if count == 3 {
				break
			}
		}
	}
}
