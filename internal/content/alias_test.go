package content

import "strings"

import "testing"

func TestDownloadCode(t *testing.T) {
	if got := DownloadCode("nor"); got != "nob" {
		t.Fatalf("nor should download as nob, got %q", got)
	}
	if got := DownloadCode("spa"); got != "spa" {
		t.Fatalf("non-aliased key should be identity, got %q", got)
	}
}

// A nor-keyed filter must accept nob-tagged TSV rows and store them under nor.
func TestFilterCandidates_NorwegianAlias(t *testing.T) {
	tsv := "1\tnob\tJeg liker å lese bøker om kvelden.\n" +
		"2\tnno\tEg likar å lesa bøker.\n" + // Nynorsk (nno) must be skipped
		"3\tswe\tJag gillar att läsa böcker.\n" // wrong language, skipped
	got, err := FilterCandidates(strings.NewReader(tsv), FilterOptions{Lang: "nor", Min: 5, Max: 120, Cap: 100, Seed: 1})
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("expected only the nob row (id 1), got %+v", got)
	}
}
