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

	// Snagbox wires the /feedback command into the snagbox issue-intake
	// service ([[snagbox-integration]]). Both are OPTIONAL: with either
	// unset, /feedback degrades to a "feedback isn't available" reply rather
	// than failing startup — the same nil-safe posture every optional bot
	// dependency takes. The ingest token is write-only, scoped to geodrill's
	// snagbox project, and stays server-side (never shipped to clients).
	SnagboxURL         string // SNAGBOX_URL (e.g. https://snagbox.example.com)
	SnagboxIngestToken string // SNAGBOX_INGEST_TOKEN (write-only, per-project)

	// Garage is the S3-compatible object store holding city-map PNGs
	// ([[deployment]], vibe/design-cities.md). ALL fields are OPTIONAL and
	// server-side only (sourced from OpenBao in production): with them unset
	// the bot simply can't fetch S3-backed images, so city maps degrade to
	// text — the same nil-safe posture Snagbox takes above. Load never fails
	// when they're absent; GarageConfigured() reports whether enough is set
	// to build an object-store client. The keys are secrets — never logged.
	GarageEndpoint  string // GARAGE_S3_ENDPOINT (e.g. http://garage:3900)
	GarageRegion    string // GARAGE_S3_REGION (default "garage" when unset)
	GarageAccessKey string // GARAGE_ACCESS_KEY_ID
	GarageSecretKey string // GARAGE_SECRET_ACCESS_KEY
	GarageBucket    string // GARAGE_BUCKET (default "apps-geodrill" when unset)
}

// GarageConfigured reports whether the Garage object store has enough
// configuration to build a client: an endpoint plus both credentials. Region
// and bucket always have defaults (see Load), so they don't gate this.
func (c Config) GarageConfigured() bool {
	return c.GarageEndpoint != "" && c.GarageAccessKey != "" && c.GarageSecretKey != ""
}

// Load reads configuration from the environment. requireToken controls whether
// TELEGRAM_TOKEN must be present (the bot needs it; the ingest tool does not).
func Load(requireToken bool) (Config, error) {
	cfg := Config{
		TelegramToken: os.Getenv("TELEGRAM_TOKEN"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		LogLevel:      slog.LevelInfo,
		FSRSRetention: 0.9,

		SnagboxURL:         os.Getenv("SNAGBOX_URL"),
		SnagboxIngestToken: os.Getenv("SNAGBOX_INGEST_TOKEN"),

		GarageEndpoint:  os.Getenv("GARAGE_S3_ENDPOINT"),
		GarageRegion:    os.Getenv("GARAGE_S3_REGION"),
		GarageAccessKey: os.Getenv("GARAGE_ACCESS_KEY_ID"),
		GarageSecretKey: os.Getenv("GARAGE_SECRET_ACCESS_KEY"),
		GarageBucket:    os.Getenv("GARAGE_BUCKET"),
	}

	// Region and bucket have stable defaults so callers never have to set
	// them for a standard Garage deployment; the credentials/endpoint stay
	// unset-means-disabled (GarageConfigured).
	if cfg.GarageRegion == "" {
		cfg.GarageRegion = "garage"
	}
	if cfg.GarageBucket == "" {
		cfg.GarageBucket = "apps-geodrill"
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
