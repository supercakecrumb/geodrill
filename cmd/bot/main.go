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
	"github.com/supercakecrumb/geodrill/internal/feedback"
	"github.com/supercakecrumb/geodrill/internal/game"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/storage/objstore"
	"github.com/supercakecrumb/geodrill/internal/study"
	"github.com/supercakecrumb/geodrill/internal/suggest"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/capitals"
	"github.com/supercakecrumb/geodrill/internal/topics/cities"
	"github.com/supercakecrumb/geodrill/internal/topics/flags"
	"github.com/supercakecrumb/geodrill/internal/topics/profiles"
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

	// Garage object store for city-map images (vibe/design-cities.md). Wired
	// only when GARAGE_* is configured; left nil otherwise so SendPhoto's
	// garage:// branch degrades cleanly (city maps aren't sent) while
	// disk-backed flags keep working. The interface-typed var stays a true nil
	// interface when unset so telegram's nil check holds.
	var objStore objstore.Store
	if cfg.GarageConfigured() {
		s, err := objstore.New(cfg.GarageEndpoint, cfg.GarageRegion, cfg.GarageAccessKey, cfg.GarageSecretKey)
		if err != nil {
			return fmt.Errorf("build object store: %w", err)
		}
		objStore = s
		logger.Info("garage object store configured", "endpoint", cfg.GarageEndpoint, "bucket", cfg.GarageBucket)
	} else {
		logger.Info("garage object store not configured (GARAGE_* unset) — S3-backed images will not be sent")
	}

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
	// Country profiles: single-choice country->language quiz with
	// same-region distractors — vibe/design-country-profiles.md, narrowed to
	// one quizzable topic by the P3.3 task brief (main_religion/region are
	// seeded as facts for future sibling quizzes but have no Generator yet).
	topics.Register(profiles.New())
	// Cities first slice: "which country is this city from?", one direction,
	// answered via the existing global country-suggestion index (no city
	// entries added to it) — vibe/design-cities.md's task-brief override.
	topics.Register(cities.New())
	// Flags: flag photo -> country name (autocomplete), plus confusable-flag
	// set-choice — vibe/design-flags-quiz.md. flags.New() resolves images
	// against flags.DefaultMediaRoot ("data/flags", relative to the process's
	// working directory — cmd/bot runs from the repo root).
	topics.Register(flags.New())

	studySvc := study.New(store, sched, study.GlobalRegistry, nil, time.Now().UnixNano())

	// gameEngine powers /game's Language Roulette (internal/game.Engine):
	// *storage.Store satisfies both its ContentSampler and StatsStore
	// dependencies directly, the same "no adapter needed" pattern the topic
	// Generators use.
	gameEngine := game.NewEngine(store, store)

	// suggestIdx powers inline-mode autocomplete answers
	// (vibe/spike-autocomplete-inline.md): built once at startup as ONE
	// merged index over every country (name + flag emoji + iso2), every
	// country's primary capital (name + flag emoji + "cap:iso2"), and every
	// special-characters language (name + "lang:iso3") — the capitals quiz's
	// country->capital direction needs capital-name suggestions, the
	// special-characters "which language uses «x»?" text question needs
	// language-name suggestions, and the OnQuery handler answers from a single
	// global index regardless of which exercise is open (see
	// internal/suggest.NewFromCountriesCapitalsAndLanguages's doc). Capitals are
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
		capitalEntries = append(capitalEntries, suggest.CapitalEntry{CountryISO: c.ISOA2, Name: *f.ValText, FlagEmoji: c.FlagEmoji, Coverage: c.GGCoverage})
	}
	// Language suggestions for the special-characters "which language uses
	// «x»?" text questions (specialchars.Languages() is the read-only view of
	// that topic's alias table). Merged into the same global index so the
	// single OnQuery handler can answer language, capital, and country
	// questions alike.
	languageEntries := make([]suggest.LanguageEntry, 0)
	for _, l := range specialchars.Languages() {
		languageEntries = append(languageEntries, suggest.LanguageEntry{Code: l.Code, Name: l.Name})
	}
	suggestIdx := suggest.NewFromCountriesCapitalsAndLanguages(countries, capitalEntries, languageEntries)

	// snagbox feedback reporting ([[snagbox-integration]]): wired only when
	// both SNAGBOX_URL and SNAGBOX_INGEST_TOKEN are set, otherwise left nil so
	// /feedback degrades to "not available" (telegram.Config.Feedback is
	// nil-safe). The interface-typed var stays a true nil interface when
	// unset — a typed-nil *feedback.Reporter would defeat the handler's nil
	// check. The ingest token is write-only and scoped to geodrill's project;
	// it's read here from the environment and never logged.
	var feedbackReporter telegram.FeedbackReporter
	if cfg.SnagboxURL != "" && cfg.SnagboxIngestToken != "" {
		feedbackReporter = feedback.New(cfg.SnagboxURL, cfg.SnagboxIngestToken)
		logger.Info("snagbox feedback reporting enabled")
	} else {
		logger.Info("snagbox feedback reporting disabled (SNAGBOX_URL/SNAGBOX_INGEST_TOKEN unset)")
	}

	bot, err := telegram.New(telegram.Config{
		Token:  cfg.TelegramToken,
		Store:  store,
		Logger: logger,

		// studySvc serves every service interface (architecture §5, §8
		// W4.3a: study.Service is now the ONLY exercise/answer/stats engine
		// — the legacy trainer it replaced is gone), and *storage.Store
		// implements IntroCapStore directly (internal/storage/introcap.go).
		StudyService:   studySvc,
		TopicService:   studySvc,
		Trainer:        studySvc,
		IntroCapStore:  store,
		TierRecomputer: studySvc,
		Game:           telegram.NewGameService(gameEngine, store, time.Now().UnixNano()),
		Suggest:        suggestIdx,
		Feedback:       feedbackReporter,
		Objects:        objStore,
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
