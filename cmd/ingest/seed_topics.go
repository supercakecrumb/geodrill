package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/supercakecrumb/geodrill/internal/coverage"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/capitals"
	"github.com/supercakecrumb/geodrill/internal/topics/cities"
	"github.com/supercakecrumb/geodrill/internal/topics/flags"
	"github.com/supercakecrumb/geodrill/internal/topics/guesslang"
	"github.com/supercakecrumb/geodrill/internal/topics/profiles"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
	"github.com/supercakecrumb/geodrill/internal/topics/specialchars"
	"github.com/supercakecrumb/geodrill/internal/topics/tld"
	"github.com/supercakecrumb/geodrill/internal/topics/words"
)

// runSeedTopics seeds every topic package's data (topics + items) against
// store, in an order that satisfies -backfill's precondition (the
// languages/guess-the-language tree must exist before that mode can map
// legacy skills onto it). Each package's Seed is independently idempotent
// (architecture §6), so re-running -seed-topics is always safe and simply
// converges existing rows rather than duplicating them.
func runSeedTopics(ctx context.Context, logger *slog.Logger, store *storage.Store) error {
	if err := specialchars.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed specialchars (languages/special-characters): %w", err)
	}
	logger.Info("seeded topic package", "topic", "languages/special-characters", "quiz_kind", specialchars.Kind)

	if err := guesslang.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed guesslang (languages/guess-the-language): %w", err)
	}
	logger.Info("seeded topic package", "topic", "languages/guess-the-language", "quiz_kind", guesslang.Kind)

	if err := words.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed words (languages/common-words): %w", err)
	}
	logger.Info("seeded topic package", "topic", "languages/common-words", "quiz_kind", words.QuizKind)

	if err := roadside.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed roadside (roads/which-side): %w", err)
	}
	logger.Info("seeded topic package", "topic", "roads/which-side", "quiz_kind", roadside.Kind)

	// tld runs after roadside: it resolves its country references against the
	// already-seeded country data (roadside owns country seeding) rather than
	// seeding countries itself (design §5).
	if err := tld.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed tld (countries/domains): %w", err)
	}
	logger.Info("seeded topic package", "topic", "countries/domains", "quiz_kind", tld.KindTLDToCountry+"+"+tld.KindCountryToTLD)

	// capitals runs after roadside too, for the same reason: it resolves
	// country references against the already-seeded country data rather
	// than seeding countries itself.
	if err := capitals.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed capitals (countries/capitals): %w", err)
	}
	logger.Info("seeded topic package", "topic", "countries/capitals", "quiz_kind", capitals.KindCountryToCapital+"+"+capitals.KindCapitalToCountry)

	// profiles runs after roadside too, for the same reason: it resolves
	// country references against the already-seeded country data rather
	// than seeding countries itself.
	if err := profiles.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed profiles (countries/profiles/language): %w", err)
	}
	logger.Info("seeded topic package", "topic", "countries/profiles/language", "quiz_kind", profiles.Kind)

	// cities runs after roadside too, for the same reason: it resolves
	// country references against the already-seeded country data rather
	// than seeding countries itself.
	if err := cities.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed cities (cities/city-on-map): %w", err)
	}
	logger.Info("seeded topic package", "topic", "cities/city-on-map", "quiz_kind", cities.Kind)

	// flags runs after roadside too, for the same reason: it resolves
	// country references (both single items and confusable-group members)
	// against the already-seeded country data rather than seeding countries
	// itself.
	if err := flags.Seed(ctx, store); err != nil {
		return fmt.Errorf("seed flags (flags/guess-the-flag): %w", err)
	}
	logger.Info("seeded topic package", "topic", "flags/guess-the-flag", "quiz_kind", flags.Kind)

	// GeoGuessr-only relevance pass — the single global source of truth for
	// items.gg_relevant, run after every topic (and its country links) is
	// seeded. Country-linked items mirror their country's gg_coverage;
	// language items are matched against the languages spoken in covered
	// countries (see recomputeGGRelevance).
	if err := recomputeGGRelevance(ctx, logger, store); err != nil {
		return fmt.Errorf("recompute gg_relevant: %w", err)
	}

	logger.Info("seed-topics: all topic packages seeded")
	return nil
}

// langItemPayload is the subset of a language item's payload the relevance
// pass reads: the ISO-639-3 deck code(s) the item is about. It covers all
// three language topics' payload shapes — special-characters
// ({"languages":[...]}), common-words and guess-the-language
// ({"language":"..."}).
type langItemPayload struct {
	Languages []string `json:"languages"`
	Language  string   `json:"language"`
}

// langCodes extracts the deck codes from a language item's jsonb payload.
func langCodes(payload []byte) []string {
	var p langItemPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil
	}
	codes := append([]string(nil), p.Languages...)
	if p.Language != "" {
		codes = append(codes, p.Language)
	}
	return codes
}

// recomputeGGRelevance is the two-pass computation of items.gg_relevant
// (architecture: coverage is a GLOBAL item property; users.gg_only only
// decides whether to apply it). It is idempotent — every UpsertItem writes
// gg_relevant=true, and this pass corrects it — so re-running -seed-topics
// always converges.
//
//  1. Country pass (SQL): every country-linked item's gg_relevant mirrors its
//     country's gg_coverage.
//  2. Language pass (Go): each language item (country_id IS NULL) is marked
//     relevant iff one of its languages is spoken in a covered country, using
//     the seeds/language_coverage.yaml code→name bridge and the conservative
//     "keep undeterminable languages visible" rule (internal/coverage).
func recomputeGGRelevance(ctx context.Context, logger *slog.Logger, store *storage.Store) error {
	if err := store.UpdateItemsRelevanceByCountry(ctx); err != nil {
		return fmt.Errorf("country relevance pass: %w", err)
	}

	mapping, err := coverage.Load(coverage.DefaultSeedPath)
	if err != nil {
		return err
	}
	coveredNames, err := store.ListCoveredLanguageFactValues(ctx)
	if err != nil {
		return fmt.Errorf("list covered languages: %w", err)
	}
	allNames, err := store.ListAllLanguageFactValues(ctx)
	if err != nil {
		return fmt.Errorf("list all languages: %w", err)
	}
	decider := coverage.NewDecider(mapping, coveredNames, allNames)

	items, err := store.ListLanguageItems(ctx)
	if err != nil {
		return fmt.Errorf("list language items: %w", err)
	}

	var nRelevant, nHidden int
	undeterminable := map[string]bool{}
	for _, it := range items {
		codes := langCodes(it.Payload)
		relevant, undet := decider.Relevant(codes)
		if undet {
			for _, c := range codes {
				undeterminable[c] = true
			}
		}
		if relevant {
			nRelevant++
		} else {
			nHidden++
		}
		if err := store.SetItemRelevance(ctx, it.ID, relevant); err != nil {
			return fmt.Errorf("set language item %s relevance: %w", it.ID, err)
		}
	}

	kept := make([]string, 0, len(undeterminable))
	for c := range undeterminable {
		kept = append(kept, c)
	}
	sort.Strings(kept)
	logger.Info("gg_relevant: language pass",
		"language_items", len(items),
		"relevant", nRelevant,
		"hidden", nHidden,
		"covered_names", len(coveredNames),
		"undeterminable_codes_kept", kept,
	)
	return nil
}
