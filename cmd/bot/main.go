// Command bot runs geodrill's Telegram bot: /start /train /practice /decks
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

	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/config"
	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/study"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/tips"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/topics/guesslang"
	"github.com/supercakecrumb/geodrill/internal/topics/roadside"
	"github.com/supercakecrumb/geodrill/internal/topics/specialchars"
	"github.com/supercakecrumb/geodrill/internal/topics/words"
	"github.com/supercakecrumb/geodrill/internal/train"
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
	svc := train.NewService(store, sched, tips.Provider(), time.Now().UnixNano(), nil)

	// Register every v2 topic Generator once at startup, keyed by quiz_kind
	// (architecture §8): this is the only place that imports all four topic
	// packages by name, so no topic worker or internal/study ever switches
	// on a slug. guesslang.New(store) works because *storage.Store already
	// satisfies its narrow ContentSampler interface directly (see that
	// package's New doc) — no adapter needed.
	topics.Register(guesslang.New(store))
	topics.Register(specialchars.New())
	topics.Register(roadside.New())
	topics.Register(words.New())

	studySvc := study.New(store, sched, study.GlobalRegistry, nil, time.Now().UnixNano())

	bot, err := telegram.New(telegram.Config{
		Token:   cfg.TelegramToken,
		Store:   store,
		Service: svc,
		Logger:  logger,

		// v2 wiring (architecture §5, §8 W4.3): studySvc implements all
		// three v2 service interfaces, and *storage.Store implements
		// IntroCapStore directly (internal/storage/introcap.go).
		StudyService:  studySvc,
		TopicService:  studySvc,
		TrainerV2:     studySvc,
		IntroCapStore: store,
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
