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
	"github.com/supercakecrumb/geodrill/internal/telegram"
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
	svc := train.NewService(store, sched, time.Now().UnixNano(), nil)

	bot, err := telegram.New(telegram.Config{
		Token:   cfg.TelegramToken,
		Store:   store,
		Service: svc,
		Logger:  logger,
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
