package coverage

import "testing"

// deciderFixture builds a Decider over a small mapping and name sets:
//   - fra → French, spoken in a covered country → covered
//   - swa → Swahili, in the fact data but only in non-coverage countries → hidden
//   - tam → Tamil, absent from the fact data entirely → undeterminable/kept
func deciderFixture() *Decider {
	m := Mapping{
		"fra": {"French"},
		"swa": {"Swahili"},
		"tam": {"Tamil"},
		"cmn": {"Chinese", "Mandarin"},
	}
	covered := []string{"French", "Mandarin"}        // spoken in covered countries
	all := []string{"French", "Mandarin", "Swahili"} // every language in the fact data
	return NewDecider(m, covered, all)
}

func TestRelevant_CoveredLanguage(t *testing.T) {
	d := deciderFixture()
	rel, undet := d.Relevant([]string{"fra"})
	if !rel || undet {
		t.Fatalf("French should be covered (relevant, not undeterminable), got rel=%v undet=%v", rel, undet)
	}
}

func TestRelevant_NonCoveredButKnownLanguage(t *testing.T) {
	d := deciderFixture()
	rel, undet := d.Relevant([]string{"swa"})
	if rel || undet {
		t.Fatalf("Swahili is in the data but only non-coverage → should be hidden, got rel=%v undet=%v", rel, undet)
	}
}

func TestRelevant_UndeterminableLanguageKept(t *testing.T) {
	d := deciderFixture()
	rel, undet := d.Relevant([]string{"tam"})
	if !rel || !undet {
		t.Fatalf("Tamil absent from data → conservatively kept+flagged, got rel=%v undet=%v", rel, undet)
	}
}

func TestRelevant_UnmappedCodeKept(t *testing.T) {
	d := deciderFixture()
	rel, undet := d.Relevant([]string{"zzz"})
	if !rel || !undet {
		t.Fatalf("unmapped code → conservatively kept+flagged, got rel=%v undet=%v", rel, undet)
	}
}

func TestRelevant_MultiCodeAnyCoveredWins(t *testing.T) {
	d := deciderFixture()
	// One covered code among non-covered/undeterminable ones → relevant.
	rel, undet := d.Relevant([]string{"swa", "fra"})
	if !rel || undet {
		t.Fatalf("any covered code should make the item relevant, got rel=%v undet=%v", rel, undet)
	}
}

func TestRelevant_MandarinAliasCovered(t *testing.T) {
	d := deciderFixture()
	// cmn maps to both "Chinese" and "Mandarin"; only "Mandarin" is covered,
	// which is enough.
	rel, undet := d.Relevant([]string{"cmn"})
	if !rel || undet {
		t.Fatalf("Mandarin alias should be covered, got rel=%v undet=%v", rel, undet)
	}
}

func TestRelevant_NoCodesKept(t *testing.T) {
	d := deciderFixture()
	rel, undet := d.Relevant(nil)
	if !rel || !undet {
		t.Fatalf("no codes → kept+undeterminable, got rel=%v undet=%v", rel, undet)
	}
}

func TestRelevant_CaseInsensitive(t *testing.T) {
	// Mapping/name matching is casefolded, so odd-cased fact spellings match.
	d := NewDecider(Mapping{"fra": {"FRENCH"}}, []string{"french"}, []string{"french"})
	rel, undet := d.Relevant([]string{"FRA"})
	if !rel || undet {
		t.Fatalf("case-insensitive match failed, got rel=%v undet=%v", rel, undet)
	}
}

// TestLoad_RealSeedFile parses the committed seeds/language_coverage.yaml so a
// malformed edit is caught, and sanity-checks a couple of known mappings.
func TestLoad_RealSeedFile(t *testing.T) {
	m, err := Load("../../seeds/language_coverage.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := m["rus"]; len(got) == 0 || got[0] != "Russian" {
		t.Fatalf("rus mapping = %v, want [Russian]", got)
	}
	if got := m["cmn"]; len(got) < 2 {
		t.Fatalf("cmn should map to multiple names (Chinese/Mandarin), got %v", got)
	}
}
