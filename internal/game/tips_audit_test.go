package game

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// auditDecks mirrors tipDecks (tips_data.go): only decks with a curated,
// corpus-audited cue set are listed here. A tell only has to discriminate
// within its own deck, so false positives are measured against deck
// siblings. It grows deck by deck alongside the tips rollout — add the next
// deck's languages here (and to tells in tips_data.go) once that group's cue
// set has been mined and hand-filtered.
var auditDecks = map[string][]string{
	"romance": {"spa", "por", "ita", "fra", "cat", "ron"},
}

// TestAuditTellsAgainstCorpus is an opt-in data audit, not a regular unit
// test: it runs every tell against the real ingested Tatoeba sentences and
// reports (a) how often each tell fires on its own language ("useless tell"
// detector), (b) how often it fires on same-deck siblings ("misleading
// uniqueness claim" detector), and (c) per-language coverage — the share of
// sentences where at least one tell matches (below that, the generic
// fallback text is shown).
//
// Enable with:
//
//	TIPS_AUDIT_DATABASE_URL=postgres://geodrill:geodrill@localhost:5432/geodrill?sslmode=disable \
//	  go test ./internal/game/ -run TestAuditTellsAgainstCorpus -v
func TestAuditTellsAgainstCorpus(t *testing.T) {
	dsn := os.Getenv("TIPS_AUDIT_DATABASE_URL")
	if dsn == "" {
		t.Skip("TIPS_AUDIT_DATABASE_URL not set; skipping corpus audit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	sentences := make(map[string][]string)
	rows, err := pool.Query(ctx, `SELECT key, payload FROM content_items WHERE kind = 'sentence'`)
	if err != nil {
		t.Fatalf("query: %v", err)
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

	// fireRate: share of lang's sentences on which the tell matches.
	fireRate := func(tell Tell, lang string) (float64, int) {
		ss := sentences[lang]
		if len(ss) == 0 {
			return 0, 0
		}
		hits := 0
		for _, low := range ss {
			if tell.matches(low, splitWords(low)) {
				hits++
			}
		}
		return float64(hits) / float64(len(ss)), len(ss)
	}

	deckNames := make([]string, 0, len(auditDecks))
	for name := range auditDecks {
		deckNames = append(deckNames, name)
	}
	sort.Strings(deckNames)

	for _, deck := range deckNames {
		langs := auditDecks[deck]
		t.Logf("=== deck %s ===", deck)
		for _, lang := range langs {
			ss := sentences[lang]
			if len(ss) == 0 {
				t.Errorf("%s: no sentences ingested", lang)
				continue
			}
			// Coverage: at least one tell matched.
			covered := 0
			for _, low := range ss {
				words := splitWords(low)
				for _, tell := range tells[lang] {
					if tell.matches(low, words) {
						covered++
						break
					}
				}
			}
			t.Logf("%s: %d sentences, coverage %.1f%%", lang, len(ss), pct(covered, len(ss)))

			for i, tell := range tells[lang] {
				own, _ := fireRate(tell, lang)
				var fp []string
				for _, sib := range langs {
					if sib == lang {
						continue
					}
					if rate, n := fireRate(tell, sib); n > 0 && rate > 0.02 {
						fp = append(fp, fmt.Sprintf("%s %.1f%%", sib, rate*100))
					}
				}
				line := fmt.Sprintf("  [%d] %-9s %-8s own %5.1f%%", i, kindName(tell.Kind), short(tell.Pattern), own*100)
				if len(fp) > 0 {
					line += "  ⚠ fires on: " + strings.Join(fp, ", ")
				}
				t.Log(line)
				if own < 0.01 {
					t.Logf("  [%d] ^ fires on <1%% of own sentences — consider dropping", i)
				}
			}
		}
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func kindName(k TellKind) string {
	switch k {
	case KindLetters:
		return "letters"
	case KindSubstring:
		return "substr"
	case KindWord:
		return "word"
	case KindSuffix:
		return "suffix"
	case KindScript:
		return "script"
	}
	return "?"
}

func short(p string) string {
	if p == "" {
		return "(script)"
	}
	return p
}
