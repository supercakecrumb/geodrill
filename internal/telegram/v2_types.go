package telegram

// v2_types.go declares the seam between this package and the v2 service
// layer that lands in a later wave (architecture §5, §8 W3.4/W4.3):
// StudyService (/study introductions), TopicService (/topics browser),
// TrainerV2 (mode-aware exercises), and IntroCapStore (the /settings
// intro-cap row). Every interface here is an OPTIONAL field on Config
// (bot.go) — a nil value keeps the corresponding command/flow dormant, so
// cmd/bot (which this package cannot touch) keeps compiling and the
// pre-v2 bot keeps working unchanged until wave 4 wires a real
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
}

// ── TrainerV2 — mode-aware exercises (architecture §1.6) ────────────────────

// PromptV2Kind describes the outcome of NextExerciseV2, mirroring
// train.NextKind for the v2 exercise path.
type PromptV2Kind int8

const (
	// PromptV2KindExercise: a new exercise is ready (the other PromptV2
	// fields are populated).
	PromptV2KindExercise PromptV2Kind = iota
	// PromptV2KindNothingDue: nothing is due now (DueAt may hold the next
	// due time).
	PromptV2KindNothingDue
	// PromptV2KindNoContent: due (or, for /practice, tier-unlocked) items
	// exist but none have content ready.
	PromptV2KindNoContent
	// PromptV2KindNoTopics: /practice found no enabled+quizzable topics at
	// all (nudge the caller to /topics) — the v2 counterpart of the legacy
	// KindNoDecks.
	PromptV2KindNoTopics
)

// OptionV2 is one answer option for a PromptV2 in ModeSingle/ModeSet. Label
// arrives already flag-prefixed by the service — this package never builds
// flag logic (see the package doc in flags.go). Index is the stable
// position used by the index-based answer callback: architecture §5.4
// reserves the "ans:"/"prac:" prefixes for a later wave's key→index
// migration, so v2 exercises answer through their own "v2a:" prefix
// instead (see dataV2AnswerPrefix in trainv2.go).
type OptionV2 struct {
	Index int
	Label string
}

// PromptV2 is a ready-to-send v2 exercise: mode-aware (ModeSingle/ModeSet
// render as buttons; ModeText renders as a bare "type your answer" prompt
// with no buttons) and media-aware (MediaPath non-empty ⇒ a photo message
// from birth, decision 6, instead of a text message).
type PromptV2 struct {
	Kind       PromptV2Kind
	ExerciseID uuid.UUID
	// Text is the prompt body when MediaPath == "", or the photo caption
	// when MediaPath != "". Meaningful when Kind == PromptV2KindExercise.
	Text      string
	MediaPath string
	Mode      quiz.Mode  // ModeSingle | ModeSet | ModeText
	Options   []OptionV2 // populated for ModeSingle/ModeSet; empty for ModeText
	DueAt     time.Time  // set when Kind == PromptV2KindNothingDue and a future due exists

	// Practice is true for a /practice exercise (NextPracticeV2): the
	// telegram layer adds a Stop control and answers route through the
	// v2p: callback prefix instead of v2a:, mirroring the legacy
	// train.Prompt.Practice flag.
	Practice bool
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

// GradedOptionV2 is one OptionV2 after grading, for the in-place edit.
type GradedOptionV2 struct {
	Index int
	Label string
	Mark  Mark
}

// AnswerResultV2 is the outcome of grading a v2 tap (AnswerV2) or a typed
// answer (AnswerText).
type AnswerResultV2 struct {
	Stale   bool // the exercise was already answered (or unknown) — show a toast
	Correct bool

	// Text/MediaPath re-render the exercise message for the in-place edit:
	// MediaPath != "" ⇒ the caller must use EditCaption (photo), otherwise
	// EditMessage (text) — mirroring PromptV2's own Text/MediaPath split.
	Text      string
	MediaPath string
	Options   []GradedOptionV2

	MessageID  int64
	HasMessage bool

	// Practice echoes back the graded exercise's Practice flag (read from
	// storage.ExerciseV2 at answer time) — handleText has no callback
	// prefix to tell a practice tap from a scheduled one, so it reads this
	// field to decide whether the "next" step is NextPracticeV2 or
	// NextExerciseV2.
	Practice bool
}

// TrainerV2 supplements trainer with the mode-aware v2 exercise path
// (architecture §1.6: single/set/text). A nil TrainerV2 keeps /train on the
// legacy path and leaves the free-text OnText handler unregistered (see
// bot.go's New).
type TrainerV2 interface {
	NextExerciseV2(ctx context.Context, userID uuid.UUID) (PromptV2, error)
	// NextPracticeV2 generates an unscheduled practice exercise (PromptV2.
	// Practice=true) from a random active item across the caller's
	// enabled+tier-unlocked topics — the v2 counterpart of the legacy
	// trainer.NextPractice.
	NextPracticeV2(ctx context.Context, userID uuid.UUID) (PromptV2, error)
	AnswerV2(ctx context.Context, userID, exerciseID uuid.UUID, optionIndex int) (AnswerResultV2, error)
	// AnswerText grades a free-typed message against the caller's single
	// open ModeText exercise (answered_at IS NULL, newest). ok=false means
	// there is no such exercise — the caller must treat the message as
	// ordinary, unhandled text rather than silently swallowing it.
	AnswerText(ctx context.Context, userID uuid.UUID, typed string) (result AnswerResultV2, ok bool, err error)
	// Stats builds the /stats view model over v2 reviews/user_items — the
	// v2 counterpart of the legacy trainer.Stats.
	Stats(ctx context.Context, userID uuid.UUID) (StatsV2, error)
	// DueCount reports how many of the user's v2 cards (user_items in
	// lifecycle Introduced/Reviewing) are due right now — the v2
	// counterpart of the legacy trainer.DueCount, feeding the reminder
	// loop's due-review count (architecture §5.3).
	DueCount(ctx context.Context, userID uuid.UUID) (int, error)
}

// ── /stats — v2 view model ──────────────────────────────────────────────

// TopicAccuracyV2 is per-topic accuracy for /stats (the v2 counterpart of
// the legacy DeckAccuracy).
type TopicAccuracyV2 struct {
	Name     string
	Total    int
	Correct  int
	Accuracy float64 // 0..1; 0 when Total == 0
}

// ConfusionRowV2 is one "you mistake X for Y" line for /stats (the v2
// counterpart of ConfusionRow), computed over v2 attempts (quiz.Confusion).
// TargetLabel/ChosenLabel are resolved via a best-effort global item
// key->label map (see storage.Store.ListAllItemKeyLabels): they fall back
// to the raw key when no item currently carries it.
type ConfusionRowV2 struct {
	TargetLabel string
	ChosenLabel string
	Count       int
	Share       float64
}

// StatsV2 is the /stats view model, computed over v2 reviews/user_items
// (the v2 counterpart of the legacy train.Stats): ByTopic replaces ByDeck,
// and Introduced/Known are new (architecture §2.3 lifecycle counts).
type StatsV2 struct {
	ReviewsToday int
	ReviewsWeek  int
	Streak       int
	Accuracy     float64 // overall, 0..1
	ByTopic      []TopicAccuracyV2
	DueForecast  []int            // due counts for the next N days (index 0 = today)
	Confusion    []ConfusionRowV2 // top pairs, most-confused first
	Introduced   int              // items with lifecycle != new
	Known        int              // items with lifecycle == known
}

// ── /settings — daily intro cap ─────────────────────────────────────────────

// IntroCapStore is the narrow settings surface for the daily intro cap
// (architecture §2.10 users.daily_intro_cap). It is kept separate from
// userStore (handlers.go) because storage.User does not carry this column
// yet — internal/storage is out of scope for this package. A nil
// IntroCapStore keeps the /settings intro-cap row dormant (unrendered).
type IntroCapStore interface {
	GetIntroCap(ctx context.Context, userID uuid.UUID) (int, error)
	SetIntroCap(ctx context.Context, userID uuid.UUID, cap int) error
}
