package tips

// mine_test.go is the reusable corpus miner used to bootstrap each deck's
// cue list during the staged tips rollout: it finds, per language, words
// that are frequent in that language and near-absent in every deck sibling
// — candidates for word cues. Results are printed for hand-filtering (drop
// proper nouns and corpus artifacts) before the survivors are added to
// data.go's tells map. It iterates auditDecks (see audit_test.go), so
// re-running it against a newly curated deck picks up that deck's languages
// automatically.
// Run: TIPS_AUDIT_DATABASE_URL=... go test ./internal/tips/ -run TestMine -v

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

func TestMineDiscriminatingWords(t *testing.T) {
	dsn := os.Getenv("TIPS_AUDIT_DATABASE_URL")
	if dsn == "" {
		t.Skip("TIPS_AUDIT_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// share[lang][word] = fraction of lang's sentences containing word.
	share := make(map[string]map[string]float64)
	counts := make(map[string]int)
	rows, err := pool.Query(ctx, `SELECT key, payload FROM content_items WHERE kind = 'sentence'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	docfreq := make(map[string]map[string]int)
	for rows.Next() {
		var key, payload string
		if err := rows.Scan(&key, &payload); err != nil {
			t.Fatal(err)
		}
		counts[key]++
		if docfreq[key] == nil {
			docfreq[key] = make(map[string]int)
		}
		seen := map[string]bool{}
		for _, w := range splitWords(strings.ToLower(payload)) {
			if len([]rune(w)) < 2 || seen[w] {
				continue
			}
			seen[w] = true
			docfreq[key][w]++
		}
	}
	for lang, df := range docfreq {
		share[lang] = make(map[string]float64, len(df))
		for w, n := range df {
			share[lang][w] = float64(n) / float64(counts[lang])
		}
	}

	// Existing cue patterns, to skip.
	have := map[string]map[string]bool{}
	for lang, ts := range tells {
		have[lang] = map[string]bool{}
		for _, tl := range ts {
			have[lang][tl.Pattern] = true
		}
	}

	type cand struct {
		w   string
		own float64
		sib float64 // worst sibling share
	}
	deckNames := make([]string, 0, len(auditDecks))
	for n := range auditDecks {
		deckNames = append(deckNames, n)
	}
	sort.Strings(deckNames)
	for _, deck := range deckNames {
		langs := auditDecks[deck]
		fmt.Printf("=== %s ===\n", deck)
		for _, lang := range langs {
			var cands []cand
			for w, own := range share[lang] {
				if own < 0.012 || have[lang][w] {
					continue
				}
				worst := 0.0
				ok := true
				for _, sib := range langs {
					if sib == lang {
						continue
					}
					s := share[sib][w]
					if s > worst {
						worst = s
					}
					if s > own*0.12 || s > 0.004 {
						ok = false
						break
					}
				}
				if ok {
					cands = append(cands, cand{w, own, worst})
				}
			}
			sort.Slice(cands, func(i, j int) bool { return cands[i].own > cands[j].own })
			if len(cands) > 12 {
				cands = cands[:12]
			}
			parts := make([]string, len(cands))
			for i, c := range cands {
				parts[i] = fmt.Sprintf("%s %.1f%%(sib %.2f%%)", c.w, c.own*100, c.sib*100)
			}
			fmt.Printf("%-4s: %s\n", lang, strings.Join(parts, " · "))
		}
	}
}
