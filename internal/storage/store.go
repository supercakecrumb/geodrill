// Package storage is geodrill's PostgreSQL layer: a thin, engram-free wrapper
// around the sqlc-generated queries (internal/storage/db). It exposes clean app
// model types (no pgtype leakage) for the ingest tool, the bot, and the engram
// adapters (internal/storage/engramstore).
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// Store wraps a pgx pool and the generated queries.
type Store struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// New opens a pgx pool against dsn and returns a Store. Callers are responsible
// for applying migrations first (see MigrateUp).
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, q: db.New(pool)}, nil
}

// NewWithPool wraps an existing pool (used by integration tests).
func NewWithPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: db.New(pool)}
}

// Pool exposes the underlying pool.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// ── app model types ─────────────────────────────────────────────────────────

// User is a registered Telegram user with their settings.
type User struct {
	ID               uuid.UUID
	TelegramID       int64
	Username         string
	DailyNewCap      int
	RemindersEnabled bool
	Timezone         string
	CreatedAt        time.Time
}

// Deck is a confusion group.
type Deck struct {
	ID           uuid.UUID
	Slug         string
	Name         string
	ExerciseType string
}

// UserDeck is a deck with a per-user enabled flag.
type UserDeck struct {
	Deck
	Enabled bool
}

// Skill is one answer target within a deck (an ISO-639-3 language here).
type Skill struct {
	ID     uuid.UUID
	DeckID uuid.UUID
	Key    string
	Label  string
}

// CardFields is the FSRS memory state for one user+skill (engram.CardState,
// expressed with plain Go types so this package stays engram-free).
type CardFields struct {
	Due        time.Time
	Stability  float64
	Difficulty float64
	Reps       int
	Lapses     int
	State      int16
	LastReview time.Time // zero if never reviewed
}

// SkillCard pairs a skill with its optional card state. HasCard is false when
// the user has never been scheduled for this skill (treat as engram.StateNew).
type SkillCard struct {
	Skill
	Card    CardFields
	HasCard bool
}

// Content is one sentence (or, later, image ref) with attribution.
type Content struct {
	ID         uuid.UUID
	Kind       string
	Key        string
	Payload    string
	Source     string
	CharLength int
}

// Exercise is an open (or answered) multiple-choice question.
type Exercise struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	SkillID    uuid.UUID
	ContentID  uuid.UUID
	Options    []byte // jsonb: [{"key":..,"label":..}] as shown
	CreatedAt  time.Time
	AnsweredAt time.Time // zero if still open
	Answered   bool
	MessageID  int64
	HasMessage bool
}

// ReviewInsert is the full reviews row: engram.Review FSRS fields merged with
// quiz.Attempt data. This is geodrill's single review write path.
type ReviewInsert struct {
	UserID           uuid.UUID
	SkillID          uuid.UUID
	ExerciseID       *uuid.UUID
	ContentID        *uuid.UUID
	ChosenKey        string
	CorrectKey       string
	Correct          bool
	Rating           int16
	ResponseMS       *int
	StabilityBefore  float64
	DifficultyBefore float64
	StabilityAfter   float64
	DifficultyAfter  float64
	StateBefore      int16
	ScheduledDays    int
	ElapsedDays      int
	ReviewedAt       time.Time
}

// ReviewRecord is a persisted review, carrying both the FSRS fields (for
// engram.Review mapping / accuracy / streak) and the answer keys.
type ReviewRecord struct {
	SkillID          uuid.UUID
	Rating           int16
	ReviewedAt       time.Time
	StabilityBefore  float64
	DifficultyBefore float64
	StabilityAfter   float64
	DifficultyAfter  float64
	StateBefore      int16
	ScheduledDays    int
	ElapsedDays      int
	Correct          bool
	ChosenKey        string
	CorrectKey       string
}

// Attempt is the minimal per-answer record for quiz.Confusion.
type Attempt struct {
	SkillID    uuid.UUID
	CorrectKey string
	ChosenKey  string
	Correct    bool
	ResponseMS int
	AnsweredAt time.Time
}

// DeckStat is aggregate accuracy for one deck over a window.
type DeckStat struct {
	Slug    string
	Name    string
	Total   int
	Correct int
}

// ── pgtype helpers ──────────────────────────────────────────────────────────

func tsTime(t pgtype.Timestamptz) time.Time {
	if t.Valid {
		return t.Time
	}
	return time.Time{}
}

func timeTs(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func int4Int(i pgtype.Int4) int {
	if i.Valid {
		return int(i.Int32)
	}
	return 0
}

func ptrInt4(p *int) pgtype.Int4 {
	if p == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*p), Valid: true}
}

func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func pgInt8(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

// IsNotFound reports whether err is pgx's no-rows error.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
