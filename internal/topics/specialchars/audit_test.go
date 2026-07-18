package specialchars

// TestAuditCharsAgainstCorpus is an opt-in data audit (mirrors
// internal/tips/audit_test.go's precedent), not a regular unit test: it runs
// every seeds/special_chars.yaml claim against the real ingested Tatoeba
// corpus (content_items, kind='sentence') and fails, listing every
// violation, when a claim isn't backed by the data.
//
// READ-ONLY: this test issues nothing but SELECT statements. It is gated on
// SPECIALCHARS_AUDIT_DATABASE_URL specifically so it can be pointed at the
// live dev database (the corpus is only fully ingested there) without the
// destructive testDSN/freshSchema machinery in seed_test.go ever touching
// it — enable with:
//
//	SPECIALCHARS_AUDIT_DATABASE_URL=postgres://geodrill:geodrill@localhost:5432/geodrill?sslmode=disable \
//	  go test ./internal/topics/specialchars/ -run TestAuditCharsAgainstCorpus -v

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// scriptDeckLanguages lists the in-scope geodrill deck languages (seeds/decks.yaml)
// sharing each script, as actually ingested into content_items — i.e. the
// script of the corpus text itself, not a language's theoretical dual-script
// capability (Serbian/Macedonian CAN be written in Latin, but this deck's
// ingested Tatoeba sentences for srp/mkd are Cyrillic — verified against the
// live corpus before writing this test). This is the sibling pool the
// near-absence check (b) runs against for every char claim of that script.
var scriptDeckLanguages = map[string][]string{
	"latin": {
		"spa", "por", "ita", "fra", "cat", "ron", // romance
		"pol", "ces", "slk", "hrv", "slv", // slavic-latin
		"swe", "nor", "dan", "isl", "fin", // nordic
		"vie", "ind", // se-asia (Latin-script members)
		"msa", // malay-indonesian
	},
	"cyrillic": {"rus", "ukr", "bul", "srp", "mkd"}, // slavic-cyrillic
}

const (
	// claimedFireFloor: a claimed language's sentences must contain the char
	// at least this often, or the claim isn't backed by the corpus. 0.5% is
	// deliberately low (not "every sentence uses this letter") since even a
	// genuinely-used diacritic can be absent from most short sentences —
	// it just must not be vanishingly rare.
	claimedFireFloor = 0.005

	// siblingAbsenceCeil: a same-script sibling NOT in the claimed set must
	// stay under this share, or the "unique to language(s) X" claim is
	// contradicted by the sibling actually using the letter too. An order of
	// magnitude below claimedFireFloor so genuine rare cross-contamination
	// (loanwords, proper nouns, OCR noise in the corpus) doesn't trip it.
	siblingAbsenceCeil = 0.0005
)

// siblingException documents one (char, sibling-language) pair that clears
// siblingAbsenceCeil in the corpus but was manually inspected (every
// matching sentence read, not just the count) and found NOT to warrant
// adding Lang to the item's claimed languages. Two distinct reasons show up
// in practice — both are legitimate, neither should silence a failure
// without first reading the actual matching sentences (SELECT payload FROM
// content_items WHERE key=<lang> AND payload ILIKE '%<char>%'):
//
//   - corpus noise: every hit is a foreign proper noun or a
//     recognized-but-marginal loanword spelling, not native orthography
//     (ę/hrv, ö/dan, š/fin below) — the "unique to the claimed language(s)"
//     fact still holds, only the corpus sample is noisy.
//   - genuine but sub-floor: the sibling really does use the char (not
//     noise), but at a rate below claimedFireFloor — too rare in THIS
//     corpus to add as a claimed language (per the same floor every claimed
//     language must clear), so it stays a documented non-claim rather than
//     a silent contradiction (õ/vie below: Vietnamese genuinely uses õ, e.g.
//     "rõ"/"theo dõi", at 0.44% — under the 0.5% floor).
type siblingException struct {
	Char, Lang, Reason string
}

var knownSiblingExceptions = []siblingException{
	{"ę", "hrv", `3/4555 hits, all Polish surnames in sentences about family members ("Zaręba", "Zarębówna", "Zarębowa") — not Croatian orthography (noise)`},
	{"ö", "dan", `3/5000 hits, all foreign proper nouns ("Eyjafjallajökull", "Özgür", "Björk") — not Danish orthography (noise)`},
	{"š", "fin", `4/5000 hits, recognized-but-marginal loanword spellings ("šamppanja", "matkašekeillä", "jiddišin", "šekit") — not core Finnish orthography (noise)`},
	{"õ", "vie", `22/5000 hits (0.44%), genuine Vietnamese usage (e.g. "rõ", "theo dõi") but below the 0.5% claimed-language floor — not added as a claimed language (genuine, sub-floor)`},
}

func knownSiblingException(char, lang string) (string, bool) {
	for _, e := range knownSiblingExceptions {
		if e.Char == char && e.Lang == lang {
			return e.Reason, true
		}
	}
	return "", false
}

func TestAuditCharsAgainstCorpus(t *testing.T) {
	dsn := os.Getenv("SPECIALCHARS_AUDIT_DATABASE_URL")
	if dsn == "" {
		t.Skip("SPECIALCHARS_AUDIT_DATABASE_URL not set; skipping corpus audit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	sentences := loadSentences(ctx, t, pool)

	sf, err := LoadSeedFile("../../../seeds/special_chars.yaml")
	if err != nil {
		t.Fatalf("load seed file: %v", err)
	}
	if len(sf.Chars) == 0 {
		t.Fatalf("seed file has no chars — test fixture is broken")
	}

	for _, c := range sf.Chars {
		c := c
		t.Run(fmt.Sprintf("%s_%s", c.Char, strings.Join(c.Languages, "+")), func(t *testing.T) {
			claimed := make(map[string]bool, len(c.Languages))
			for _, l := range c.Languages {
				claimed[l] = true
			}

			for _, lang := range c.Languages {
				ss := sentences[lang]
				if len(ss) == 0 {
					t.Errorf("claimed language %s has no ingested sentences — cannot verify claim", lang)
					continue
				}
				rate, hits := fireRate(c.Char, ss)
				if rate < claimedFireFloor {
					t.Errorf("claimed language %s: fire rate %.3f%% (%d/%d sentences) below floor %.2f%%",
						lang, rate*100, hits, len(ss), claimedFireFloor*100)
				} else {
					t.Logf("claimed language %s: fire rate %.3f%% (%d/%d sentences) — OK", lang, rate*100, hits, len(ss))
				}
			}

			for _, lang := range scriptDeckLanguages[c.Script] {
				if claimed[lang] {
					continue
				}
				ss := sentences[lang]
				if len(ss) == 0 {
					continue // not ingested; nothing to check
				}
				rate, hits := fireRate(c.Char, ss)
				if rate >= siblingAbsenceCeil {
					if reason, ok := knownSiblingException(c.Char, lang); ok {
						t.Logf("sibling %s: fires on %.3f%% (%d/%d sentences) but reviewed, documented exception: %s",
							lang, rate*100, hits, len(ss), reason)
						continue
					}
					t.Errorf("NOT claimed for sibling %s but fires on %.3f%% (%d/%d sentences), >= ceiling %.3f%% — uniqueness claim contradicted",
						lang, rate*100, hits, len(ss), siblingAbsenceCeil*100)
				}
			}
		})
	}
}

// loadSentences pulls every ingested sentence, lower-cased, grouped by
// language key. One query for the whole corpus (a few hundred thousand short
// strings) is far cheaper than one query per (char, language) pair.
func loadSentences(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string][]string {
	t.Helper()
	sentences := make(map[string][]string)
	rows, err := pool.Query(ctx, `SELECT key, payload FROM content_items WHERE kind = 'sentence'`)
	if err != nil {
		t.Fatalf("query content_items: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, payload string
		if err := rows.Scan(&key, &payload); err != nil {
			t.Fatalf("scan: %v", err)
		}
		sentences[key] = append(sentences[key], strings.ToLower(payload))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return sentences
}

// fireRate returns the share (and raw count) of sentences containing char
// (case-insensitively — sentences are pre-lowercased by loadSentences, so
// char is lowercased here to match).
func fireRate(char string, sentences []string) (rate float64, hits int) {
	lc := strings.ToLower(char)
	for _, s := range sentences {
		if strings.Contains(s, lc) {
			hits++
		}
	}
	return float64(hits) / float64(len(sentences)), hits
}
