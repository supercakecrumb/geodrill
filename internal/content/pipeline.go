package content

import (
	"bufio"
	"io"
	"unicode/utf8"
)

// FilterOptions configures the filter/dedupe/cap pipeline.
type FilterOptions struct {
	Lang string
	Min  int   // inclusive rune-count lower bound
	Max  int   // inclusive rune-count upper bound
	Cap  int   // max candidates kept per language
	Seed int64 // seed for the deterministic sample when len(candidates) > Cap
}

// FilterCandidates applies the ingest filter chain (architecture contract
// §6) to decompressed per-language TSV data read from r: length check ->
// script check -> exact-text dedupe -> seeded-sample cap. Malformed lines and
// lines whose lang field doesn't match opts.Lang are skipped. The download
// step is intentionally separate (see downloader.go) so this function is
// unit-testable from a strings.Reader with no network access.
func FilterCandidates(r io.Reader, opts FilterOptions) ([]Candidate, error) {
	var kept []Candidate

	scanner := bufio.NewScanner(r)
	// Tatoeba lines are short, but bump the buffer well past the default
	// 64KiB just in case of an outlier long sentence.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		id, lang, text, ok := ParseLine(line)
		if !ok {
			continue
		}
		if lang != opts.Lang {
			continue
		}
		if !LengthOK(text, opts.Min, opts.Max) {
			continue
		}
		if !ScriptOK(lang, text) {
			continue
		}
		kept = append(kept, Candidate{
			ID:    id,
			Text:  text,
			Runes: utf8.RuneCountInString(text),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	kept = DedupeCandidates(kept)
	kept = SampleCap(kept, opts.Cap, opts.Seed)
	return kept, nil
}
