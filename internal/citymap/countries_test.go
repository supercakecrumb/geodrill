package citymap

import (
	"reflect"
	"testing"
)

func TestDecodeCountryIndex_Keys(t *testing.T) {
	idx := loadFixtureIndex(t)
	for _, iso := range []string{"TA", "FR", "NO", "ZA", "US", "FJ", "HO"} {
		if len(idx[iso]) == 0 {
			t.Errorf("expected geometry for %q, got none", iso)
		}
	}
	if _, ok := idx["-99"]; ok {
		t.Errorf("the -99 placeholder must never become an index key")
	}
}

// FR and NO carry ISO_A2_EH == ISO_A2 == "-99" in Natural Earth; only the
// name-keyed override recovers them.
func TestDecodeCountryIndex_NameOverride(t *testing.T) {
	idx := loadFixtureIndex(t)
	if len(idx["FR"]) == 0 {
		t.Fatal("France not recovered via name override")
	}
	if len(idx["NO"]) == 0 {
		t.Fatal("Norway not recovered via name override")
	}
}

// ZA has ISO_A2_EH == "-99" but a valid plain ISO_A2 — the fallback path.
func TestDecodeCountryIndex_PlainFallback(t *testing.T) {
	idx := loadFixtureIndex(t)
	if len(idx["ZA"]) == 0 {
		t.Fatal("ZA not recovered via ISO_A2 fallback")
	}
}

func TestDecodeCountryIndex_MultiPolygonNormalized(t *testing.T) {
	idx := loadFixtureIndex(t)
	if got := len(idx["US"]); got != 2 {
		t.Errorf("US should have 2 polygon components, got %d", got)
	}
	// A plain Polygon feature normalizes to a single-component MultiPolygon.
	if got := len(idx["TA"]); got != 1 {
		t.Errorf("TA (Polygon) should normalize to 1 component, got %d", got)
	}
}

func TestAuditISOCoverage(t *testing.T) {
	idx := loadFixtureIndex(t)

	// Missing code is reported; present codes (any case) are not.
	got := AuditISOCoverage(idx, []string{"TA", "ta", "FR", "XX", "XX", ""})
	want := []string{"XX"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuditISOCoverage = %v, want %v", got, want)
	}

	// All present -> empty.
	if got := AuditISOCoverage(idx, []string{"US", "FJ"}); len(got) != 0 {
		t.Errorf("expected no missing, got %v", got)
	}

	// Multiple missing -> sorted, deduped.
	got = AuditISOCoverage(idx, []string{"ZZ", "AA", "ZZ"})
	want = []string{"AA", "ZZ"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AuditISOCoverage = %v, want %v", got, want)
	}
}
