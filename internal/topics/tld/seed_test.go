package tld

import (
	"strings"
	"testing"

	"github.com/supercakecrumb/geodrill/internal/topics/engine"
)

// TestLoadTLDsRealSeed parses the committed seeds/tlds.yaml and cross-checks
// it against seeds/countries.yaml: every entry references a real country, no
// iso is duplicated, and every TLD is well-formed (dot-prefixed, lowercase
// ascii). Runs without a DB so a structural break in the data fails fast.
func TestLoadTLDsRealSeed(t *testing.T) {
	sf, err := loadTLDsFile(tldsSeedPath())
	if err != nil {
		t.Fatalf("loadTLDsFile: %v", err)
	}
	if len(sf.TLDs) != 248 {
		t.Fatalf("len(tlds) = %d, want 248", len(sf.TLDs))
	}

	countries, err := engine.LoadCountriesFile(countriesSeedPath())
	if err != nil {
		t.Fatalf("LoadCountriesFile: %v", err)
	}
	countryISO := make(map[string]bool, len(countries))
	for _, c := range countries {
		countryISO[c.ISOA2] = true
	}

	seen := make(map[string]bool, len(sf.TLDs))
	for _, e := range sf.TLDs {
		if seen[e.Country] {
			t.Fatalf("duplicate tld entry for %q", e.Country)
		}
		seen[e.Country] = true
		if !countryISO[e.Country] {
			t.Fatalf("tld entry %q has no matching country in countries.yaml", e.Country)
		}
		if err := checkTLD(e.TLD); err != nil {
			t.Fatalf("tld entry %q: %v", e.Country, err)
		}
	}

	// Spot checks from the design/brief: famous and edge-case TLDs.
	spot := map[string]string{"GB": ".uk", "US": ".us", "DE": ".de", "TV": ".tv", "IO": ".io", "ME": ".me", "AI": ".ai"}
	got := make(map[string]string, len(sf.TLDs))
	for _, e := range sf.TLDs {
		got[e.Country] = e.TLD
	}
	for iso, want := range spot {
		if got[iso] != want {
			t.Fatalf("tld for %s = %q, want %q", iso, got[iso], want)
		}
	}
}

func checkTLD(tld string) error {
	if !strings.HasPrefix(tld, ".") {
		return errNotDotPrefixed
	}
	bare := strings.TrimPrefix(tld, ".")
	if len(bare) < 2 {
		return errTooShort
	}
	for _, r := range bare {
		if r < 'a' || r > 'z' {
			return errNotLowerASCII
		}
	}
	return nil
}

var (
	errNotDotPrefixed = tldFormatError("tld must start with '.'")
	errTooShort       = tldFormatError("tld too short")
	errNotLowerASCII  = tldFormatError("tld must be lowercase ascii after the dot")
)

type tldFormatError string

func (e tldFormatError) Error() string { return string(e) }

// TestLookupTables builds the real lookup tables and asserts the entries both
// descriptors' ModeText hooks close over resolve correctly.
func TestLookupTables(t *testing.T) {
	tbl, err := loadLookupTables()
	if err != nil {
		t.Fatalf("loadLookupTables: %v", err)
	}
	if tbl.countryLabels["DE"] != "Germany" {
		t.Fatalf("countryLabels[DE] = %q, want Germany", tbl.countryLabels["DE"])
	}
	if tbl.tldLabels["GB"] != ".uk" {
		t.Fatalf("tldLabels[GB] = %q, want .uk", tbl.tldLabels["GB"])
	}
	if want := []string{".uk", "uk"}; !equalStrings(tbl.tldAccept["GB"], want) {
		t.Fatalf("tldAccept[GB] = %v, want %v", tbl.tldAccept["GB"], want)
	}
	if !contains(tbl.countryAccept["US"], "USA") {
		t.Fatalf("countryAccept[US] = %v, want to contain USA", tbl.countryAccept["US"])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
