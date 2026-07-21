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

	"github.com/supercakecrumb/geodrill/internal/citymap"
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
		logger.Info("garage object store not configured (GARAGE_* unset) — city maps render on demand but aren't persisted")
	}

	// City-map renderer for lazy, on-demand rendering (internal/citymap): built
	// once at startup from the Natural Earth basemap + the cities seed. On the
	// first send of a city map with no cached telegram_file_id and no persisted
	// object, the bot renders it in-process (and, when Garage is configured,
	// persists it) — so there's no offline batch/upload step. Nil-safe: if the
	// Natural Earth data is missing the renderer is left nil and city maps
	// degrade to text (the same posture the object store takes). Kept a concrete
	// *citymap.Renderer here so a build failure yields a TRUE nil interface in
	// telegram.Config (a typed-nil would defeat the bot's nil check).
	var mapRenderer *citymap.Renderer
	if r, rerr := citymap.NewRenderer(cfg.NaturalEarthPath, cities.CitiesSeedPath()); rerr != nil {
		logger.Warn("city-map renderer unavailable — city maps degrade to text until the Natural Earth data is present",
			"natural_earth_path", cfg.NaturalEarthPath, "error", rerr)
	} else {
		mapRenderer = r
		logger.Info("city-map renderer ready (on-demand rendering enabled)", "natural_earth_path", cfg.NaturalEarthPath)
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
	// Cities: map-based "which city is at the red marker?", answered by typing
	// the city name via the city-suggestion autocomplete index; each newly
	// discovered city shows a map + rich info card — vibe/design-cities-on-map.md.
	// The map image comes from Garage (presence decided at seed time); a
	// not-yet-synced city degrades to a text question.
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
	// country's primary capital (name + flag emoji + "cap:iso2"), every
	// special-characters language (name + "lang:iso3"), and every city (name +
	// country flag + "city:<seedkey>") — the capitals quiz's country->capital
	// direction needs capital-name suggestions, the special-characters "which
	// language uses «x»?" text question needs language-name suggestions, the
	// upcoming map-based cities question needs city-name suggestions, and the
	// OnQuery handler answers from a single global index regardless of which
	// exercise is open (see internal/suggest.NewFromSources's doc). Capitals are
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
	// City suggestions for the upcoming map-based "which city is at the
	// marker?" question (cities.SuggestCities() reads seeds/cities.yaml). Each
	// city's flag emoji + coverage come from its country, looked up by ISO2 via
	// countryByISO built once here; a city whose ISO2 isn't in the loaded
	// countries is skipped defensively (a seed referencing an unknown country).
	// These entries are INERT until that question ships — the live cities topic
	// still answers a COUNTRY.
	countryByISO := make(map[string]storage.Country, len(countries))
	for _, c := range countries {
		countryByISO[c.ISOA2] = c
	}
	suggestCities, err := cities.SuggestCities()
	if err != nil {
		return fmt.Errorf("load cities for autocomplete index: %w", err)
	}
	cityEntries := make([]suggest.CityEntry, 0, len(suggestCities))
	for _, sc := range suggestCities {
		c, ok := countryByISO[sc.Country]
		if !ok {
			logger.Debug("skipping city with unknown country for autocomplete index", "key", sc.Key, "country", sc.Country)
			continue
		}
		cityEntries = append(cityEntries, suggest.CityEntry{Key: sc.Key, Name: sc.Name, FlagEmoji: c.FlagEmoji, Coverage: c.GGCoverage})
	}
	suggestIdx := suggest.NewFromSources(countries, capitalEntries, languageEntries, cityEntries)

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

	botCfg := telegram.Config{
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
	}
	// Only set MapRenderer when the renderer actually built — assigning a
	// typed-nil *citymap.Renderer would make the interface field non-nil and
	// defeat the bot's nil check (panicking on the first uncached city map).
	if mapRenderer != nil {
		botCfg.MapRenderer = mapRenderer
	}

	bot, err := telegram.New(botCfg)
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
