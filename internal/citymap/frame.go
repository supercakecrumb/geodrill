package citymap

import (
	"math"

	"github.com/paulmach/orb"
)

// minSpanDeg is the smallest geographic span (in degrees) a frame is allowed
// on either axis before aspect-fit. ~3.0° ≈ 330 km, enough to give a
// microstate (Monaco, San Marino, Singapore) some muted-neighbor context
// instead of a full-bleed single dot on one country.
const minSpanDeg = 3.0

// framePadFrac is the padding added on every side of the raw framing bound,
// as a fraction of that bound's span, so the country/marker never touches the
// image edge.
const framePadFrac = 0.08

// targetAspect is the output image aspect (width/height) the frame is fit to.
const targetAspect = float64(ImageWidth) / float64(ImageHeight)

// Frame is the geographic window to render, in a city-centered longitude
// space. Longitudes are normalized so the city sits near lon 0's antimeridian
// seam is never crossed (see normalizeLon); CenterLng records the reference
// the same normalization must be applied against at render time. Bounds are
// the final, padded, min-span-enforced, aspect-fit window.
type Frame struct {
	MinLat, MaxLat float64
	MinLng, MaxLng float64 // in normalized (city-centered) longitude space
	CenterLng      float64 // reference longitude for normalizeLon (the city's Lng)
}

// LatMid returns the frame's central latitude.
func (f Frame) LatMid() float64 { return (f.MinLat + f.MaxLat) / 2 }

// cosLatMid is the longitude-compression factor at the frame's mid-latitude,
// clamped away from zero so polar frames stay finite.
func (f Frame) cosLatMid() float64 {
	c := math.Cos(f.LatMid() * math.Pi / 180)
	if c < 0.01 {
		return 0.01
	}
	return c
}

// normalizeLon brings lon into the half-open interval [center-180, center+180)
// by adding whole turns of 360°. This is the one antimeridian trick: applied
// to every coordinate (and the city point) it makes a country that straddles
// ±180° (Russia, Fiji, NZ, the US Aleutians) contiguous in the frame's local
// longitude space, so ordinary min/max bound math just works.
func normalizeLon(lon, center float64) float64 {
	lo := center - 180
	// Shift into [lo, lo+360).
	lon -= lo
	lon -= 360 * math.Floor(lon/360)
	return lon + lo
}

// normalizedPolygonBound computes the geographic bound of a polygon after
// normalizing every longitude against center. All rings are considered.
func normalizedPolygonBound(p orb.Polygon, center float64) orb.Bound {
	b := orb.Bound{Min: orb.Point{math.Inf(1), math.Inf(1)}, Max: orb.Point{math.Inf(-1), math.Inf(-1)}}
	for _, ring := range p {
		for _, pt := range ring {
			lon := normalizeLon(pt[0], center)
			lat := pt[1]
			b.Min[0] = math.Min(b.Min[0], lon)
			b.Min[1] = math.Min(b.Min[1], lat)
			b.Max[0] = math.Max(b.Max[0], lon)
			b.Max[1] = math.Max(b.Max[1], lat)
		}
	}
	return b
}

// pointInRing reports whether pt lies inside ring, using a standard ray-cast
// (even-odd) test. Longitudes are compared in the same normalized space as
// the caller supplies. A ring may or may not repeat its first point as its
// last; the wrap handles both.
func pointInRing(ring orb.Ring, pt orb.Point) bool {
	inside := false
	n := len(ring)
	if n < 3 {
		return false
	}
	x, y := pt[0], pt[1]
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > y) != (yj > y) {
			xCross := xi + (y-yi)/(yj-yi)*(xj-xi)
			if x < xCross {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

// normalizedRing returns a copy of ring with every longitude normalized
// against center — so containment tests and bound math share one space.
func normalizedRing(ring orb.Ring, center float64) orb.Ring {
	out := make(orb.Ring, len(ring))
	for i, pt := range ring {
		out[i] = orb.Point{normalizeLon(pt[0], center), pt[1]}
	}
	return out
}

// BuildFrame computes the render window for a city over its country geometry.
// It is pure: no I/O, no globals, deterministic in its inputs. Steps mirror
// the package doc: normalize longitudes into a city-centered frame, pick the
// country component containing the city (else the nearest), union with the
// city point, pad, enforce a minimum span, then aspect-fit to 1280×800 by
// expanding the shorter axis (never cropping).
func BuildFrame(city City, mp orb.MultiPolygon) Frame {
	center := city.Lng
	cityPt := orb.Point{normalizeLon(city.Lng, center), city.Lat}

	comp := pickComponent(mp, cityPt, center)

	// Raw bound: chosen component ∪ city point (empty geometry -> just the
	// city point, later widened by the min-span floor).
	var b orb.Bound
	if comp != nil {
		b = normalizedPolygonBound(comp, center)
	} else {
		b = orb.Bound{Min: cityPt, Max: cityPt}
	}
	b = b.Extend(cityPt)

	f := Frame{
		MinLat:    b.Min[1],
		MaxLat:    b.Max[1],
		MinLng:    b.Min[0],
		MaxLng:    b.Max[0],
		CenterLng: center,
	}

	f = padFrame(f, framePadFrac)
	f = enforceMinSpan(f, minSpanDeg)
	f = aspectFit(f, targetAspect)
	return f
}

// pickComponent returns the polygon component of mp (in normalized space)
// that contains cityPt, or — if none does — the component whose bound center
// is nearest cityPt. Returns nil only when mp has no usable polygon.
func pickComponent(mp orb.MultiPolygon, cityPt orb.Point, center float64) orb.Polygon {
	var nearest orb.Polygon
	nearestDist := math.Inf(1)

	for _, poly := range mp {
		if len(poly) == 0 || len(poly[0]) < 3 {
			continue
		}
		outer := normalizedRing(poly[0], center)
		if pointInRing(outer, cityPt) {
			return normalizePolygon(poly, center)
		}
		bnd := normalizedPolygonBound(poly, center)
		cx := (bnd.Min[0] + bnd.Max[0]) / 2
		cy := (bnd.Min[1] + bnd.Max[1]) / 2
		d := (cx-cityPt[0])*(cx-cityPt[0]) + (cy-cityPt[1])*(cy-cityPt[1])
		if d < nearestDist {
			nearestDist = d
			nearest = normalizePolygon(poly, center)
		}
	}
	return nearest
}

// normalizePolygon returns a copy of poly with all rings' longitudes
// normalized against center.
func normalizePolygon(poly orb.Polygon, center float64) orb.Polygon {
	out := make(orb.Polygon, len(poly))
	for i, ring := range poly {
		out[i] = normalizedRing(ring, center)
	}
	return out
}

// padFrame grows the frame by frac of each axis span on every side.
func padFrame(f Frame, frac float64) Frame {
	latPad := (f.MaxLat - f.MinLat) * frac
	lngPad := (f.MaxLng - f.MinLng) * frac
	f.MinLat -= latPad
	f.MaxLat += latPad
	f.MinLng -= lngPad
	f.MaxLng += lngPad
	return f
}

// enforceMinSpan widens (centered) any axis whose span is below minDeg to
// exactly minDeg, so microstates get neighbor context. Never shrinks.
func enforceMinSpan(f Frame, minDeg float64) Frame {
	if s := f.MaxLat - f.MinLat; s < minDeg {
		mid := (f.MinLat + f.MaxLat) / 2
		f.MinLat = mid - minDeg/2
		f.MaxLat = mid + minDeg/2
	}
	if s := f.MaxLng - f.MinLng; s < minDeg {
		mid := (f.MinLng + f.MaxLng) / 2
		f.MinLng = mid - minDeg/2
		f.MaxLng = mid + minDeg/2
	}
	return f
}

// aspectFit expands the SHORTER projected axis (centered) so the frame's
// projected aspect (lngSpan·cosLatMid : latSpan) equals target. Only ever
// grows a span, so the country is never cropped.
func aspectFit(f Frame, target float64) Frame {
	cosLat := f.cosLatMid()
	latSpan := f.MaxLat - f.MinLat
	lngSpan := f.MaxLng - f.MinLng
	projW := lngSpan * cosLat
	projH := latSpan
	if projH <= 0 || projW <= 0 {
		return f
	}
	if projW/projH < target {
		// Too tall/narrow: widen longitude.
		newLngSpan := (projH * target) / cosLat
		mid := (f.MinLng + f.MaxLng) / 2
		f.MinLng = mid - newLngSpan/2
		f.MaxLng = mid + newLngSpan/2
	} else {
		// Too wide/short: heighten latitude.
		newLatSpan := projW / target
		mid := (f.MinLat + f.MaxLat) / 2
		f.MinLat = mid - newLatSpan/2
		f.MaxLat = mid + newLatSpan/2
	}
	return f
}
