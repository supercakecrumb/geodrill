package citymap

import (
	"bytes"
	"image"
	"math"
	"testing"

	"github.com/paulmach/orb"
)

func rgb8(img image.Image, x, y int) (int, int, int) {
	r, g, b, _ := img.At(x, y).RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

func assertColor(t *testing.T, img image.Image, x, y int, wantHex string, tol int, what string) {
	t.Helper()
	wr, wg, wb := hexRGB(wantHex)
	want := [3]int{int(math.Round(wr * 255)), int(math.Round(wg * 255)), int(math.Round(wb * 255))}
	gr, gg, gb := rgb8(img, x, y)
	if abs(gr-want[0]) > tol || abs(gg-want[1]) > tol || abs(gb-want[2]) > tol {
		t.Errorf("%s: pixel (%d,%d) = rgb(%d,%d,%d), want ~%s rgb(%d,%d,%d)",
			what, x, y, gr, gg, gb, wantHex, want[0], want[1], want[2])
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func px(proj projector, lng, lat float64) (int, int) {
	x, y := proj.project(orb.Point{lng, lat})
	return int(math.Round(x)), int(math.Round(y))
}

func TestRender_PixelProbes(t *testing.T) {
	idx := loadFixtureIndex(t)
	city := City{Key: "ta:capital", Country: "TA", Lat: 5, Lng: 5}

	dc, err := Render(city, idx)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	img := dc.Image()
	proj := newProjector(BuildFrame(city, idx["TA"]))

	// Marker core at the projected city point is the red dot color.
	mx, my := px(proj, 5, 5)
	assertColor(t, img, mx, my, colorMarker, 4, "marker core")

	// A point well inside the city's country (and its frame) is the highlight
	// fill, not the muted land color.
	hx, hy := px(proj, 2, 2)
	assertColor(t, img, hx, hy, colorHighlight, 4, "highlight fill")

	// A point in-frame but off any land is ocean.
	ox, oy := px(proj, -3, 8)
	assertColor(t, img, ox, oy, colorOcean, 4, "ocean")
}

// Holes are cut with even-odd fill: the middle of Holeland's hole shows ocean.
func TestRender_HolePunchedThrough(t *testing.T) {
	idx := loadFixtureIndex(t)
	city := City{Key: "ho:rim", Country: "HO", Lat: 21, Lng: 21}

	dc, err := Render(city, idx)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	img := dc.Image()
	proj := newProjector(BuildFrame(city, idx["HO"]))

	// Center of the hole -> ocean (fill was punched out).
	holeX, holeY := px(proj, 25, 25)
	assertColor(t, img, holeX, holeY, colorOcean, 4, "hole center")

	// Between the outer ring and the hole -> highlighted land.
	landX, landY := px(proj, 21.5, 25)
	assertColor(t, img, landX, landY, colorHighlight, 4, "solid land near rim")
}

// Determinism: rendering the same inputs twice yields byte-identical PNGs.
func TestRender_Deterministic(t *testing.T) {
	idx := loadFixtureIndex(t)
	city := City{Key: "ta:capital", Country: "TA", Lat: 5, Lng: 5}

	encode := func() []byte {
		dc, err := Render(city, idx)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		var buf bytes.Buffer
		if err := WritePNG(&buf, dc); err != nil {
			t.Fatalf("WritePNG: %v", err)
		}
		return buf.Bytes()
	}

	a, b := encode(), encode()
	if !bytes.Equal(a, b) {
		t.Errorf("PNG output not deterministic: %d vs %d bytes / differing content", len(a), len(b))
	}
}

func TestRender_UnknownCountryErrors(t *testing.T) {
	idx := loadFixtureIndex(t)
	if _, err := Render(City{Key: "zz:x", Country: "ZZ", Lat: 0, Lng: 0}, idx); err == nil {
		t.Error("expected error for a country with no geometry")
	}
}
