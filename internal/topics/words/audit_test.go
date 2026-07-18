package words

// TestAuditWordsAgainstCorpus is an opt-in, READ-ONLY data audit (not a
// regular unit test), mirroring internal/tips's TestAuditTellsAgainstCorpus
// precedent: it checks every seeds/common_words.yaml entry against the real
// ingested Tatoeba corpus (content_items, kind='sentence') for two things
// (architecture §6.3):
//
//   - (a) frequency floor: the word must be independently attested (>=
//     minOccurrences) in its claimed language's ingested sentences
//     (word-boundary matched, Unicode-aware, so combining marks/diacritics
//     and multi-token phrases match correctly). The architecture doc
//     suggested tuning this ("0.05% of a language's sentences") and
//     documenting the result: a first pass at that literal 0.05%
//     (~2-3 occurrences per 5000-sentence pool) failed 90/136 entries,
//     because this deck's vocabulary — signage/directional words (avenue,
//     north, south, entrance/exit, closed, station, stop) — is
//     systematically rarer in Tatoeba's casual conversational sentences
//     than concrete nouns like city/street/road, and inflected
//     Slavic/Finnic languages rarely surface a bare dictionary form as a
//     stand-alone token. Requiring only that the word occurs at least once
//     still catches real errors (two were found this way: Slovak "да" is
//     Russian/Bulgarian/Serbian for "yes", not Slovak — Slovak is "áno";
//     Macedonian "преслав" isn't "center", that's "центар" — while
//     accepting genuinely rare-but-correct vocabulary.
//   - (b) cross-language collision: the exact spelling must not appear at
//     >=10x the frequency in a different in-scope language (in-scope =
//     every language actually used in the yaml) — such an entry can't be a
//     fair single-answer quiz item.
//
// A failing entry may be kept anyway via an explicit `audit: waived` +
// `note` in the yaml (waivers must be rare and justified — none are used in
// the current dataset; every survivor independently clears both checks
// against the live corpus, which keeps this audit mechanically
// reproducible rather than resting on a per-word subjective call. A waiver
// without a note is itself flagged, for whenever one is next needed).
// Enable with:
//
//	WORDS_AUDIT_DATABASE_URL=postgres://geodrill:geodrill@localhost:5432/geodrill?sslmode=disable \
//	  go test ./internal/topics/words/ -run TestAuditWordsAgainstCorpus -v

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// minOccurrences is the tuned frequency floor (see the package doc
	// comment above for why a raw percentage floor doesn't work for this
	// deck's vocabulary): a word must be attested at least this many times
	// in its claimed language's ingested sentences.
	minOccurrences = 1
	// collisionFactor: a different in-scope language firing at >= this
	// multiple of the claimed language's own rate makes the item ambiguous
	// (architecture §6.3).
	collisionFactor = 10.0
)

// deckLanguages returns the set of languages actually referenced in the yaml
// (sorted), so the collision check's "in-scope" comparison set always
// matches the real deck rather than a hand-maintained list that can drift.
func deckLanguages(sf seedFile) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, w := range sf.Words {
		if _, ok := seen[w.Language]; ok {
			continue
		}
		seen[w.Language] = struct{}{}
		out = append(out, w.Language)
	}
	sort.Strings(out)
	return out
}

// isWordRune reports whether r counts as "inside a word" for boundary
// purposes: letters, digits, and combining marks (so diacritics don't get
// mistaken for boundaries mid-grapheme).
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)
}

// wordBoundaryMatch reports whether needle occurs in haystack (both already
// lowercased) bounded by non-word runes (or start/end of string) on both
// sides. Only the outer edges are boundary-checked, so multi-token phrases
// ("hướng bắc") and hyphenated compounds ("željeznicka-stanica") match as a
// single unit against their own internal spaces/hyphens.
func wordBoundaryMatch(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	start := 0
	for {
		idx := strings.Index(haystack[start:], needle)
		if idx < 0 {
			return false
		}
		matchStart := start + idx
		matchEnd := matchStart + len(needle)

		leftOK := matchStart == 0
		if !leftOK {
			r, _ := utf8.DecodeLastRuneInString(haystack[:matchStart])
			leftOK = !isWordRune(r)
		}
		rightOK := matchEnd == len(haystack)
		if !rightOK {
			r, _ := utf8.DecodeRuneInString(haystack[matchEnd:])
			rightOK = !isWordRune(r)
		}
		if leftOK && rightOK {
			return true
		}
		start = matchStart + 1
		if start >= len(haystack) {
			return false
		}
	}
}

func TestAuditWordsAgainstCorpus(t *testing.T) {
	dsn := os.Getenv("WORDS_AUDIT_DATABASE_URL")
	if dsn == "" {
		t.Skip("WORDS_AUDIT_DATABASE_URL not set; skipping corpus audit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	sf, err := loadSeedFile(seedFilePath())
	if err != nil {
		t.Fatalf("load seed file: %v", err)
	}
	langs := deckLanguages(sf)

	// READ-ONLY: a plain SELECT restricted to the languages this deck
	// actually uses.
	rows, err := pool.Query(ctx, `SELECT key, payload FROM content_items WHERE kind = 'sentence' AND key = ANY($1)`, langs)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	sentences := make(map[string][]string)
	for rows.Next() {
		var key, payload string
		if err := rows.Scan(&key, &payload); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		sentences[key] = append(sentences[key], strings.ToLower(payload))
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		t.Fatalf("rows: %v", rowsErr)
	}

	type rateKey struct{ word, lang string }
	rateCache := make(map[rateKey]float64)
	hitRate := func(word, lang string) (rate float64, hits, total int) {
		total = len(sentences[lang])
		if total == 0 {
			return 0, 0, 0
		}
		rk := rateKey{word, lang}
		if r, ok := rateCache[rk]; ok {
			return r, int(r * float64(total)), total
		}
		low := strings.ToLower(word)
		for _, s := range sentences[lang] {
			if wordBoundaryMatch(s, low) {
				hits++
			}
		}
		rate = float64(hits) / float64(total)
		rateCache[rk] = rate
		return rate, hits, total
	}

	for _, w := range sf.Words {
		if len(sentences[w.Language]) == 0 {
			t.Errorf("%-4s %-24q no ingested sentences for language %q — cannot audit", w.Language, w.Word, w.Language)
			continue
		}
		ownRate, ownHits, ownTotal := hitRate(w.Word, w.Language)

		var collidesWith []string
		if ownRate > 0 {
			for _, other := range langs {
				if other == w.Language {
					continue
				}
				otherRate, _, otherTotal := hitRate(w.Word, other)
				if otherTotal == 0 || otherRate == 0 {
					continue
				}
				if otherRate >= collisionFactor*ownRate {
					collidesWith = append(collidesWith, fmt.Sprintf("%s@%.4f%%", other, otherRate*100))
				}
			}
			sort.Strings(collidesWith)
		}

		floorFails := ownHits < minOccurrences
		collisionFails := len(collidesWith) > 0

		status := "ok"
		if floorFails {
			status = "BELOW-FLOOR"
		}
		if collisionFails {
			status += "+COLLISION"
		}
		if w.Audit == "waived" {
			status += " (waived)"
		}
		t.Logf("%-4s %-24q own=%.4f%% (%d/%d) collides=%v %s", w.Language, w.Word, ownRate*100, ownHits, ownTotal, collidesWith, status)

		if !floorFails && !collisionFails {
			continue
		}
		if w.Audit == "waived" {
			if strings.TrimSpace(w.Note) == "" {
				t.Errorf("%s/%s: audit:waived with no justification note", w.Language, w.Word)
			}
			continue
		}
		if floorFails {
			t.Errorf("%s/%s: below frequency floor (%d occurrences < %d) — prune or waive with a note", w.Language, w.Word, ownHits, minOccurrences)
		}
		if collisionFails {
			t.Errorf("%s/%s: collides with %v (>=%.0fx frequency) — ambiguous single-answer item, prune or waive with a note", w.Language, w.Word, collidesWith, collisionFactor)
		}
	}
}

// TestNoCrossLanguageDuplicateSpellings guards against a future entry
// reintroducing an exact-spelling collision across two different claimed
// languages in the yaml — the class of bug that originally required
// resolving 9 known duplicates by hand (avenida, gata, nord, ulica, mesto,
// булевард, дорога, затворено, проспект — see seeds/common_words.yaml's
// header comment for the per-word resolution). Unlike
// TestAuditWordsAgainstCorpus, this needs no database: it is a plain,
// always-on unit test over the yaml file itself, because an exact
// same-spelling duplicate is a bad single-answer quiz item regardless of
// what the corpus says about either language's frequency.
func TestNoCrossLanguageDuplicateSpellings(t *testing.T) {
	sf, err := loadSeedFile(seedFilePath())
	if err != nil {
		t.Fatalf("load seed file: %v", err)
	}

	langsByWord := make(map[string][]string)
	for _, w := range sf.Words {
		langsByWord[w.Word] = append(langsByWord[w.Word], w.Language)
	}

	for word, langsForWord := range langsByWord {
		if len(langsForWord) < 2 {
			continue
		}
		sort.Strings(langsForWord)
		t.Errorf("%q is claimed by multiple languages %v — an exact cross-language duplicate is a bad single-answer quiz item; keep only the single most-associated language (or drop it entirely)", word, langsForWord)
	}
}
