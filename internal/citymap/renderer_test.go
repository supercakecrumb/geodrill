package citymap

import (
	"bytes"
	"image"
	_ "image/png" // registers the PNG decoder image.DecodeConfig needs
	"os"
	"path/filepath"
	"testing"
)

// TestKeyFromImageFileNameRoundTrip: KeyFromImageFileName inverts ImageFileName
// for real (single-colon) city keys, including slugs that themselves contain a
// hyphen.
func TestKeyFromImageFileNameRoundTrip(t *testing.T) {
	keys := []string{
		"de:munich",
		"us:new-york",
		"cn:bao-an", // slug with an internal hyphen — first-'-' split must be exact
		"fr:paris",
		"nocolon", // colon-free key round-trips unchanged
	}
	for _, key := range keys {
		file := ImageFileName(key)
		if got := KeyFromImageFileName(file); got != key {
			t.Errorf("round-trip %q: ImageFileName=%q, KeyFromImageFileName=%q, want %q", key, file, got, key)
		}
	}
}

func TestKeyFromImageFileName_Direct(t *testing.T) {
	cases := map[string]string{
		"cn-bao-an.png":   "cn:bao-an",
		"de-munich.png":   "de:munich",
		"us-new-york.png": "us:new-york",
		"nocolon.png":     "nocolon",
	}
	for in, want := range cases {
		if got := KeyFromImageFileName(in); got != want {
			t.Errorf("KeyFromImageFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeCitiesSeed writes a minimal cities.yaml with the given entries and
// returns its path.
func writeCitiesSeed(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cities.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write cities seed: %v", err)
	}
	return path
}

// TestRenderer_RenderPNG drives the runtime renderer against the tiny testdata
// fixture (never the 24 MB real Natural Earth file), so it runs in the gate.
func TestRenderer_RenderPNG(t *testing.T) {
	seed := writeCitiesSeed(t, `cities:
  - key: ta:capital
    country: TA
    lat: 5
    lng: 5
  - key: zz:nowhere
    country: ZZ
    lat: 0
    lng: 0
`)
	r, err := NewRenderer(filepath.Join("testdata", "countries.geojson"), seed)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	// A city with coords and a country in the index renders real PNG bytes.
	data, ok, err := r.RenderPNG("ta:capital")
	if err != nil {
		t.Fatalf("RenderPNG(ta:capital): %v", err)
	}
	if !ok {
		t.Fatalf("RenderPNG(ta:capital): ok=false, want true")
	}
	if cfg, _, derr := image.DecodeConfig(bytes.NewReader(data)); derr != nil {
		t.Fatalf("rendered bytes are not a valid PNG: %v", derr)
	} else if cfg.Width != ImageWidth || cfg.Height != ImageHeight {
		t.Fatalf("rendered PNG is %dx%d, want %dx%d", cfg.Width, cfg.Height, ImageWidth, ImageHeight)
	}

	// A city at (0,0) is treated as coordinate-less: ok=false, no error.
	if _, ok, err := r.RenderPNG("zz:nowhere"); err != nil || ok {
		t.Fatalf("RenderPNG(zz:nowhere) = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	// An unknown key is ok=false, no error (caller falls back to text).
	if _, ok, err := r.RenderPNG("xx:unknown"); err != nil || ok {
		t.Fatalf("RenderPNG(xx:unknown) = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	// Keys() exposes every seeded key, sorted.
	if got := r.Keys(); len(got) != 2 || got[0] != "ta:capital" || got[1] != "zz:nowhere" {
		t.Fatalf("Keys() = %v, want [ta:capital zz:nowhere]", got)
	}
}

// TestNewRenderer_MissingNaturalEarth: a missing Natural Earth file is a clear
// error so the caller can treat the renderer as unavailable (nil).
func TestNewRenderer_MissingNaturalEarth(t *testing.T) {
	seed := writeCitiesSeed(t, "cities: []\n")
	if _, err := NewRenderer(filepath.Join(t.TempDir(), "does-not-exist.geojson"), seed); err == nil {
		t.Fatalf("expected an error for a missing Natural Earth file")
	}
}
