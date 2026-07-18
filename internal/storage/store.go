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
	ReminderHour     int
	FollowUpEnabled  bool
	FollowUpDelayMin int
	LabelStyle       string
	Timezone         string
	CreatedAt        time.Time
	// DailyIntroCap is the v2 daily introduction budget (architecture §2.10,
	// users.daily_intro_cap). 0 = unlimited (engram.RemainingIntroBudget's
	// convention).
	DailyIntroCap int
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
	Practice         bool
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
	Practice         bool
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

// ── v2 app model types (architecture §2: topics/items/introductions/tiers) ──

// Topic is one node in the topic tree (parent_id + slug, architecture §2.1).
// ParentID is nil for a root topic.
type Topic struct {
	ID            uuid.UUID
	ParentID      *uuid.UUID
	Slug          string
	Name          string
	Position      int
	BaseTier      int16
	QuizKind      string
	ExerciseModes []string
	IsQuizzable   bool
	Config        []byte // jsonb
	CreatedAt     time.Time
}

// UserTopic pairs a topic with a user's opt-in/out flag (default-on).
type UserTopic struct {
	Topic
	Enabled bool
}

// Item is a generic quizzable entity within a topic (architecture §2.2). Tier
// is nil when the item inherits the topic's base_tier.
type Item struct {
	ID        uuid.UUID
	TopicID   uuid.UUID
	Key       string
	Label     string
	Tier      *int16
	Payload   []byte // jsonb
	CountryID *uuid.UUID
	Position  int
	Active    bool
	CreatedAt time.Time
}

// ItemWithTier pairs an item with its effective tier (COALESCE(items.tier,
// topics.base_tier), resolved via the item_tiers view).
type ItemWithTier struct {
	Item
	EffectiveTier int16
}

// IntroCandidate is one candidate row from the introduction-queue selection
// (architecture §2.9/§4.2): active, tier-unlocked, lifecycle new-or-absent.
type IntroCandidate struct {
	ItemID  uuid.UUID
	TopicID uuid.UUID
	Key     string
	Label   string
	Tier    int16
}

// UserItem is the per-user lifecycle + FSRS card state for one item
// (architecture §2.3, replaces user_skills for v2 topics).
type UserItem struct {
	UserID       uuid.UUID
	ItemID       uuid.UUID
	Lifecycle    int16 // engram.Lifecycle: 0 new,1 introduced,2 reviewing,3 known
	Card         CardFields
	IntroducedAt time.Time // zero = not yet introduced
	KnownAt      time.Time // zero = never marked known
	UpdatedAt    time.Time
}

// DueUserItem is a due UserItem joined with its item's topic/key/label.
type DueUserItem struct {
	UserItem
	TopicID uuid.UUID
	Key     string
	Label   string
}

// Introduction is one row of the append-only introduction event log
// (architecture §2.4). Outcome is nil while the card is shown but unanswered.
type Introduction struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	ItemID     uuid.UUID
	Seq        int
	Outcome    *int16 // engram.IntroOutcome: 0 got_it,1 known,2 test_me
	ShownAt    time.Time
	AnsweredAt time.Time // zero = not yet answered
	MessageID  int64
	HasMessage bool
}

// Country is a first-class ISO 3166 country or subdivision (architecture
// §2.6). Optional text fields are "" when unset.
type Country struct {
	ID              uuid.UUID
	ISOA2           string
	ISOA3           string
	NumericCode     string
	Name            string
	OfficialName    string
	FlagEmoji       string
	ParentCountryID *uuid.UUID
	IsSubdivision   bool
	UNMember        bool
	GGCoverage      bool
	CreatedAt       time.Time
}

// FactDef describes one typed, normalized country-fact key (architecture
// §2.7), e.g. 'drives_on', 'main_religion'.
type FactDef struct {
	ID          uuid.UUID
	Key         string
	Label       string
	ValueType   string // "text"|"number"|"bool"
	Unit        string
	Cardinality string // "single"|"multi"
	Dataset     string
	CreatedAt   time.Time
}

// CountryFact is one typed fact value for a country. Exactly one of
// ValText/ValNum/ValBool is non-nil (DB CHECK enforces this on write).
type CountryFact struct {
	ID         uuid.UUID
	CountryID  uuid.UUID
	FactDefID  uuid.UUID
	FactKey    string // populated by ListFactsForCountry; "" otherwise
	ValText    *string
	ValNum     *float64
	ValBool    *bool
	Source     string
	ObservedAt time.Time // zero = current (not time-series)
	CreatedAt  time.Time
}

// MediaFile is a photo asset: local path plus a cached Telegram file_id
// (architecture §2.8, decision 6). Optional fields are "" / 0 when unset.
type MediaFile struct {
	ID             uuid.UUID
	ContentID      *uuid.UUID
	LocalPath      string
	SHA256         string
	TelegramFileID string
	Width          int
	Height         int
	Bytes          int
	CreatedAt      time.Time
}

// TierProgress is the cached per-user, per-tier completion summary
// (architecture §2.9/§4.2); RecomputeTierProgress is the on-the-fly source of
// truth this cache mirrors.
type TierProgress struct {
	UserID          uuid.UUID
	Tier            int16
	TotalItems      int
	IntroducedItems int
	GoodShapeItems  int
	Complete        bool
	UpdatedAt       time.Time
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

// pgTextPtr converts an optional string (nil = SQL NULL, distinct from "")
// into pgtype.Text — used for columns where empty-string and NULL are
// meaningfully different (e.g. country_facts.val_text).
func pgTextPtr(p *string) pgtype.Text {
	if p == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *p, Valid: true}
}

// stringPtrFromPg is the read-side counterpart of pgTextPtr.
func stringPtrFromPg(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func pgInt8(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

func ptrInt8(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *p, Valid: true}
}

// int8ToMessageID converts a nullable bigint into (messageID, hasMessage),
// mirroring the Exercise.MessageID/HasMessage convention.
func int8ToMessageID(v pgtype.Int8) (int64, bool) {
	if !v.Valid {
		return 0, false
	}
	return v.Int64, true
}

func pgInt2(p *int16) pgtype.Int2 {
	if p == nil {
		return pgtype.Int2{}
	}
	return pgtype.Int2{Int16: *p, Valid: true}
}

func int2Ptr(v pgtype.Int2) *int16 {
	if !v.Valid {
		return nil
	}
	n := v.Int16
	return &n
}

func pgFloat8(p *float64) pgtype.Float8 {
	if p == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *p, Valid: true}
}

func float8Ptr(v pgtype.Float8) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func pgBool(p *bool) pgtype.Bool {
	if p == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *p, Valid: true}
}

func boolPtr(v pgtype.Bool) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Bool
	return &b
}

func pgDate(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func dateTime(d pgtype.Date) time.Time {
	if d.Valid {
		return d.Time
	}
	return time.Time{}
}

// IsNotFound reports whether err is pgx's no-rows error.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
