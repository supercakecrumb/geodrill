// Package citymap renders a static, label-free PNG map for each city: a red
// marker at the city's coordinates over a muted Natural Earth basemap, with
// the city's own country highlighted. It is the image behind the "which city
// is at the marker?" question.
//
// The package is pure and offline: given a decoded Natural Earth country
// index (map[ISO2]orb.MultiPolygon) and a city's lat/lng, Render produces a
// deterministic 1280x800 image with no text, no network, and no randomness.
// The 24 MB source GeoJSON is fetched out-of-band by scripts/fetch-
// naturalearth.sh into the gitignored data/ tree; this package's tests run
// against tiny hand-written fixtures instead.
//
// Pipeline: LoadCountryIndex (countries.go) -> BuildFrame (frame.go, pure
// framing math) -> Render (render.go, projection + gg drawing).
package citymap

import "strings"

// Output image dimensions. Fixed so framing (aspect-fit) and the on-screen
// marker sizing are deterministic.
const (
	ImageWidth  = 1280
	ImageHeight = 800
)

// City is the minimal input the renderer needs: a stable key, the ISO2 of the
// country it belongs to, and its geographic location. Name is carried for
// logging/debugging only — it is never drawn (the map is label-free).
type City struct {
	Key     string
	Name    string
	Country string // ISO2 uppercase, e.g. "DE"
	Lat     float64
	Lng     float64
}

// ImageFileName is the single home of the city-image filename convention:
// the city key with ':' replaced by '-' plus ".png" (so "de:munich" ->
// "de-munich.png"). A later phase uploads these to S3 under the same names.
func ImageFileName(key string) string {
	return strings.ReplaceAll(key, ":", "-") + ".png"
}
