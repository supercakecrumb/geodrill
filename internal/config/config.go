// Package config loads geodrill runtime configuration from the environment
// (architecture contract §7).
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	TelegramToken string     // TELEGRAM_TOKEN (required for the bot)
	DatabaseURL   string     // DATABASE_URL (required)
	LogLevel      slog.Level // LOG_LEVEL (debug|info|warn|error), default info
	FSRSRetention float64    // FSRS_RETENTION, default 0.9 (engram default)
}

// Load reads configuration from the environment. requireToken controls whether
// TELEGRAM_TOKEN must be present (the bot needs it; the ingest tool does not).
func Load(requireToken bool) (Config, error) {
	cfg := Config{
		TelegramToken: os.Getenv("TELEGRAM_TOKEN"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		LogLevel:      slog.LevelInfo,
		FSRSRetention: 0.9,
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if requireToken && cfg.TelegramToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_TOKEN is required")
	}

	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		parsed, err := ParseLevel(lvl)
		if err != nil {
			return Config{}, err
		}
		cfg.LogLevel = parsed
	}

	if r := os.Getenv("FSRS_RETENTION"); r != "" {
		v, err := strconv.ParseFloat(r, 64)
		if err != nil {
			return Config{}, fmt.Errorf("FSRS_RETENTION must be a float: %w", err)
		}
		if v <= 0 || v >= 1 {
			return Config{}, fmt.Errorf("FSRS_RETENTION must be in (0,1), got %v", v)
		}
		cfg.FSRSRetention = v
	}

	return cfg, nil
}

// ParseLevel maps a textual level to slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug", "DEBUG":
		return slog.LevelDebug, nil
	case "info", "INFO":
		return slog.LevelInfo, nil
	case "warn", "WARN", "warning", "WARNING":
		return slog.LevelWarn, nil
	case "error", "ERROR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown LOG_LEVEL %q (want debug|info|warn|error)", s)
	}
}

// NewLogger builds a slog.Logger writing JSON to stderr at the configured level.
func NewLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
