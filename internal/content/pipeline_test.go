package content

import (
	"strings"
	"testing"
)

func TestFilterCandidates(t *testing.T) {
	longEnoughSpa := "La gente se olvidará de nosotros mañana." // > 20 runes, Latin script

	tsv := strings.Join([]string{
		// valid spa candidate
		"1\tspa\t" + longEnoughSpa,
		// duplicate text (same as line 1) - should be deduped
		"2\tspa\t" + longEnoughSpa,
		// too short for spa (well under 20 runes)
		"3\tspa\tHola.",
		// wrong lang - filtered out because opts.Lang == "spa"
		"4\tfra\t" + longEnoughSpa,
		// spa text containing Cyrillic letters - fails script check
		"5\tspa\tHola мир como estas hoy aqui",
		// malformed line - skipped
		"not a valid tsv line",
		// blank line - skipped
		"",
		// another distinct valid spa candidate
		"6\tspa\tEl tiempo pasa muy rápido cuando estamos ocupados.",
	}, "\n")

	got, err := FilterCandidates(strings.NewReader(tsv), FilterOptions{
		Lang: "spa",
		Min:  DefaultMinLen,
		Max:  DefaultMaxLen,
		Cap:  100,
		Seed: 42,
	})
	if err != nil {
		t.Fatalf("FilterCandidates() error = %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("FilterCandidates() returned %d candidates, want 2: %+v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Text != longEnoughSpa {
		t.Errorf("got[0] = %+v, want ID=1 Text=%q", got[0], longEnoughSpa)
	}
	if got[1].ID != "6" {
		t.Errorf("got[1].ID = %q, want 6", got[1].ID)
	}
}

func TestFilterCandidates_capIsRespected(t *testing.T) {
	var lines []string
	base := "El tiempo pasa muy rápido cuando estamos ocupados hoy"
	for i := 0; i < 50; i++ {
		// vary the text slightly so dedupe doesn't collapse them
		lines = append(lines, "id\tspa\t"+base+" "+string(rune('a'+i%26))+string(rune('A'+i)))
	}
	tsv := strings.Join(lines, "\n")

	got, err := FilterCandidates(strings.NewReader(tsv), FilterOptions{
		Lang: "spa",
		Min:  DefaultMinLen,
		Max:  DefaultMaxLen,
		Cap:  10,
		Seed: 7,
	})
	if err != nil {
		t.Fatalf("FilterCandidates() error = %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("FilterCandidates() returned %d candidates, want cap=10", len(got))
	}
}

func TestFilterCandidates_emptyInput(t *testing.T) {
	got, err := FilterCandidates(strings.NewReader(""), FilterOptions{
		Lang: "spa", Min: DefaultMinLen, Max: DefaultMaxLen, Cap: 10, Seed: 1,
	})
	if err != nil {
		t.Fatalf("FilterCandidates() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FilterCandidates(empty) = %+v, want empty", got)
	}
}
