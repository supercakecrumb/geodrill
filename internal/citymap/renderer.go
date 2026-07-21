package citymap

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/paulmach/orb"
	"gopkg.in/yaml.v3"
)

// KeyFromImageFileName is the inverse of ImageFileName: it recovers a city key
// from its map-image filename. It strips the ".png" suffix and turns the FIRST
// '-' back into the ':' that ImageFileName replaced. City keys are
// "<iso2>:<slug>" and an ISO2 never contains '-', so splitting on the first
// '-' is exact even when the slug itself contains hyphens ("cn:bao-an" ->
// "cn-bao-an.png" -> "cn:bao-an"). A filename with no '-' (a colon-free key)
// round-trips unchanged. Multi-colon keys are NOT round-trippable — but real
// city keys carry exactly one colon, so this inverse is exact for every key
// ImageFileName is used on in practice.
func KeyFromImageFileName(file string) string {
	name := strings.TrimSuffix(file, ".png")
	if i := strings.IndexByte(name, '-'); i >= 0 {
		return name[:i] + ":" + name[i+1:]
	}
	return name
}

// cityPoint is a seeded city's renderable location: its coordinates and the
// ISO2 of the country the renderer highlights around the marker.
type cityPoint struct {
	lat, lng float64
	iso2     string
}

// rendererCitySeed is a LOCAL minimal mirror of one seeds/cities.yaml entry —
// only the fields RenderPNG needs (key, country, coords). Kept independent of
// the cities topic's own seed struct so importing that package (an import
// cycle risk, and unnecessary coupling) is avoided: the renderer reads the
// committed YAML itself.
type rendererCitySeed struct {
	Key     string  `yaml:"key"`
	Country string  `yaml:"country"`
	Lat     float64 `yaml:"lat"`
	Lng     float64 `yaml:"lng"`
}

type rendererCitiesFile struct {
	Cities []rendererCitySeed `yaml:"cities"`
}

// Renderer renders a city's label-free map on demand, in-process. It loads the
// Natural Earth country index and the city coordinate table ONCE at
// construction, then serves RenderPNG deterministically with no further I/O.
// It is the runtime counterpart to cmd/citymaps' disk batch: the bot builds
// one at startup so a city map is rendered lazily on its first Telegram send
// (internal/telegram SendPhoto), then cached as a telegram_file_id forever.
type Renderer struct {
	countryIdx map[string]orb.MultiPolygon
	points     map[string]cityPoint
}

// NewRenderer loads the Natural Earth countries GeoJSON at neGeoJSONPath and
// the city coordinate table from the cities seed at citiesSeedPath. A missing
// or unreadable Natural Earth file returns a clear error so the caller can
// treat the renderer as unavailable (nil) and degrade city maps to text.
func NewRenderer(neGeoJSONPath, citiesSeedPath string) (*Renderer, error) {
	idx, err := LoadCountryIndex(neGeoJSONPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(citiesSeedPath)
	if err != nil {
		return nil, fmt.Errorf("citymap: read cities seed %s: %w", citiesSeedPath, err)
	}
	var cf rendererCitiesFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("citymap: parse cities seed %s: %w", citiesSeedPath, err)
	}
	points := make(map[string]cityPoint, len(cf.Cities))
	for _, c := range cf.Cities {
		points[c.Key] = cityPoint{lat: c.Lat, lng: c.Lng, iso2: strings.ToUpper(c.Country)}
	}
	return &Renderer{countryIdx: idx, points: points}, nil
}

// Keys returns every seeded city key, sorted — the pre-warm tool
// (cmd/citymapsync) iterates them to render+upload the whole set ahead of
// first send. Keys with no coordinates are included; RenderPNG reports them as
// unrenderable (ok=false).
func (r *Renderer) Keys() []string {
	keys := make([]string, 0, len(r.points))
	for k := range r.points {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RenderPNG renders the city map for key and PNG-encodes it to bytes. It
// returns ok=false (and no error) when the key is unknown or the city has no
// coordinates (exactly (0,0)) — the caller then falls back to a text question.
// A real render/encode failure (e.g. the country has no Natural Earth
// geometry) returns a non-nil error. Deterministic and pure aside from reading
// the in-memory index built at construction.
func (r *Renderer) RenderPNG(key string) ([]byte, bool, error) {
	pt, ok := r.points[key]
	if !ok || (pt.lat == 0 && pt.lng == 0) {
		return nil, false, nil
	}
	dc, err := Render(City{Key: key, Country: pt.iso2, Lat: pt.lat, Lng: pt.lng}, r.countryIdx)
	if err != nil {
		return nil, false, fmt.Errorf("citymap: render %s: %w", key, err)
	}
	var buf bytes.Buffer
	if err := WritePNG(&buf, dc); err != nil {
		return nil, false, fmt.Errorf("citymap: encode %s: %w", key, err)
	}
	return buf.Bytes(), true, nil
}
