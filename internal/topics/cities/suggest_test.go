package cities

import "testing"

func TestSuggestCities_LoadsPlausibleCountAndSpotEntry(t *testing.T) {
	cities, err := SuggestCities()
	if err != nil {
		t.Fatalf("SuggestCities() error = %v", err)
	}

	// The seed file carries the full GeoNames-derived dataset (~thousands of
	// cities); a floor of 4000 catches an accidentally-truncated or empty file
	// without pinning an exact count that legitimately changes as data grows.
	if len(cities) < 4000 {
		t.Fatalf("SuggestCities() returned %d cities, want >4000", len(cities))
	}

	// Spot-check a stable, well-known entry maps all three fields correctly.
	var munich *SuggestCity
	for i := range cities {
		if cities[i].Key == "de:munich" {
			munich = &cities[i]
			break
		}
	}
	if munich == nil {
		t.Fatalf("SuggestCities() missing expected entry de:munich")
	}
	if munich.Name != "Munich" || munich.Country != "DE" {
		t.Fatalf("de:munich = %+v, want Name=Munich Country=DE", *munich)
	}
}
