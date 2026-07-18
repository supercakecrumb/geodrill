package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics/guesslang"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
	"github.com/supercakecrumb/geodrill/internal/topics/specialchars"
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

	logger.Info("seed-topics: all topic packages seeded")
	return nil
}
