package citymap

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/fogleman/gg"
	"github.com/paulmach/orb"
)

// Palette — muted, label-free basemap tuned so the red marker is the only
// thing that draws the eye.
const (
	colorOcean         = "#B8D6E6" // sea fill
	colorLand          = "#E9E5DC" // other countries' fill
	colorLandStroke    = "#CFC9BD" // other countries' border
	colorHighlight     = "#F2D8A0" // the city's own country fill
	colorHighlightEdge = "#A87F32" // the city's own country border
	colorMarker        = "#D64533" // marker halo + core (red)
)

// Marker geometry, in screen pixels — constant regardless of zoom so the dot
// reads the same on a microstate frame and a continent-spanning one.
const (
	markerHaloR  = 16.0
	markerRingR  = 8.0
	markerCoreR  = 6.0
	markerHaloA  = 0.35
	landStrokeW  = 1.0
	countryEdgeW = 2.0
)

// projector maps geographic (lon,lat) to image pixels via a local
// equirectangular projection: x scales normalized longitude by cos(latMid),
// y is a plain latitude drop from the frame's top edge. Deterministic and
// Mercator-free.
type projector struct {
	minLng, maxLat float64
	cosLat         float64
	scaleX, scaleY float64
	center         float64
}

func newProjector(f Frame) projector {
	cosLat := f.cosLatMid()
	return projector{
		minLng: f.MinLng,
		maxLat: f.MaxLat,
		cosLat: cosLat,
		center: f.CenterLng,
		scaleX: float64(ImageWidth) / ((f.MaxLng - f.MinLng) * cosLat),
		scaleY: float64(ImageHeight) / (f.MaxLat - f.MinLat),
	}
}

func (p projector) project(pt orb.Point) (float64, float64) {
	lon := normalizeLon(pt[0], p.center)
	x := (lon - p.minLng) * p.cosLat * p.scaleX
	y := (p.maxLat - pt[1]) * p.scaleY
	return x, y
}

// Render draws the label-free city map: ocean, all in-frame countries muted,
// the city's own country highlighted, then the marker on top. It returns a
// *gg.Context (whose .Image() is an *image.RGBA) so callers can probe pixels
// or encode to PNG via WritePNG. Deterministic: a pure function of its inputs
// (sorted country iteration, no rng, no time).
func Render(city City, countryIdx map[string]orb.MultiPolygon) (*gg.Context, error) {
	own, ok := countryIdx[city.Country]
	if !ok || len(own) == 0 {
		return nil, fmt.Errorf("citymap: no geometry for city %q country %q", city.Key, city.Country)
	}

	frame := BuildFrame(city, own)
	proj := newProjector(frame)
	fb := frameBound(frame)

	dc := gg.NewContext(ImageWidth, ImageHeight)

	// 1. Ocean.
	dc.SetHexColor(colorOcean)
	dc.Clear()

	// 2. Every in-frame country, muted. Sorted keys keep border-stroke
	// overlaps deterministic.
	isos := make([]string, 0, len(countryIdx))
	for iso := range countryIdx {
		isos = append(isos, iso)
	}
	sort.Strings(isos)
	for _, iso := range isos {
		mp := countryIdx[iso]
		if !normalizedMultiPolygonBound(mp, frame.CenterLng).Intersects(fb) {
			continue
		}
		drawMultiPolygon(dc, proj, mp, colorLand, colorLandStroke, landStrokeW)
	}

	// 3. Re-draw the city's own country highlighted, on top of the muted layer.
	drawMultiPolygon(dc, proj, own, colorHighlight, colorHighlightEdge, countryEdgeW)

	// 4. Marker last, in screen space.
	mx, my := proj.project(orb.Point{city.Lng, city.Lat})
	drawMarker(dc, mx, my)

	return dc, nil
}

// WritePNG encodes a rendered context to w as PNG.
func WritePNG(w io.Writer, dc *gg.Context) error {
	return dc.EncodePNG(w)
}

// maxDrawLngSpan guards against wrapped components. A polygon that, once
// longitudes are normalized against the frame center, spans more than this
// many degrees is straddling the frame's antimeridian seam (e.g. the US
// Aleutians seen from a European frame): its edges connect points on opposite
// sides and would draw a spurious line across the canvas. Such a component is
// never the city's own containing component, so skipping it is safe and
// removes the artifact.
const maxDrawLngSpan = 180.0

// drawMultiPolygon fills and strokes every ring-group of mp, honoring holes
// via even-odd fill (outer ring + hole subpaths, filled once per component).
func drawMultiPolygon(dc *gg.Context, proj projector, mp orb.MultiPolygon, fillHex, strokeHex string, strokeW float64) {
	dc.SetFillRule(gg.FillRuleEvenOdd)
	for _, poly := range mp {
		if len(poly) == 0 {
			continue
		}
		if b := normalizedPolygonBound(poly, proj.center); b.Max[0]-b.Min[0] > maxDrawLngSpan {
			continue
		}
		for _, ring := range poly {
			addRing(dc, proj, ring)
		}
		dc.SetHexColor(fillHex)
		dc.FillPreserve()
		dc.SetHexColor(strokeHex)
		dc.SetLineWidth(strokeW)
		dc.Stroke()
	}
}

// addRing appends one ring as a closed subpath in pixel space.
func addRing(dc *gg.Context, proj projector, ring orb.Ring) {
	if len(ring) == 0 {
		return
	}
	dc.NewSubPath()
	for i, pt := range ring {
		x, y := proj.project(pt)
		if i == 0 {
			dc.MoveTo(x, y)
		} else {
			dc.LineTo(x, y)
		}
	}
	dc.ClosePath()
}

// drawMarker paints the three-layer red marker: soft halo, white ring, red
// core — constant pixel radii, drawn in screen space.
func drawMarker(dc *gg.Context, x, y float64) {
	r, g, b := hexRGB(colorMarker)
	dc.SetRGBA(r, g, b, markerHaloA)
	dc.DrawCircle(x, y, markerHaloR)
	dc.Fill()

	dc.SetRGB(1, 1, 1)
	dc.DrawCircle(x, y, markerRingR)
	dc.Fill()

	dc.SetRGB(r, g, b)
	dc.DrawCircle(x, y, markerCoreR)
	dc.Fill()
}

// hexRGB parses "#RRGGBB" into 0..1 float components. The inputs are the
// package's own compile-time palette constants, so a parse miss simply yields
// black rather than an error path.
func hexRGB(hex string) (float64, float64, float64) {
	var ri, gi, bi int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &ri, &gi, &bi); err != nil {
		return 0, 0, 0
	}
	return float64(ri) / 255, float64(gi) / 255, float64(bi) / 255
}

// frameBound is the frame as an orb.Bound in normalized longitude space.
func frameBound(f Frame) orb.Bound {
	return orb.Bound{
		Min: orb.Point{f.MinLng, f.MinLat},
		Max: orb.Point{f.MaxLng, f.MaxLat},
	}
}

// normalizedMultiPolygonBound is the bound of mp with all longitudes
// normalized against center.
func normalizedMultiPolygonBound(mp orb.MultiPolygon, center float64) orb.Bound {
	b := orb.Bound{Min: orb.Point{math.Inf(1), math.Inf(1)}, Max: orb.Point{math.Inf(-1), math.Inf(-1)}}
	for _, poly := range mp {
		pb := normalizedPolygonBound(poly, center)
		b.Min[0] = math.Min(b.Min[0], pb.Min[0])
		b.Min[1] = math.Min(b.Min[1], pb.Min[1])
		b.Max[0] = math.Max(b.Max[0], pb.Max[0])
		b.Max[1] = math.Max(b.Max[1], pb.Max[1])
	}
	return b
}
