package citymap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paulmach/orb"
)

func TestImageFileName(t *testing.T) {
	cases := map[string]string{
		"de:munich":    "de-munich.png",
		"us:new-york":  "us-new-york.png",
		"nocolon":      "nocolon.png",
		"a:b:c":        "a-b-c.png",
		"ru:sankt:pet": "ru-sankt-pet.png",
	}
	for in, want := range cases {
		if got := ImageFileName(in); got != want {
			t.Errorf("ImageFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// loadFixtureIndex decodes the hand-written testdata GeoJSON into the country
// index the render/frame tests share. Never touches the 24 MB real dataset.
func loadFixtureIndex(t *testing.T) map[string]orb.MultiPolygon {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "countries.geojson"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	idx, err := DecodeCountryIndex(data)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return idx
}
