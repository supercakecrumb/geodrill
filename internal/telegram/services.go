package telegram

// services.go declares the seam between this package and the service
// layer that lands in a later wave (architecture §5, §8 W3.4/W4.3):
// StudyService (/study introductions), TopicService (/topics browser),
// Trainer (mode-aware exercises), and IntroCapStore (the /settings
// intro-cap row). Every interface here is an OPTIONAL field on Config
// (bot.go) — a nil value keeps the corresponding command/flow dormant, so
// cmd/bot (which this package cannot touch) keeps compiling and the
// legacy bot keeps working unchanged until wave 4 wires a real
// implementation. Handlers built against these interfaces are unit-tested
// with hand-written fakes, per the existing trainer/userStore pattern in
// handlers.go.
//
// userID throughout this file is the internal storage.User.ID (a uuid.UUID),
// never the Telegram user id — the same convention internal/topics.Service
// already uses.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/game"
	"github.com/supercakecrumb/geodrill/internal/suggest"
)

// ── /study — introductions (architecture §5.1) ──────────────────────────────

// IntroReason explains why a StudyService.NextIntro call did not return a
// presentable card (IntroCard.Reason != IntroOK).
type IntroReason int8

const (
	// IntroOK: Text/MediaPath are populated; render the card normally.
	IntroOK IntroReason = iota
	// IntroNoneAvailable: no unlocked, not-yet-introduced item exists for
	// this user right now (nothing left to introduce until more tiers
	// unlock or more content is ingested).
	IntroNoneAvailable
	// IntroBudgetExhausted: candidates exist, but the user's daily intro
	// cap has already been spent today.
	IntroBudgetExhausted
)

// IntroCard is one step of the /study introduction flow: either a
// presentable card (Reason == IntroOK) or a terminal state explaining why
// there is nothing to show right now, so callers can render a single "no
// card" closer generically instead of special-casing each reason.
type IntroCard struct {
	IntroID uuid.UUID // the introductions row id; echoed back via AnswerIntro
	ItemID  uuid.UUID

	// Text is the teaching blurb: the message body when MediaPath == "", or
	// the photo caption when MediaPath != "". Meaningful only when
	// Reason == IntroOK.
	Text string
	// MediaPath is a local image path; non-empty ⇒ render as a photo
	// message via Session.SendPhoto (decision 6) instead of plain text.
	MediaPath string

	// Remaining is the daily intro budget left after this card is
	// answered (0 when Reason != IntroOK).
	Remaining int
	// Reason is IntroOK unless there is nothing to present.
	Reason IntroReason
	// IntroducedToday is how many items have been introduced today so
	// far; meaningful for the "done for today" closer when Reason != IntroOK.
	IntroducedToday int
}

// IntroAck is the outcome of answering one introduction card: the
// confirmation blurb the caller edits the card's message/caption to (see
// (*Bot).handleIntroCallback), before fetching and sending the next card.
type IntroAck struct {
	Text string
}

// StudyService is the /study introduction flow, implemented by wave 4 over
// internal/topics + internal/storage. A nil StudyService on Config keeps
// /study, /introduce, and the reminder loop's introduction nudge dormant.
type StudyService interface {
	// NextIntro returns the next introduction card for userID, or a
	// terminal IntroCard (Reason != IntroOK) when nothing is available.
	NextIntro(ctx context.Context, userID uuid.UUID) (IntroCard, error)

	// AnswerIntro applies outcome to introID (engram.IntroGotIt /
	// IntroKnown / IntroTestMe — the three-button intro card) and returns
	// the confirmation to show in its place.
	AnswerIntro(ctx context.Context, userID, introID uuid.UUID, outcome engram.IntroOutcome) (IntroAck, error)

	// IntroSummary reports how many items are eligible to introduce right
	// now (available: tier-unlocked, lifecycle=new) and how much of the
	// daily budget remains (budgetLeft). The reminder loop's "M new items
	// to introduce" figure is min(available, budgetLeft).
	IntroSummary(ctx context.Context, userID uuid.UUID) (available, budgetLeft int, err error)
}

// ── /topics — tree browser (architecture §5.2) ──────────────────────────────

// TopicCrumb is one entry in a TopicView's breadcrumb, root-first, ending
// with the topic the view is showing.
type TopicCrumb struct {
	TopicID uuid.UUID
	Name    string
}

// TopicRow is one row in a topic listing (the root list, or a container's
// children): architecture §5.2's "▸ Languages   tier: 42/50 · introduced
// 48/50" line.
type TopicRow struct {
	TopicID     uuid.UUID
	Name        string
	IsQuizzable bool // false = container (drilling in lists Children); true = quizzable (drilling in lists Tiers)

	Introduced int // items with lifecycle != new, recursively under this topic
	Total      int // total items under this topic
	GoodShape  int // items meeting the architecture §4.1 "good shape" threshold

	AnyLocked  bool // true ⇒ render 🔒
	LockedTier int  // the lowest tier locked under this topic; meaningful only when AnyLocked

	HasTips bool // true ⇒ render 💡 (a topics.TipProvider exists for this topic)

	// Enabled is the user's user_topics opt-in/out flag for this topic
	// (default-on, architecture §2.10) — rendered as a ✅/⬜ prefix so a
	// disabled topic is visible from the listing without drilling in. Only
	// a quizzable topic's flag has any gating effect (see
	// internal/study.enabledQuizzableTopicIDs/disabledTopicSet); a
	// container's Enabled is cosmetic.
	Enabled bool
}

// TierRow is one per-tier progress line shown when drilling into a
// quizzable topic (architecture §4.2/§5.2).
type TierRow struct {
	Tier       int
	Total      int
	Introduced int
	GoodShape  int
	Locked     bool
}

// TopicView is the result of drilling into one topic: its breadcrumb (for
// the header) plus either Children (IsQuizzable == false, a container) or
// Tiers (IsQuizzable == true). ParentID is nil when the topic itself is a
// root topic, so the ⬆️ row then targets "top:root" instead of
// "top:up:<ParentID>" (architecture §5.4).
type TopicView struct {
	TopicID     uuid.UUID
	Name        string
	IsQuizzable bool
	Breadcrumb  []TopicCrumb // root-first, includes this topic
	ParentID    *uuid.UUID

	Children []TopicRow // populated when IsQuizzable == false
	Tiers    []TierRow  // populated when IsQuizzable == true

	// Enabled is this topic's user_topics opt-in/out flag (meaningful when
	// IsQuizzable == true — see TopicRow.Enabled's doc): the drilled-in
	// view renders a toggle row ("topen:"/"topoff:" callbacks) reflecting
	// it, since /decks' per-deck on/off affordance retired onto /topics
	// (architecture: /decks now points here instead of its own picker).
	Enabled bool

	// GroupEnabledLeaves / GroupTotalLeaves are the enabled-vs-total count
	// of QUIZZABLE descendant topics (recursive, via topic_paths) under
	// this container (meaningful when IsQuizzable == false; zero value for
	// a quizzable view). They feed the container's group-level "Turn group
	// off"/"Turn group on" button (topics_ui.go's groupToggleButton): the
	// task this closes is that turning off a whole group otherwise means
	// toggling every subtopic by hand. GroupTotalLeaves == 0 hides the
	// button entirely (a container view is only ever reached by drilling
	// through a listing that already filtered out subtrees with no
	// quizzable descendant — see internal/study.filterVisibleTopics — so
	// this should never actually be 0 in practice, but the render side
	// stays defensive about it anyway). GroupEnabledLeaves == 0 means every
	// descendant is off (button reads "Turn on"); anything else — some or
	// all enabled — means the button reads "Turn off".
	GroupEnabledLeaves int
	GroupTotalLeaves   int
}

// TopicService is the /topics tree browser, implemented by wave 4 over
// internal/topics + internal/storage. A nil TopicService keeps /topics
// dormant.
type TopicService interface {
	// Root returns the top-level topics (no parent).
	Root(ctx context.Context, userID uuid.UUID) ([]TopicRow, error)
	// Children drills into topicID: its breadcrumb plus either its child
	// topics (container) or its per-tier progress (quizzable).
	Children(ctx context.Context, userID, topicID uuid.UUID) (TopicView, error)
	// SetTopicEnabled toggles a topic on/off for a user — the /topics
	// counterpart of the retired /decks' per-deck toggle (only a quizzable
	// topic's flag has any gating effect; see TopicRow.Enabled's doc).
	SetTopicEnabled(ctx context.Context, userID, topicID uuid.UUID, enabled bool) error
	// SetSubtreeEnabled toggles EVERY quizzable topic in topicID's subtree
	// (itself + descendants) on/off for a user in one shot — the
	// container view's group-level "Turn group off"/"Turn group on" button
	// (topics_ui.go's groupToggleButton), so disabling a whole group no
	// longer means toggling every subtopic by hand.
	SetSubtreeEnabled(ctx context.Context, userID, topicID uuid.UUID, enabled bool) error
}

// ── Trainer — mode-aware exercises (architecture §1.6) ────────────────────

// PromptKind describes the outcome of NextExercise, mirroring
// train.NextKind for the exercise path.
type PromptKind int8

const (
	// PromptKindExercise: a new exercise is ready (the other Prompt
	// fields are populated).
	PromptKindExercise PromptKind = iota
	// PromptKindNothingDue: nothing is due now (DueAt may hold the next
	// due time).
	PromptKindNothingDue
	// PromptKindNoContent: due items exist but none have content ready.
	PromptKindNoContent
)

// Option is one answer option for a Prompt in ModeSingle/ModeSet. Label
// arrives already flag-prefixed by the service — this package never builds
// flag logic (see the package doc in flags.go). Index is the stable
// position used by the index-based answer callback: architecture §5.4
// reserves the "ans:"/"prac:" prefixes for a later wave's key→index
// migration, so exercises answer through their own "ans:" prefix
// instead (see dataAnswerPrefix in train.go).
type Option struct {
	Index int
	Label string
}

// Prompt is a ready-to-send exercise: mode-aware (ModeSingle/ModeSet
// render as buttons; ModeText renders as a bare "type your answer" prompt
// with no buttons) and media-aware (MediaPath non-empty ⇒ a photo message
// from birth, decision 6, instead of a text message).
type Prompt struct {
	Kind       PromptKind
	ExerciseID uuid.UUID
	// Text is the prompt body when MediaPath == "", or the photo caption
	// when MediaPath != "". Meaningful when Kind == PromptKindExercise.
	Text      string
	MediaPath string
	Mode      quiz.Mode // ModeSingle | ModeSet | ModeText
	Options   []Option  // populated for ModeSingle/ModeSet; empty for ModeText
	DueAt     time.Time // set when Kind == PromptKindNothingDue and a future due exists

	// Summary is the review-pipeline snapshot shown on /train's "nothing
	// due" idle screen (architecture: the empty-state screen should read
	// as real progress, not a dead end). Populated when
	// Kind == PromptKindNothingDue; zero value elsewhere.
	Summary DueSummary

	// Autocomplete is true when this ModeText exercise should render the
	// "⌨️ Type your answer" inline-query prefill button
	// (vibe/spike-autocomplete-inline.md) alongside its bare "Type your
	// answer" prompt — set by internal/study from the topic's
	// exercise_modes config and/or the generator's own
	// topics.Exercise.Autocomplete flag (see study.modeFromString /
	// buildExerciseForItem). Ignored outside ModeText.
	Autocomplete bool
}

// DueSummary is a compact snapshot of a user's review pipeline, attached to
// a NothingDue Prompt so /train's idle screen can show real progress
// (reviews already done, what's still coming up, what's left to learn)
// instead of a bare "nothing due" message. Every count here is computed by
// internal/study by reusing the same underlying storage counts /stats
// already reports (Stats.ReviewsToday) plus the /study introduction
// candidate count (Service.candidatesFor) — no new queries.
type DueSummary struct {
	// ReviewsToday is how many reviews the user has logged so far today,
	// in their own timezone (the same count as Stats.ReviewsToday).
	ReviewsToday int
	// ReviewsScheduled is how many Introduced/Reviewing cards are in the
	// pipeline but not yet due (i.e. scheduled for a later time today or
	// beyond) — the "coming up" figure.
	ReviewsScheduled int
	// LeftToLearn is how many active, tier-unlocked items this user has
	// not yet introduced — the same candidate count /study's IntroSummary
	// reports as "available".
	LeftToLearn int
}

// Mark is the visual state of a graded answer option (formerly the legacy
// trainer's Mark type — moved here since this package is its only user now
// that the legacy /train rendering path is gone).
type Mark int

const (
	// MarkNone: this option was neither the answer nor the (wrong) tap.
	MarkNone Mark = iota
	// MarkCorrect: this option is the correct answer (✅).
	MarkCorrect
	// MarkWrong: this option is the wrong one the user tapped (❌).
	MarkWrong
)

// DecorateLabel prefixes a button label with its grade mark.
func DecorateLabel(label string, m Mark) string {
	switch m {
	case MarkCorrect:
		return "✅ " + label
	case MarkWrong:
		return "❌ " + label
	default:
		return label
	}
}

// DataNoop is the inert callback used on already-graded buttons (formerly
// the legacy trainer's DataNoop constant).
const DataNoop = "noop"

// GradedOption is one Option after grading, for the in-place edit.
type GradedOption struct {
	Index int
	Label string
	Mark  Mark
}

// AnswerResult is the outcome of grading a tap (Answer) or a typed
// answer (AnswerText).
type AnswerResult struct {
	Stale   bool // the exercise was already answered (or unknown) — show a toast
	Correct bool

	// Text/MediaPath re-render the exercise message for the in-place edit:
	// MediaPath != "" ⇒ the caller must use EditCaption (photo), otherwise
	// EditMessage (text) — mirroring Prompt's own Text/MediaPath split.
	Text      string
	MediaPath string
	Options   []GradedOption

	MessageID  int64
	HasMessage bool
}

// Trainer supplements trainer with the mode-aware exercise path
// (architecture §1.6: single/set/text). A nil Trainer keeps /train on the
// legacy path and leaves the free-text OnText handler unregistered (see
// bot.go's New).
type Trainer interface {
	NextExercise(ctx context.Context, userID uuid.UUID) (Prompt, error)
	Answer(ctx context.Context, userID, exerciseID uuid.UUID, optionIndex int) (AnswerResult, error)
	// AnswerText grades a free-typed message against the caller's single
	// open ModeText exercise (answered_at IS NULL, newest). ok=false means
	// there is no such exercise — the caller must treat the message as
	// ordinary, unhandled text rather than silently swallowing it.
	AnswerText(ctx context.Context, userID uuid.UUID, typed string) (result AnswerResult, ok bool, err error)
	// Stats builds the /stats view model over reviews/user_items — the
	// counterpart of the legacy trainer.Stats.
	Stats(ctx context.Context, userID uuid.UUID) (Stats, error)
	// DueCount reports how many of the user's cards (user_items in
	// lifecycle Introduced/Reviewing) are due right now — the
	// counterpart of the legacy trainer.DueCount, feeding the reminder
	// loop's due-review count (architecture §5.3).
	DueCount(ctx context.Context, userID uuid.UUID) (int, error)
}

// ── /stats — view model ───────────────────────────────────────────────

// TopicAccuracy is per-topic accuracy for /stats (the counterpart of
// the legacy DeckAccuracy).
type TopicAccuracy struct {
	Name     string
	Total    int
	Correct  int
	Accuracy float64 // 0..1; 0 when Total == 0
}

// ConfusionRow is one "you mistake X for Y" line for /stats, computed
// over item-based attempts (quiz.Confusion).
// TargetLabel/ChosenLabel are resolved via a best-effort global item
// key->label map (see storage.Store.ListAllItemKeyLabels): they fall back
// to the raw key when no item currently carries it.
type ConfusionRow struct {
	TargetLabel string
	ChosenLabel string
	Count       int
	Share       float64
}

// Stats is the /stats view model, computed over reviews/user_items
// (the counterpart of the legacy train.Stats): ByTopic replaces ByDeck,
// and Introduced/Known are new (architecture §2.3 lifecycle counts).
type Stats struct {
	ReviewsToday int
	ReviewsWeek  int
	Streak       int
	Accuracy     float64 // overall, 0..1
	ByTopic      []TopicAccuracy
	DueForecast  []int          // due counts for the next N days (index 0 = today)
	Confusion    []ConfusionRow // top pairs, most-confused first
	Introduced   int            // items with lifecycle != new
	Known        int            // items with lifecycle == known

	// Tier is the user's current tier: the lowest tier not yet complete
	// (internal/study.currentTierFrom's gating-frontier definition — tiers
	// unlock as a set, not a strict ladder, so this is the compact,
	// single-number read of "how far the user has gotten"). MaxTier is the
	// highest tier that exists in the item catalog at all, so /stats can
	// render "Tier X of Y".
	Tier    int
	MaxTier int
}

// ── /settings — daily intro cap ─────────────────────────────────────────────

// ── /game — the game zone (vibe/design-game-zone.md) ────────────────────

// GameService is the game zone's engine surface (internal/telegram/game.go
// is its only caller). Implemented by NewGameService over
// internal/game.Engine — game.Language/game.Round are internal/game's own
// domain types, reused directly here (the same way this file already
// reuses engram/quiz.Mode) rather than redeclared as a parallel shape.
type GameService interface {
	// Catalog returns the current Language Roulette catalog — every seeded
	// language, grouped by deck (internal/game.LoadCatalog). Load once per
	// run and reuse across its rounds; the topic tree rarely changes at
	// runtime.
	Catalog(ctx context.Context) ([]game.Language, error)
	// NextRound builds one round for a run in progress: streak drives the
	// difficulty ramp (design doc table) and used is the set of content
	// ids already shown this run (no repeats). ok=false means no content
	// is available at all right now.
	NextRound(ctx context.Context, userID uuid.UUID, catalog []game.Language, streak int, used map[uuid.UUID]bool) (game.Round, bool, error)
	// FinishRun persists the end of a run (best streak only ever grows,
	// runs increments by one) and returns the updated aggregate for the
	// "final streak, personal best, runs played" closer.
	FinishRun(ctx context.Context, userID uuid.UUID, streak int, at time.Time) (bestStreak, runs int, err error)
}

// ── inline-mode autocomplete (vibe/spike-autocomplete-inline.md) ───────────

// Suggester answers inline-query autocomplete lookups (telebot.OnQuery,
// bot.go's handleQuery) with ranked, typo-tolerant, answer-domain-scoped
// matches — internal/suggest.Index satisfies this directly (its own
// MatchDomain/DomainForAnswer methods have this exact shape); this package
// declares its own copy of the interface, per this file's
// narrow-dependency-interface convention, so handleQuery is unit-testable
// against a hand-written fake instead of a real Index. A nil Suggester on
// Config keeps telebot.OnQuery unregistered entirely (bot.go's New,
// mirroring Trainer's OnText gate) — a query never reaches an unconfigured
// bot.
type Suggester interface {
	// MatchDomain ranks matches scoped to domain (suggest.DomainCountry or
	// suggest.DomainCapital) — handleQuery always calls this instead of a
	// domain-agnostic Match, so suggestions never mix countries and
	// capitals regardless of which direction's exercise is open (kanban
	// card "Autocomplete must be scoped to the question's answer domain").
	// ggOnly additionally drops non-coverage countries when the querying
	// user has the GeoGuessr-only filter on (queryScope reads it).
	MatchDomain(query string, domain suggest.Domain, ggOnly bool, limit int) []suggest.Suggestion
	// DomainForAnswer resolves which suggest.Domain an open exercise's
	// CorrectAnswer belongs to (country-first membership — see
	// suggest.Index.DomainForAnswer's doc comment), so handleQuery knows
	// which Domain to pass to MatchDomain.
	DomainForAnswer(correct string) suggest.Domain
}

// TierRecomputer rebuilds a user's whole cached tier-progress gating set —
// the /settings GeoGuessr-only toggle (handleGGOnlyToggle) calls it after
// flipping users.gg_only, because tier totals depend on that per-user flag
// and the gating cache (AllowedTiers) would otherwise stay stale until the
// next answer. internal/study.Service satisfies it (RecomputeTiers). A nil
// TierRecomputer keeps the toggle's persistence working but skips the eager
// cache rebuild (the next answer/introduction recomputes the touched tier
// anyway), the same nil-safe convention every other optional Config field
// follows.
type TierRecomputer interface {
	RecomputeTiers(ctx context.Context, userID uuid.UUID) error
}

// IntroCapStore is the narrow settings surface for the daily intro cap
// (architecture §2.10 users.daily_intro_cap). It is kept separate from
// userStore (handlers.go) because storage.User does not carry this column
// yet — internal/storage is out of scope for this package. A nil
// IntroCapStore keeps the /settings intro-cap row dormant (unrendered).
type IntroCapStore interface {
	GetIntroCap(ctx context.Context, userID uuid.UUID) (int, error)
	SetIntroCap(ctx context.Context, userID uuid.UUID, cap int) error
}
