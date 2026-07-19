package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// ── narrow dependency interfaces (for unit-testing without a DB) ───────────

// userStore is the subset of *storage.Store the handlers call, extracted so
// tests can stub it without a database. *storage.Store satisfies this
// structurally.
type userStore interface {
	UpsertUser(ctx context.Context, telegramID int64, username string) (storage.User, error)
	GetUserByTelegramID(ctx context.Context, telegramID int64) (storage.User, bool, error)
	SetExerciseMessageID(ctx context.Context, exerciseID uuid.UUID, messageID int64) error
	SetDailyCap(ctx context.Context, userID uuid.UUID, cap int) error
	SetReminders(ctx context.Context, userID uuid.UUID, enabled bool) error
	SetReminderHour(ctx context.Context, userID uuid.UUID, hour int) error
	SetFollowUpEnabled(ctx context.Context, userID uuid.UUID, enabled bool) error
	SetFollowUpDelay(ctx context.Context, userID uuid.UUID, minutes int) error
	SetLabelStyle(ctx context.Context, userID uuid.UUID, style string) error
	UsersWithReminders(ctx context.Context) ([]storage.User, error)
	CountReviewsSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error)
}

// dataStartTrain is the callback payload for the "Start reviewing" button on
// the daily reminder — tapping it kicks off the /train flow without the user
// typing a command.
const dataStartTrain = "train:start"

// ── daily-cap bounds ────────────────────────────────────────────────────────

const (
	minDailyCap = 0
	maxDailyCap = 500
)

const (
	minIntroCap = 0
	maxIntroCap = 200
)

// ── label-style cycle ───────────────────────────────────────────────────────

// labelStyleCycle is the order the "🔤 ..." control in /decks advances
// through: name -> code -> plain -> name.
var labelStyleCycle = []string{"name", "code", "plain"}

// nextLabelStyle returns the style after style in labelStyleCycle,
// defaulting unrecognized styles to "name".
func nextLabelStyle(style string) string {
	for i, s := range labelStyleCycle {
		if s == style {
			return labelStyleCycle[(i+1)%len(labelStyleCycle)]
		}
	}
	return labelStyleCycle[0]
}

// labelStyleButtonLabel renders the control's own label so it reflects the
// CURRENT style (tapping it cycles to the next one).
func labelStyleButtonLabel(style string) string {
	switch style {
	case "code":
		return "🔤 Flag + code"
	case "plain":
		return "🔤 Name only"
	default: // "name" and anything else
		return "🔤 Flag + name"
	}
}

// ── follow-up delay cycle ────────────────────────────────────────────────────

// followUpDelayCycle is the set of follow-up delays (minutes) the ⏱ control in
// /settings advances through.
var followUpDelayCycle = []int{30, 60, 120}

// nextFollowUpDelay returns the delay after cur in followUpDelayCycle,
// defaulting an unrecognized value to the first option.
func nextFollowUpDelay(cur int) int {
	for i, d := range followUpDelayCycle {
		if d == cur {
			return followUpDelayCycle[(i+1)%len(followUpDelayCycle)]
		}
	}
	return followUpDelayCycle[0]
}

// ── user-facing copy ────────────────────────────────────────────────────────

const welcomeText = "Hi! I'm geodrill — I'll show you short sentences in different languages " +
	"and quiz you on which language they're in, spacing out repeats so they stick.\n\n" +
	"All decks start disabled. Pick at least one below, then send /train to begin."

const noContentText = "The content for your due skills hasn't been ingested yet. Try again later, or enable a different deck via /decks."
const allCaughtUpText = "You're all caught up for now."
const fallbackText = "Something went wrong on my end. Please try again in a moment."
const staleToast = "⏳ already answered"
const correctToast = "✅ correct"
const wrongToast = "❌ wrong"
const settingsText = "⚙️ Settings — daily new-skill cap, button style, and reminders:"

// decksUnavailableText is what the retired /decks command replies with when
// TopicService isn't wired: the legacy per-deck picker was removed along
// with the decks/user_decks tables, so there's no fallback left to render.
const decksUnavailableText = "Topic browser is unavailable right now."

// help:* callback payloads: one per root-menu button (tapped from the
// /help message), plus help:root to return to that menu from any section.
const (
	dataHelpFSRS  = "help:fsrs"
	dataHelpIntro = "help:intro"
	dataHelpTiers = "help:tiers"
	dataHelpCmds  = "help:cmds"
	dataHelpRoot  = "help:root"
)

// helpRootText is the 2-3 line /help overview shown above the subtopic menu.
const helpRootText = "🌍 geodrill trains GeoGuessr-relevant knowledge — special characters, " +
	"road sides, words, and more — with spaced repetition. Pick a topic below:"

const helpFSRSText = "📚 How spaced repetition works\n\n" +
	"New items are first introduced — teaching cards you see via /study, limited by a " +
	"daily intro cap. Once introduced, an item enters the review queue and comes up in " +
	"/train on a schedule set by the FSRS algorithm.\n\n" +
	"Answer wrong (\"Again\") and it comes back sooner. Answer right (\"Good\") and it's " +
	"pushed further out — intervals grow as an item stabilizes. The daily cap in " +
	"/settings bounds how many reviews you get shown in total per day."

const helpIntroButtonsText = "🎓 The intro buttons explained\n\n" +
	"«Got it» — the item is introduced and will come up for review soon.\n\n" +
	"«I know this» — the item is marked known immediately, scheduled far out (~2 weeks) " +
	"to verify later, and does NOT consume the daily intro budget.\n\n" +
	"«Know it, but test me» — introduced like «Got it», so it shows up in reviews for " +
	"you to prove it."

const helpTiersText = "🗺 Topics and tiers\n\n" +
	"Topics form a tree you browse in /topics, where you toggle what you study.\n\n" +
	"Items have tiers 0–5: 0 is universally known, up through 4 (advanced/rare) and 5 " +
	"(expert meta). Tiers 0–1 are open from the start. Finishing a tier in good shape " +
	"(about 80% of its items solidly learned or known) unlocks the tier two levels up. " +
	"Locked tiers show a lock in /topics."

// ── /start ───────────────────────────────────────────────────────────────

func (b *Bot) handleStart(ctx context.Context, s Session) error {
	if _, err := b.store.UpsertUser(ctx, s.UserID(), s.Username()); err != nil {
		return err
	}
	if err := s.Send(welcomeText); err != nil {
		return err
	}
	// The legacy deck picker that used to follow the welcome message was
	// removed along with the decks/user_decks tables; /topics (via
	// handleTopics, which is itself nil-safe) is its replacement.
	if err := b.handleTopics(ctx, s); err != nil {
		return err
	}
	// Hub-and-spoke (Feature 1): /start ends at the same navigation hub
	// /menu sends, so a fresh user always lands somewhere every other
	// destination can be reached from.
	return b.handleMenu(ctx, s)
}

// ── /train ───────────────────────────────────────────────────────────────

func (b *Bot) handleTrain(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	return b.sendNextTrain(ctx, s, user)
}

// handleStartTrainCallback runs the /train flow from the "Start reviewing"
// button on the daily reminder: it acks the tap (clearing the button's
// spinner) and sends the next due exercise (when wired) as a new message.
func (b *Bot) handleStartTrainCallback(ctx context.Context, s Session) error {
	if err := s.Respond(""); err != nil {
		return err
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	return b.sendNextTrain(ctx, s, user)
}

// ── /decks (retired onto /topics) ────────────────────────────────────────

// handleDecks serves the retired /decks command: it now aliases /topics
// (architecture: confusion-group on/off moved to the per-topic toggle in a
// quizzable TopicView, topics_ui.go's topicToggleButton) when TopicService
// is wired, replying that the topic browser is unavailable otherwise (the
// legacy deck picker was removed along with the decks/user_decks tables).
func (b *Bot) handleDecks(ctx context.Context, s Session) error {
	if b.topics != nil {
		return b.handleTopics(ctx, s)
	}
	return s.Send(decksUnavailableText)
}

// ── /settings ─────────────────────────────────────────────────────────────

func (b *Bot) handleSettings(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	_, err = s.SendKeyboard(settingsText, settingsRows(user, b.introCapFor(ctx, user.ID)))
	return err
}

func (b *Bot) introCapFor(ctx context.Context, userID uuid.UUID) *int {
	if b.introCap == nil {
		return nil
	}
	v, err := b.introCap.GetIntroCap(ctx, userID)
	if err != nil {
		b.logger.Warn("telegram: get intro cap", "error", err)
		return nil
	}
	return &v
}

func (b *Bot) handleIntroCapChange(ctx context.Context, s Session, delta int) error {
	if b.introCap == nil {
		return s.Respond("")
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	cur, err := b.introCap.GetIntroCap(ctx, user.ID)
	if err != nil {
		return err
	}
	if err := b.introCap.SetIntroCap(ctx, user.ID, clamp(cur+delta, minIntroCap, maxIntroCap)); err != nil {
		return err
	}
	return b.rerenderSettings(ctx, s, user)
}

// rerenderSettings re-renders the settings keyboard in place (after a cap,
// style, reminder, hour, or follow-up change) and acks the callback.
func (b *Bot) rerenderSettings(ctx context.Context, s Session, user storage.User) error {
	if err := s.EditKeyboard(s.MessageID(), settingsRows(user, b.introCapFor(ctx, user.ID))); err != nil {
		return err
	}
	return s.Respond("")
}

func (b *Bot) handleReminderHourChange(ctx context.Context, s Session, delta int) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	user.ReminderHour = ((user.ReminderHour+delta)%24 + 24) % 24
	if err := b.store.SetReminderHour(ctx, user.ID, user.ReminderHour); err != nil {
		return err
	}
	return b.rerenderSettings(ctx, s, user)
}

func (b *Bot) handleFollowUpToggle(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	user.FollowUpEnabled = !user.FollowUpEnabled
	if err := b.store.SetFollowUpEnabled(ctx, user.ID, user.FollowUpEnabled); err != nil {
		return err
	}
	return b.rerenderSettings(ctx, s, user)
}

func (b *Bot) handleFollowUpDelayCycle(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	user.FollowUpDelayMin = nextFollowUpDelay(user.FollowUpDelayMin)
	if err := b.store.SetFollowUpDelay(ctx, user.ID, user.FollowUpDelayMin); err != nil {
		return err
	}
	return b.rerenderSettings(ctx, s, user)
}

// ── /help ────────────────────────────────────────────────────────────────

func (b *Bot) handleHelp(ctx context.Context, s Session) error {
	_, err := s.SendKeyboard(helpRootText, helpRootRows())
	return err
}

// handleHelpCallback renders the tapped /help subtopic (or, for help:root,
// the root menu) by editing the existing message's text and keyboard in
// place — the same in-place-edit convention rerenderSettings uses for
// /settings.
func (b *Bot) handleHelpCallback(ctx context.Context, s Session, data string) error {
	text, rows := helpSection(data, b.study != nil, b.topics != nil, b.game != nil)
	if err := s.EditMessage(s.MessageID(), text, rows); err != nil {
		return err
	}
	return s.Respond("")
}

// helpSection resolves a help:* payload to the section text + keyboard to
// render. Any payload other than the three subtopic sections and
// help:cmds — including help:root and any unrecognized value — renders the
// root menu, so a stale or malformed tap always falls back to something
// sensible instead of erroring.
func helpSection(data string, hasStudy, hasTopics, hasGame bool) (string, [][]Btn) {
	switch data {
	case dataHelpFSRS:
		return helpFSRSText, helpBackRow()
	case dataHelpIntro:
		return helpIntroButtonsText, helpBackRow()
	case dataHelpTiers:
		return helpTiersText, helpBackRow()
	case dataHelpCmds:
		return helpCommandsText(hasStudy, hasTopics, hasGame), helpBackRow()
	default:
		return helpRootText, helpRootRows()
	}
}

// helpRootRows is the /help root menu: one button per subtopic, one per
// row, plus a trailing «⬅️ Menu» row back to the hub (hub-and-spoke rule —
// help:root is the only dead end /help itself would otherwise leave: every
// subtopic already returns here via helpBackRow).
func helpRootRows() [][]Btn {
	return [][]Btn{
		{{Label: "📚 How spaced repetition works", Data: dataHelpFSRS}},
		{{Label: "🎓 The intro buttons explained", Data: dataHelpIntro}},
		{{Label: "🗺 Topics & tiers", Data: dataHelpTiers}},
		{{Label: "🧭 Commands", Data: dataHelpCmds}},
		{{Label: "⬅️ Menu", Data: dataMenuOpen}},
	}
}

// helpBackRow is the single «⬅️ Back» button shown under every subtopic
// section, returning to the root menu.
func helpBackRow() [][]Btn {
	return [][]Btn{{{Label: "⬅️ Back", Data: dataHelpRoot}}}
}

// helpCommandsText renders the "🧭 Commands" section: the always-available
// commands, plus /study, /topics, and /game only when their services are
// wired (mirroring the retired helpTextFor's hasStudy/hasTopics gating, so
// /help never advertises a command that would just reply "🚧 coming soon").
func helpCommandsText(hasStudy, hasTopics, hasGame bool) string {
	var b strings.Builder
	b.WriteString("🧭 Commands\n\n")
	b.WriteString("/train — next due review, scheduled by FSRS\n")
	if hasStudy {
		b.WriteString("/study — teaching cards for new items (✅ Got it / 🧠 I know this / 🎯 Test me); /introduce fetches more on demand\n")
	}
	if hasTopics {
		b.WriteString("/topics — browse topics, tiers, and your progress; toggle what you study\n")
	}
	if hasGame {
		b.WriteString("/game — the game zone: quick, ungraded rounds (Language Roulette) that don't touch your schedule\n")
	}
	b.WriteString("/stats — reviews, accuracy, streak, due forecast, and your top mix-ups\n")
	b.WriteString("/settings — daily new-item cap, button style, and reminders\n")
	b.WriteString("/start — register with geodrill\n")
	b.WriteString("/help — this menu\n")
	return b.String()
}

// ── /stats ───────────────────────────────────────────────────────────────

// statsDormantText is what /stats replies with when Config.Trainer is
// nil, matching the /study and /topics "coming soon" convention:
// /stats is now computed entirely over reviews/user_items (study.
// Service.Stats), so there is no legacy fallback to degrade to.
const statsDormantText = "🚧 /stats is coming soon."

func (b *Bot) handleStats(ctx context.Context, s Session) error {
	if b.trainer == nil {
		return s.Send(statsDormantText)
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	st, err := b.trainer.Stats(ctx, user.ID)
	if err != nil {
		return err
	}
	// /stats is a top-level section (hub-and-spoke rule): it had no keyboard
	// at all before, so a one-button «⬅️ Menu» keyboard is the minimal fix.
	_, err = s.SendKeyboard(formatStats(st), menuBackRow())
	return err
}

// ── /menu ────────────────────────────────────────────────────────────────

// menu: callback payloads (Feature: hub-and-spoke navigation). dataMenuOpen
// is what every «⬅️ Menu» button across the bot carries (Settings, Help
// root, Topics root, Stats, and every Study/Train/Game terminal/idle
// screen) — it re-renders the hub in place over whatever message the tap
// landed on. The per-destination payloads below are what the hub's own
// buttons carry: each acks the tap and then runs that destination's normal
// entry point, mirroring the existing ack-then-send shape of
// handleStartTrainCallback ("▶️ Start reviewing") and handleStudyCallback
// ("✨ Introduce new") — opening a section from the hub behaves exactly
// like typing its command.
const (
	dataMenuOpen     = "menu:open"
	dataMenuStudy    = "menu:study"
	dataMenuTrain    = "menu:train"
	dataMenuGame     = "menu:game"
	dataMenuTopics   = "menu:topics"
	dataMenuStats    = "menu:stats"
	dataMenuSettings = "menu:settings"
	dataMenuHelp     = "menu:help"
)

// menuText is the /menu hub's header.
const menuText = "🧭 Main menu — where to?"

// handleMenu serves /menu: the navigation hub, one button per destination.
func (b *Bot) handleMenu(ctx context.Context, s Session) error {
	_, err := s.SendKeyboard(menuText, menuRows(b.study != nil, b.topics != nil, b.game != nil))
	return err
}

// menuRows renders the hub's destination buttons. /train, /stats,
// /settings, and /help are always available (Trainer is required — see
// Config's doc comment); /study, /topics, and /game are gated on their
// service being wired, mirroring helpCommandsText's hasStudy/hasTopics/
// hasGame gating so the hub never advertises a destination that would just
// reply "🚧 coming soon".
func menuRows(hasStudy, hasTopics, hasGame bool) [][]Btn {
	rows := make([][]Btn, 0, 7)
	if hasStudy {
		rows = append(rows, []Btn{{Label: "📚 Study", Data: dataMenuStudy}})
	}
	rows = append(rows, []Btn{{Label: "🎯 Train", Data: dataMenuTrain}})
	if hasGame {
		rows = append(rows, []Btn{{Label: "🎮 Game", Data: dataMenuGame}})
	}
	if hasTopics {
		rows = append(rows, []Btn{{Label: "🗺 Topics", Data: dataMenuTopics}})
	}
	rows = append(rows, []Btn{{Label: "📊 Stats", Data: dataMenuStats}})
	rows = append(rows, []Btn{{Label: "⚙️ Settings", Data: dataMenuSettings}})
	rows = append(rows, []Btn{{Label: "❓ Help", Data: dataMenuHelp}})
	return rows
}

// menuBackRow is the single «⬅️ Menu» button carried by every top-level
// section (Settings, Help root, Topics root, Stats) and every Study/Train/
// Game terminal/idle screen, per the hub-and-spoke rule: there must be no
// dead end anywhere a user can land.
func menuBackRow() [][]Btn {
	return [][]Btn{{{Label: "⬅️ Menu", Data: dataMenuOpen}}}
}

// handleMenuCallback routes every "menu:" callback: menu:open re-renders the
// hub in place over the tapped message (mirroring rerenderSettings/
// handleHelpCallback's own in-place-edit convention); each per-destination
// payload acks the tap and then runs that destination's normal entry point.
// An unrecognized menu: payload is acked and otherwise inert, the same
// fallback every other callback family in this package uses.
func (b *Bot) handleMenuCallback(ctx context.Context, s Session, data string) error {
	if data == dataMenuOpen {
		if err := s.EditMessage(s.MessageID(), menuText, menuRows(b.study != nil, b.topics != nil, b.game != nil)); err != nil {
			b.logger.Warn("telegram: edit menu", "error", err)
		}
		return s.Respond("")
	}

	if err := s.Respond(""); err != nil {
		return err
	}
	switch data {
	case dataMenuStudy:
		return b.handleStudy(ctx, s)
	case dataMenuTrain:
		return b.handleTrain(ctx, s)
	case dataMenuGame:
		return b.handleGame(ctx, s)
	case dataMenuTopics:
		return b.handleTopics(ctx, s)
	case dataMenuStats:
		return b.handleStats(ctx, s)
	case dataMenuSettings:
		return b.handleSettings(ctx, s)
	case dataMenuHelp:
		return b.handleHelp(ctx, s)
	default:
		return nil
	}
}

// ── callbacks ────────────────────────────────────────────────────────────

func (b *Bot) handleCallback(ctx context.Context, s Session) error {
	data := s.Data()

	switch {
	case strings.HasPrefix(data, "intro:"):
		return b.handleIntroCallback(ctx, s, data)
	case data == dataStudyStart, data == dataStudyNext:
		return b.handleStudyCallback(ctx, s)
	case strings.HasPrefix(data, dataTopicEnablePrefix), strings.HasPrefix(data, dataTopicDisablePrefix):
		return b.handleTopicToggle(ctx, s, data)
	case strings.HasPrefix(data, "top:"):
		return b.handleTopicCallback(ctx, s, data)
	case strings.HasPrefix(data, dataAnswerPrefix):
		return b.handleAnswerCallback(ctx, s, data)
	case data == "cap:inc":
		return b.handleCapChange(ctx, s, 1)
	case data == "cap:dec":
		return b.handleCapChange(ctx, s, -1)
	case data == "cap:inc5":
		return b.handleCapChange(ctx, s, 5)
	case data == "cap:dec5":
		return b.handleCapChange(ctx, s, -5)
	case data == "icap:inc":
		return b.handleIntroCapChange(ctx, s, 1)
	case data == "icap:dec":
		return b.handleIntroCapChange(ctx, s, -1)
	case data == "icap:inc5":
		return b.handleIntroCapChange(ctx, s, 5)
	case data == "icap:dec5":
		return b.handleIntroCapChange(ctx, s, -5)
	case data == "rem:toggle":
		return b.handleRemindersToggle(ctx, s)
	case data == "rhour:inc":
		return b.handleReminderHourChange(ctx, s, 1)
	case data == "rhour:dec":
		return b.handleReminderHourChange(ctx, s, -1)
	case data == "fup:toggle":
		return b.handleFollowUpToggle(ctx, s)
	case data == "fupdelay:cycle":
		return b.handleFollowUpDelayCycle(ctx, s)
	case data == "style:cycle":
		return b.handleStyleCycle(ctx, s)
	case data == dataStartTrain:
		return b.handleStartTrainCallback(ctx, s)
	case strings.HasPrefix(data, "help:"):
		return b.handleHelpCallback(ctx, s, data)
	case strings.HasPrefix(data, "game:"):
		return b.handleGameCallback(ctx, s, data)
	case strings.HasPrefix(data, "menu:"):
		return b.handleMenuCallback(ctx, s, data)
	default: // includes DataNoop and any unrecognized payload
		return s.Respond("")
	}
}

func (b *Bot) handleCapChange(ctx context.Context, s Session, delta int) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	user.DailyNewCap = clamp(user.DailyNewCap+delta, minDailyCap, maxDailyCap)
	if err := b.store.SetDailyCap(ctx, user.ID, user.DailyNewCap); err != nil {
		return err
	}
	return b.rerenderSettings(ctx, s, user)
}

func (b *Bot) handleRemindersToggle(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	user.RemindersEnabled = !user.RemindersEnabled
	if err := b.store.SetReminders(ctx, user.ID, user.RemindersEnabled); err != nil {
		return err
	}
	return b.rerenderSettings(ctx, s, user)
}

func (b *Bot) handleStyleCycle(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	user.LabelStyle = nextLabelStyle(user.LabelStyle)
	if err := b.store.SetLabelStyle(ctx, user.ID, user.LabelStyle); err != nil {
		return err
	}
	return b.rerenderSettings(ctx, s, user)
}

// ── shared rendering helpers ─────────────────────────────────────────────

// loadOrCreateUser fetches the caller's user row, registering them on the
// fly if /start was never sent (defensive; commands should still work).
func (b *Bot) loadOrCreateUser(ctx context.Context, s Session) (storage.User, error) {
	user, ok, err := b.store.GetUserByTelegramID(ctx, s.UserID())
	if err != nil {
		return storage.User{}, err
	}
	if ok {
		return user, nil
	}
	return b.store.UpsertUser(ctx, s.UserID(), s.Username())
}

// ── pure, unit-tested formatting ─────────────────────────────────────────

// settingsRows renders the /settings keyboard: the daily new-skill cap stepper,
// the label-style cycle, and the reminder controls (on/off, local hour, and
// the follow-up nudge's own on/off + delay). Each control group sits on its own
// row so labels aren't squeezed or truncated.
func settingsRows(user storage.User, introCap *int) [][]Btn {
	rows := make([][]Btn, 0, 7)

	// Daily new-skill cap: -5 / -1 / value / +1 / +5.
	rows = append(rows, []Btn{
		{Label: "-5", Data: "cap:dec5"},
		{Label: "-1", Data: "cap:dec"},
		{Label: fmt.Sprintf("cap: %d", user.DailyNewCap), Data: "noop"},
		{Label: "+1", Data: "cap:inc"},
		{Label: "+5", Data: "cap:inc5"},
	})

	if introCap != nil {
		rows = append(rows, []Btn{
			{Label: "🎯 -5", Data: "icap:dec5"},
			{Label: "🎯 -1", Data: "icap:dec"},
			{Label: fmt.Sprintf("intro cap: %d", *introCap), Data: "noop"},
			{Label: "🎯 +1", Data: "icap:inc"},
			{Label: "🎯 +5", Data: "icap:inc5"},
		})
	}

	// Button label style.
	rows = append(rows, []Btn{
		{Label: labelStyleButtonLabel(user.LabelStyle), Data: "style:cycle"},
	})

	// Daily reminder: on/off, then its local hour.
	remLabel := "🔔 reminders: off"
	if user.RemindersEnabled {
		remLabel = "🔔 reminders: on"
	}
	rows = append(rows, []Btn{{Label: remLabel, Data: "rem:toggle"}})
	rows = append(rows, []Btn{
		{Label: "🕘 -1h", Data: "rhour:dec"},
		{Label: fmt.Sprintf("at %02d:00", user.ReminderHour), Data: "noop"},
		{Label: "+1h 🕙", Data: "rhour:inc"},
	})

	// Follow-up nudge: on/off, then its delay.
	fupLabel := "🔁 follow-up: off"
	if user.FollowUpEnabled {
		fupLabel = "🔁 follow-up: on"
	}
	rows = append(rows, []Btn{{Label: fupLabel, Data: "fup:toggle"}})
	rows = append(rows, []Btn{
		{Label: fmt.Sprintf("⏱ follow-up after %d min", user.FollowUpDelayMin), Data: "fupdelay:cycle"},
	})

	return rows
}

// formatStats renders the /stats view model as a readable multi-line
// message. Pure function — no Session/store dependency — so it's directly
// unit-testable.
func formatStats(st Stats) string {
	var b strings.Builder

	b.WriteString("📊 Your stats\n\n")
	fmt.Fprintf(&b, "Reviews today: %d\n", st.ReviewsToday)
	fmt.Fprintf(&b, "Reviews this week: %d\n", st.ReviewsWeek)
	fmt.Fprintf(&b, "Accuracy: %.0f%%\n", st.Accuracy*100)
	fmt.Fprintf(&b, "Streak: %d day", st.Streak)
	if st.Streak != 1 {
		b.WriteByte('s')
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Introduced: %d · Known: %d\n", st.Introduced, st.Known)

	if len(st.ByTopic) > 0 {
		b.WriteString("\nBy topic:\n")
		for _, t := range st.ByTopic {
			fmt.Fprintf(&b, "  %s: %.0f%% (%d/%d)\n", t.Name, t.Accuracy*100, t.Correct, t.Total)
		}
	}

	if len(st.DueForecast) > 0 {
		b.WriteString("\nDue forecast (next 7 days):\n  ")
		parts := make([]string, len(st.DueForecast))
		for i, n := range st.DueForecast {
			parts[i] = strconv.Itoa(n)
		}
		b.WriteString(strings.Join(parts, " "))
		b.WriteByte('\n')
	}

	if len(st.Confusion) > 0 {
		b.WriteString("\nCommon mix-ups:\n")
		for _, c := range st.Confusion {
			fmt.Fprintf(&b, "  You mistake %s for %s — %d times (%.0f%%)\n", c.TargetLabel, c.ChosenLabel, c.Count, c.Share*100)
		}
	}

	return b.String()
}

// clamp bounds v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// locationFor resolves a user's IANA timezone, falling back to UTC.
func locationFor(user storage.User) *time.Location {
	if user.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
