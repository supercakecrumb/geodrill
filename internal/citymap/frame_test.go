package citymap

import (
	"math"
	"testing"

	"github.com/paulmach/orb"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestNormalizeLon(t *testing.T) {
	cases := []struct {
		lon, center, want float64
	}{
		{10, 10, 10},
		{200, 0, -160},     // 200 -> -160 in [-180,180)
		{-179, 179.5, 181}, // antimeridian: -179 joins 179's side of a Fiji frame
		{179, 179.5, 179},
		{-180, 0, -180}, // lower bound is inclusive
	}
	for _, c := range cases {
		got := normalizeLon(c.lon, c.center)
		if !approx(got, c.want, 1e-9) {
			t.Errorf("normalizeLon(%v, %v) = %v, want %v", c.lon, c.center, got, c.want)
		}
	}
}

func TestPointInRing(t *testing.T) {
	square := orb.Ring{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}
	if !pointInRing(square, orb.Point{5, 5}) {
		t.Error("center should be inside")
	}
	if pointInRing(square, orb.Point{15, 5}) {
		t.Error("point to the right should be outside")
	}
	if pointInRing(square, orb.Point{-1, 5}) {
		t.Error("point to the left should be outside")
	}
}

// A city in a tiny country gets a frame no smaller than the minimum span, so
// microstates render with neighbor context rather than a bare dot.
func TestBuildFrame_MinSpan(t *testing.T) {
	tiny := orb.MultiPolygon{{{{0, 0}, {0.2, 0}, {0.2, 0.2}, {0, 0.2}, {0, 0}}}}
	f := BuildFrame(City{Lat: 0.1, Lng: 0.1, Country: "XX"}, tiny)

	latSpan := f.MaxLat - f.MinLat
	lngSpan := f.MaxLng - f.MinLng
	if latSpan < minSpanDeg-1e-6 && lngSpan < minSpanDeg-1e-6 {
		t.Errorf("neither axis met the minimum span: lat=%.3f lng=%.3f", latSpan, lngSpan)
	}
}

// Every frame is aspect-fit to the output image ratio (in projected space).
func TestBuildFrame_AspectFit(t *testing.T) {
	sq := orb.MultiPolygon{{{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}}}
	f := BuildFrame(City{Lat: 5, Lng: 5, Country: "TA"}, sq)

	cosLat := f.cosLatMid()
	projW := (f.MaxLng - f.MinLng) * cosLat
	projH := f.MaxLat - f.MinLat
	if !approx(projW/projH, targetAspect, 1e-6) {
		t.Errorf("projected aspect = %.4f, want %.4f", projW/projH, targetAspect)
	}
}

// The city point is always inside the final frame bounds.
func TestBuildFrame_ContainsCity(t *testing.T) {
	sq := orb.MultiPolygon{{{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}}}
	f := BuildFrame(City{Lat: 5, Lng: 5, Country: "TA"}, sq)
	nl := normalizeLon(5, f.CenterLng)
	if nl < f.MinLng || nl > f.MaxLng || 5 < f.MinLat || 5 > f.MaxLat {
		t.Errorf("city not inside frame: %+v", f)
	}
}

// Antimeridian: a country straddling ±180° frames tightly, not as a ~360° bbox.
func TestBuildFrame_Antimeridian(t *testing.T) {
	idx := loadFixtureIndex(t)
	f := BuildFrame(City{Lat: -18, Lng: 179.5, Country: "FJ"}, idx["FJ"])
	if span := f.MaxLng - f.MinLng; span > 90 {
		t.Errorf("antimeridian frame longitude span too wide: %.2f (should be tight)", span)
	}
}

// Component pick: a Honolulu-like city on the island component frames on the
// island, not on a bbox that also swallows the mainland.
func TestBuildFrame_PicksContainingComponent(t *testing.T) {
	idx := loadFixtureIndex(t)
	f := BuildFrame(City{Lat: 20.5, Lng: -157.5, Country: "US"}, idx["US"])
	if f.MaxLat > 30 {
		t.Errorf("frame reached the mainland (MaxLat=%.2f); should stay on the island", f.MaxLat)
	}
}
