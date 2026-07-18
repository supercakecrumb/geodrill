// Command ingest populates geodrill's decks, skills, and content_items from
// seeds/decks.yaml and the Tatoeba per-language sentence exports
// (architecture contract §6).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/supercakecrumb/geodrill/internal/config"
	"github.com/supercakecrumb/geodrill/internal/content"
	"github.com/supercakecrumb/geodrill/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ingest: "+err.Error())
		os.Exit(1)
	}
}

type langSummary struct {
	lang       string
	candidates int
	poolSize   int
	err        error
}

func run() error {
	seedsPath := flag.String("seeds", "seeds/decks.yaml", "path to the deck/skill seed YAML file")
	langsFlag := flag.String("langs", "", "comma-separated ISO-639-3 codes to ingest (empty = every language across all decks in the seed file)")
	dataDir := flag.String("data", "data", "directory used to cache downloaded Tatoeba dumps")
	capN := flag.Int("cap", 5000, "max content rows kept per language")
	minLen := flag.Int("min", content.DefaultMinLen, "minimum sentence length in runes (inclusive)")
	maxLen := flag.Int("max", content.DefaultMaxLen, "maximum sentence length in runes (inclusive)")
	seedN := flag.Int64("seed", 42, "seed for the deterministic sample used when capping candidates")
	skipDownload := flag.Bool("skip-download", false, "use only cached dumps in -data; fail if a language's dump isn't cached")
	seedOnly := flag.Bool("seed-only", false, "only upsert decks/skills from the seed file; skip content ingest")
	backfillV2 := flag.Bool("backfill-v2", false, "map legacy skills/user_skills/exercises/reviews onto the v2 topics/items framework in the same database, then exit; requires languages/guess-the-language to already be seeded (see -seed-topics) — skips download/ingest entirely")
	seedTopics := flag.Bool("seed-topics", false, "seed every v2 topic package's topics/items (specialchars, guesslang, words, roadside) against the target database, then exit — skips download/ingest and legacy deck/skill seeding entirely")
	flag.Parse()

	cfg, err := config.Load(false)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := config.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx := context.Background()

	logger.Info("applying migrations")
	if err := storage.MigrateUp(storage.MigrateURL(cfg.DatabaseURL)); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	store, err := storage.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	if *seedTopics {
		logger.Info("seed-topics mode: skipping download/ingest and legacy deck/skill seeding")
		return runSeedTopics(ctx, logger, store)
	}

	if *backfillV2 {
		logger.Info("backfill-v2 mode: skipping download/ingest")
		_, err := runBackfillV2(ctx, logger, store)
		return err
	}

	seeds, err := content.LoadSeeds(*seedsPath)
	if err != nil {
		return fmt.Errorf("load seeds: %w", err)
	}

	if err := seedDecksAndSkills(ctx, logger, store, seeds); err != nil {
		return fmt.Errorf("seed decks/skills: %w", err)
	}

	if *seedOnly {
		logger.Info("seed-only mode: skipping content ingest")
		return nil
	}

	langs := resolveLanguages(*langsFlag, seeds)
	if len(langs) == 0 {
		return fmt.Errorf("no languages to ingest (seed file has none and -langs is empty)")
	}

	labels := seeds.LanguageLabels()

	summaries := make([]langSummary, 0, len(langs))
	for _, lang := range langs {
		s := ingestLanguage(ctx, logger, store, lang, labels[lang], *dataDir, *skipDownload, content.FilterOptions{
			Lang: lang,
			Min:  *minLen,
			Max:  *maxLen,
			Cap:  *capN,
			Seed: *seedN,
		})
		summaries = append(summaries, s)
	}

	printSummary(summaries)

	for _, s := range summaries {
		if s.err != nil {
			return fmt.Errorf("%d language(s) failed to ingest; see log above", countErrors(summaries))
		}
	}
	return nil
}

// seedDecksAndSkills upserts every deck and its skills from the seed file.
func seedDecksAndSkills(ctx context.Context, logger *slog.Logger, store *storage.Store, seeds content.SeedFile) error {
	for _, d := range seeds.Decks {
		deck, err := store.UpsertDeck(ctx, d.Slug, d.Name)
		if err != nil {
			return fmt.Errorf("upsert deck %q: %w", d.Slug, err)
		}
		logger.Info("upserted deck", "slug", deck.Slug, "id", deck.ID)

		for lang, label := range d.Languages {
			skill, err := store.UpsertSkill(ctx, deck.ID, lang, label)
			if err != nil {
				return fmt.Errorf("upsert skill %s/%s: %w", d.Slug, lang, err)
			}
			logger.Info("upserted skill", "deck", deck.Slug, "key", skill.Key, "label", skill.Label)
		}
	}
	return nil
}

// resolveLanguages returns the sorted, de-duplicated list of language codes
// to ingest: the -langs flag if set, otherwise every language across all
// decks in the seed file.
func resolveLanguages(langsFlag string, seeds content.SeedFile) []string {
	var langs []string
	if strings.TrimSpace(langsFlag) != "" {
		seen := make(map[string]struct{})
		for _, l := range strings.Split(langsFlag, ",") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if _, ok := seen[l]; ok {
				continue
			}
			seen[l] = struct{}{}
			langs = append(langs, l)
		}
	} else {
		langs = seeds.Languages()
	}
	sort.Strings(langs)
	return langs
}

// ingestLanguage downloads (unless skipDownload), filters, and inserts
// content for one language, then reports the resulting pool size.
func ingestLanguage(ctx context.Context, logger *slog.Logger, store *storage.Store, lang, label, dataDir string, skipDownload bool, opts content.FilterOptions) langSummary {
	sum := langSummary{lang: lang}
	start := time.Now()

	path, err := content.DownloadDump(ctx, dataDir, lang, skipDownload)
	if err != nil {
		logger.Error("download failed", "lang", lang, "error", err)
		sum.err = err
		return sum
	}

	reader, f, err := content.OpenDecompressed(path)
	if err != nil {
		logger.Error("decompress failed", "lang", lang, "error", err)
		sum.err = err
		return sum
	}
	defer f.Close()

	candidates, err := content.FilterCandidates(reader, opts)
	if err != nil {
		logger.Error("filter failed", "lang", lang, "error", err)
		sum.err = err
		return sum
	}
	sum.candidates = len(candidates)

	for _, c := range candidates {
		if err := store.InsertContent(ctx, "sentence", lang, c.Text, "tatoeba#"+c.ID, c.Runes); err != nil {
			logger.Error("insert content failed", "lang", lang, "sentence_id", c.ID, "error", err)
			sum.err = err
			return sum
		}
	}

	poolSize, err := store.CountContentByKey(ctx, "sentence", lang)
	if err != nil {
		logger.Error("count content failed", "lang", lang, "error", err)
		sum.err = err
		return sum
	}
	sum.poolSize = poolSize

	logger.Info("ingested language",
		"lang", lang,
		"label", label,
		"candidates", sum.candidates,
		"pool_size", sum.poolSize,
		"elapsed", time.Since(start).String(),
	)
	return sum
}

func countErrors(summaries []langSummary) int {
	n := 0
	for _, s := range summaries {
		if s.err != nil {
			n++
		}
	}
	return n
}

func printSummary(summaries []langSummary) {
	fmt.Println()
	fmt.Println("lang\tcandidates\tpool_size\tstatus")
	for _, s := range summaries {
		status := "ok"
		if s.err != nil {
			status = "FAILED: " + s.err.Error()
		}
		fmt.Printf("%s\t%d\t%d\t%s\n", s.lang, s.candidates, s.poolSize, status)
	}
}
