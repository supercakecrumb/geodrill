package citymap

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
)

// isoOverrides patches ISO2 codes Natural Earth's properties get wrong or
// leave out. Natural Earth stores the literal string "-99" in ISO_A2 (and
// even in ISO_A2_EH) for a few features — most famously France and Norway —
// so we key those back by their well-known admin name. Kosovo (XK) has no
// official ISO2 at all and NE carries "-99"; map it too so a KO/XK city can
// still be framed if present in the seed.
var isoOverrides = map[string]string{
	"France": "FR",
	"Norway": "NO",
	"Kosovo": "XK",
}

// isoFromFeature extracts the ISO2 code for a Natural Earth country feature.
// It reads ISO_A2_EH first (the "eH" / de-facto variant, which is correct
// where plain ISO_A2 is the "-99" placeholder), falls back to ISO_A2, and
// finally to a name-keyed override for the known "-99" gaps (FR, NO, XK).
// Returns "" when no usable code can be found.
func isoFromFeature(f *geojson.Feature) string {
	if v := isoString(f, "ISO_A2_EH"); v != "" {
		return v
	}
	if v := isoString(f, "ISO_A2"); v != "" {
		return v
	}
	// Name-keyed overrides for features whose ISO codes are all "-99".
	for _, nameKey := range []string{"ADMIN", "NAME", "NAME_EN", "SOVEREIGNT"} {
		name := strings.TrimSpace(f.Properties.MustString(nameKey, ""))
		if code, ok := isoOverrides[name]; ok {
			return code
		}
	}
	return ""
}

// isoString returns the property as an uppercased ISO2, treating the Natural
// Earth "-99" placeholder (and empty/whitespace) as absent.
func isoString(f *geojson.Feature, key string) string {
	v := strings.TrimSpace(f.Properties.MustString(key, ""))
	if v == "" || v == "-99" {
		return ""
	}
	return strings.ToUpper(v)
}

// LoadCountryIndex decodes a Natural Earth admin-0 countries GeoJSON file into
// a map keyed by ISO2 -> that country's geometry normalized to
// orb.MultiPolygon. Features with no resolvable ISO2 are skipped. If two
// features resolve to the same ISO2 their polygons are merged (Natural Earth
// keeps one feature per country, but this keeps the loader total).
func LoadCountryIndex(path string) (map[string]orb.MultiPolygon, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("citymap: read natural earth file %s: %w", path, err)
	}
	return DecodeCountryIndex(data)
}

// DecodeCountryIndex is LoadCountryIndex on already-read bytes — the seam the
// tests drive with small fixtures instead of the 24 MB file.
func DecodeCountryIndex(data []byte) (map[string]orb.MultiPolygon, error) {
	fc, err := geojson.UnmarshalFeatureCollection(data)
	if err != nil {
		return nil, fmt.Errorf("citymap: parse natural earth geojson: %w", err)
	}
	idx := make(map[string]orb.MultiPolygon, len(fc.Features))
	for _, f := range fc.Features {
		iso := isoFromFeature(f)
		if iso == "" {
			continue
		}
		mp, ok := asMultiPolygon(f.Geometry)
		if !ok {
			continue
		}
		idx[iso] = append(idx[iso], mp...)
	}
	return idx, nil
}

// asMultiPolygon normalizes a feature geometry to orb.MultiPolygon. A Polygon
// becomes a single-element MultiPolygon; a MultiPolygon passes through; any
// other geometry type (Point, LineString, ...) is rejected.
func asMultiPolygon(g orb.Geometry) (orb.MultiPolygon, bool) {
	switch v := g.(type) {
	case orb.Polygon:
		return orb.MultiPolygon{v}, true
	case orb.MultiPolygon:
		return v, true
	default:
		return nil, false
	}
}

// AuditISOCoverage returns the sorted, de-duplicated list of wanted ISO2 codes
// that have no polygon in idx (missing key or empty geometry). The CLI uses it
// to fail fast before rendering rather than silently emitting blank maps.
func AuditISOCoverage(idx map[string]orb.MultiPolygon, wantISO2 []string) []string {
	seen := make(map[string]bool, len(wantISO2))
	var missing []string
	for _, want := range wantISO2 {
		iso := strings.ToUpper(strings.TrimSpace(want))
		if iso == "" || seen[iso] {
			continue
		}
		seen[iso] = true
		if len(idx[iso]) == 0 {
			missing = append(missing, iso)
		}
	}
	sort.Strings(missing)
	return missing
}
