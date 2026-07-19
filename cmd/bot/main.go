// Command bot runs geodrill's Telegram bot: /start /train /decks
// /stats plus the daily-reminder loop (architecture contract §5, §7).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/config"
	"github.com/supercakecrumb/geodrill/internal/game"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/study"
	"github.com/supercakecrumb/geodrill/internal/suggest"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/capitals"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
	"github.com/supercakecrumb/geodrill/internal/topics/specialchars"
	"github.com/supercakecrumb/geodrill/internal/topics/tld"
	"github.com/supercakecrumb/geodrill/internal/topics/words"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bot: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(true)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := config.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("applying migrations")
	if err := storage.MigrateUp(storage.MigrateURL(cfg.DatabaseURL)); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	store, err := storage.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	sched := engram.NewScheduler(engram.WithRetention(cfg.FSRSRetention))

	// Register every topic Generator once at startup, keyed by quiz_kind
	// (architecture §8): this is the only place that imports every topic
	// package by name, so no topic worker or internal/study ever switches
	// on a slug. guesslang no longer registers a Generator here — its
	// exercise moved into the game zone (internal/game, wired separately
	// below) — vibe/design-game-zone.md.
	topics.Register(specialchars.New())
	topics.Register(roadside.New())
	topics.Register(words.New())
	// The TLD quiz is two sibling topics (both directions), each its own
	// quiz_kind/Generator: tld->country (autocomplete country answers) and
	// country->tld (typed free text) — vibe/design-tlds.md.
	topics.Register(tld.NewTLDToCountry())
	topics.Register(tld.NewCountryToTLD())
	// The capitals quiz is likewise two sibling topics, both directions
	// answered via autocomplete: country->capital (typed capital city name)
	// and capital->country (typed country name) — see
	// internal/topics/capitals's package doc.
	topics.Register(capitals.NewCountryToCapital())
	topics.Register(capitals.NewCapitalToCountry())

	studySvc := study.New(store, sched, study.GlobalRegistry, nil, time.Now().UnixNano())

	// gameEngine powers /game's Language Roulette (internal/game.Engine):
	// *storage.Store satisfies both its ContentSampler and StatsStore
	// dependencies directly, the same "no adapter needed" pattern the topic
	// Generators use.
	gameEngine := game.NewEngine(store, store)

	// suggestIdx powers inline-mode autocomplete answers
	// (vibe/spike-autocomplete-inline.md): built once at startup as ONE
	// merged index over every country (name + flag emoji + iso2) plus every
	// country's primary capital (name + flag emoji + "cap:iso2") — the
	// capitals quiz's country->capital direction needs capital-name
	// suggestions, and the OnQuery handler answers from a single global
	// index regardless of which exercise is open (see
	// internal/suggest.NewFromCountriesAndCapitals's doc). Capitals are
	// sourced from the already-seeded country_facts row
	// (capitals.FactKeyCapital), the same "query the store, don't re-parse
	// yaml here" pattern countries themselves use.
	countries, err := store.ListCountries(ctx)
	if err != nil {
		return fmt.Errorf("list countries for autocomplete index: %w", err)
	}
	capitalFacts, err := store.ListCountryFactsByDefKey(ctx, capitals.FactKeyCapital)
	if err != nil {
		return fmt.Errorf("list capital facts for autocomplete index: %w", err)
	}
	countryByID := make(map[uuid.UUID]storage.Country, len(countries))
	for _, c := range countries {
		countryByID[c.ID] = c
	}
	capitalEntries := make([]suggest.CapitalEntry, 0, len(capitalFacts))
	for _, f := range capitalFacts {
		if f.ValText == nil {
			continue
		}
		c, ok := countryByID[f.CountryID]
		if !ok {
			continue
		}
		capitalEntries = append(capitalEntries, suggest.CapitalEntry{CountryISO: c.ISOA2, Name: *f.ValText, FlagEmoji: c.FlagEmoji})
	}
	suggestIdx := suggest.NewFromCountriesAndCapitals(countries, capitalEntries)

	bot, err := telegram.New(telegram.Config{
		Token:  cfg.TelegramToken,
		Store:  store,
		Logger: logger,

		// studySvc serves every service interface (architecture §5, §8
		// W4.3a: study.Service is now the ONLY exercise/answer/stats engine
		// — the legacy trainer it replaced is gone), and *storage.Store
		// implements IntroCapStore directly (internal/storage/introcap.go).
		StudyService:  studySvc,
		TopicService:  studySvc,
		Trainer:       studySvc,
		IntroCapStore: store,
		Game:          telegram.NewGameService(gameEngine, store, time.Now().UnixNano()),
		Suggest:       suggestIdx,
	})
	if err != nil {
		return fmt.Errorf("build bot: %w", err)
	}

	logger.Info("bot starting")
	if err := bot.Start(ctx); err != nil {
		return fmt.Errorf("bot: %w", err)
	}
	logger.Info("bot stopped cleanly")
	return nil
}
