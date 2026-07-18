package telegram

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/tips"
	"github.com/supercakecrumb/geodrill/internal/train"
)

// ── narrow dependency interfaces (for unit-testing without a DB) ───────────

// trainer is the subset of *train.Service the handlers call, extracted so
// tests can stub NextResult/AnswerResult/Stats without a database.
// *train.Service satisfies this structurally.
type trainer interface {
	NextExercise(ctx context.Context, user storage.User, now time.Time) (train.NextResult, error)
	NextPractice(ctx context.Context, user storage.User, now time.Time) (train.NextResult, error)
	Answer(ctx context.Context, cb train.Callback, now time.Time) (train.AnswerResult, error)
	DueCount(ctx context.Context, user storage.User, now time.Time) (int, error)
}

// userStore is the subset of *storage.Store the handlers call, extracted so
// tests can stub it without a database. *storage.Store satisfies this
// structurally.
type userStore interface {
	UpsertUser(ctx context.Context, telegramID int64, username string) (storage.User, error)
	GetUserByTelegramID(ctx context.Context, telegramID int64) (storage.User, bool, error)
	SetExerciseMessageID(ctx context.Context, exerciseID uuid.UUID, messageID int64) error
	ListUserDecks(ctx context.Context, userID uuid.UUID) ([]storage.UserDeck, error)
	SetUserDeckEnabled(ctx context.Context, userID, deckID uuid.UUID, enabled bool) error
	CountEnabledDecks(ctx context.Context, userID uuid.UUID) (int, error)
	SetDailyCap(ctx context.Context, userID uuid.UUID, cap int) error
	SetReminders(ctx context.Context, userID uuid.UUID, enabled bool) error
	SetReminderHour(ctx context.Context, userID uuid.UUID, hour int) error
	SetFollowUpEnabled(ctx context.Context, userID uuid.UUID, enabled bool) error
	SetFollowUpDelay(ctx context.Context, userID uuid.UUID, minutes int) error
	SetLabelStyle(ctx context.Context, userID uuid.UUID, style string) error
	UsersWithReminders(ctx context.Context) ([]storage.User, error)
	CountReviewsSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error)
	PracticeStatsSince(ctx context.Context, userID uuid.UUID, since time.Time) (total, correct int, err error)
}

// dataStopPractice is the callback payload for the /practice Stop control. It
// deliberately avoids the "ans"/"prac" prefixes so train.ParseCallback never
// mistakes it for an answer tap.
const dataStopPractice = "pstop"

// dataStartTrain is the callback payload for the "Start reviewing" button on
// the daily reminder — tapping it kicks off the /train flow without the user
// typing a command. Two segments (no third ":") so train.ParseCallback ignores
// it.
const dataStartTrain = "train:start"

// ── daily-cap bounds ────────────────────────────────────────────────────────

const (
	minDailyCap = 0
	maxDailyCap = 50
)

const (
	minIntroCap = 0
	maxIntroCap = 50
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

const noDecksText = "You don't have any decks enabled yet. Pick at least one below, then /train again."
const noContentText = "The content for your due skills hasn't been ingested yet. Try again later, or enable a different deck via /decks."
const noTopicsText = "You don't have any topics enabled for practice yet. Check /topics to turn some on, then /practice again."
const allCaughtUpText = "You're all caught up for now."
const fallbackText = "Something went wrong on my end. Please try again in a moment."
const staleToast = "⏳ already answered"
const correctToast = "✅ correct"
const wrongToast = "❌ wrong"
const decksPickerText = "Your decks — tap to turn confusion groups on/off.\n(Daily cap, reminders & button style live in /settings.)"
const settingsText = "⚙️ Settings — daily new-skill cap, button style, and reminders:"

const helpText = "🌍 geodrill — train the languages you keep confusing in GeoGuessr.\n\n" +
	"I show you a short real sentence; you tap the flag of the language it's in. " +
	"I space out repeats (FSRS, like Anki), so you drill exactly the ones you miss.\n\n" +
	"Commands:\n" +
	"/train — next due exercise; answering marks the keyboard (✅/❌) and sends the next\n" +
	"/practice — endless practice that does NOT touch your schedule\n" +
	"/decks — turn confusion groups on/off\n" +
	"/settings — daily new-skill cap, button style, and reminders (hour + follow-up)\n" +
	"/stats — reviews, accuracy, streak, due forecast, and your top mix-ups\n" +
	"/start — register and open the deck picker\n" +
	"/help — this message\n\n" +
	"Buttons show a flag + language name by default (🇵🇹 Portuguese); tap the 🔤 " +
	"control in /decks to switch to flag + code or name only. The flag is a memory " +
	"hook for the language, not a claim it's spoken only there.\n\n" +
	"Decks marked 💡 show a one-line tip after each answer, pointing at the giveaway " +
	"in that exact sentence (currently: Romance languages). Other decks don't have " +
	"tips yet.\n\n" +
	"Sentences: Tatoeba (tatoeba.org), CC-BY."

// ── /start ───────────────────────────────────────────────────────────────

func (b *Bot) handleStart(ctx context.Context, s Session) error {
	user, err := b.store.UpsertUser(ctx, s.UserID(), s.Username())
	if err != nil {
		return err
	}
	if err := s.Send(welcomeText); err != nil {
		return err
	}
	return b.sendDeckPicker(ctx, s, user)
}

// ── /train ───────────────────────────────────────────────────────────────

func (b *Bot) handleTrain(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	return b.sendNextTrain(ctx, s, user)
}

// ── /practice ────────────────────────────────────────────────────────────

func (b *Bot) handlePractice(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	b.markPracticeStart(s.UserID(), b.now())
	return b.sendNextPractice(ctx, s, user)
}

// handleStopPractice ends the caller's /practice session and rewrites the
// current (last) practice message in place with a quick tally of the run.
func (b *Bot) handleStopPractice(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	now := b.now()
	since, ok := b.takePracticeStart(s.UserID())
	if !ok {
		// No session recorded (e.g. bot restarted mid-session): fall back to
		// the start of the user's local day.
		since = startOfLocalDay(user, now)
	}
	total, correct, err := b.store.PracticeStatsSince(ctx, user.ID, since)
	if err != nil {
		return err
	}
	// Rewrite the message the Stop button sits on, dropping the keyboard.
	if err := s.EditMessage(s.MessageID(), formatPracticeSummary(total, correct), nil); err != nil {
		b.logger.Warn("telegram: edit practice summary", "error", err)
	}
	return s.Respond("⏹ practice stopped")
}

// handleStartTrainCallback runs the /train flow from the "Start reviewing"
// button on the daily reminder: it acks the tap (clearing the button's
// spinner) and sends the next due exercise (V2 when wired) as a new message.
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

// ── /decks ───────────────────────────────────────────────────────────────

func (b *Bot) handleDecks(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	return b.sendDeckPicker(ctx, s, user)
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
	return s.Send(helpTextFor(b.study != nil, b.topics != nil))
}

// helpTextFor appends a mention of /study and/or /topics to the base
// helpText when the corresponding v2 service is wired, so /help never
// advertises a command that would just reply "🚧 coming with v2 wiring".
// Returns helpText verbatim when both are nil (today's exact message).
func helpTextFor(hasStudy, hasTopics bool) string {
	if !hasStudy && !hasTopics {
		return helpText
	}
	var b strings.Builder
	b.WriteString(helpText)
	b.WriteString("\n\nAlso available:\n")
	if hasStudy {
		b.WriteString("/study — teaching cards for new items (✅ Got it / 🧠 I know this / 🎯 Test me); /introduce fetches more on demand\n")
	}
	if hasTopics {
		b.WriteString("/topics — browse topics, tiers, and your progress\n")
	}
	return b.String()
}

// ── /stats ───────────────────────────────────────────────────────────────

// statsDormantText is what /stats replies with when Config.TrainerV2 is
// nil, matching the /study and /topics "coming with v2 wiring" convention:
// /stats is now computed entirely over v2 reviews/user_items (study.
// Service.Stats), so there is no legacy fallback to degrade to.
const statsDormantText = "🚧 /stats is coming with v2 wiring."

func (b *Bot) handleStats(ctx context.Context, s Session) error {
	if b.trainerV2 == nil {
		return s.Send(statsDormantText)
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	st, err := b.trainerV2.Stats(ctx, user.ID)
	if err != nil {
		return err
	}
	return s.Send(formatStatsV2(st))
}

// ── callbacks ────────────────────────────────────────────────────────────

func (b *Bot) handleCallback(ctx context.Context, s Session) error {
	data := s.Data()
	now := b.now()

	if cb, ok := train.ParseCallback(data); ok {
		return b.handleAnswerCallback(ctx, s, cb, now)
	}

	switch {
	case strings.HasPrefix(data, "deck:"):
		return b.handleDeckToggle(ctx, s, strings.TrimPrefix(data, "deck:"))
	case strings.HasPrefix(data, "intro:"):
		return b.handleIntroCallback(ctx, s, data)
	case data == dataStudyStart, data == dataStudyNext:
		return b.handleStudyCallback(ctx, s)
	case strings.HasPrefix(data, "top:"):
		return b.handleTopicCallback(ctx, s, data)
	case strings.HasPrefix(data, dataV2AnswerPrefix):
		return b.handleV2AnswerCallback(ctx, s, data)
	case strings.HasPrefix(data, dataV2PracticePrefix):
		return b.handleV2PracticeAnswerCallback(ctx, s, data)
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
	case data == dataStopPractice:
		return b.handleStopPractice(ctx, s)
	case data == dataStartTrain:
		return b.handleStartTrainCallback(ctx, s)
	default: // includes train.DataNoop and any unrecognized payload
		return s.Respond("")
	}
}

// handleAnswerCallback grades a /train or /practice tap, edits the answered
// message in place (appending a recognition tip below the sentence when one
// is available, otherwise just regrading the keyboard), toasts the result,
// then sends the next exercise as a new message.
func (b *Bot) handleAnswerCallback(ctx context.Context, s Session, cb train.Callback, now time.Time) error {
	res, err := b.svc.Answer(ctx, cb, now)
	if err != nil {
		return err
	}
	if res.Stale {
		return s.Respond(staleToast)
	}

	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}

	msgID := s.MessageID()
	if res.HasMessage {
		msgID = res.MessageID
	}
	// Both edits are best-effort: the answer is already graded and scheduled,
	// so an edit failure (message too old, race, network) must not swallow
	// the toast or the next exercise.
	rows := gradedButtonRows(user.LabelStyle, res.Buttons)
	edited := false
	if res.Tip != "" && res.SentenceText != "" {
		if text, ok := composeAnsweredText(res.SentenceText, res.Tip); ok {
			if err := s.EditMessage(msgID, text, rows); err != nil {
				b.logger.Warn("telegram: edit answered message", "error", err)
			} else {
				edited = true
			}
		}
	}
	if !edited {
		if err := s.EditKeyboard(msgID, rows); err != nil {
			b.logger.Warn("telegram: edit graded keyboard", "error", err)
		}
	}

	toast := wrongToast
	if res.Correct {
		toast = correctToast
	}
	if err := s.Respond(toast); err != nil {
		return err
	}

	var next train.NextResult
	if cb.Practice {
		next, err = b.svc.NextPractice(ctx, user, now)
	} else {
		next, err = b.svc.NextExercise(ctx, user, now)
	}
	if err != nil {
		return err
	}
	return b.sendNextResult(ctx, s, user, next)
}

func (b *Bot) handleDeckToggle(ctx context.Context, s Session, idStr string) error {
	deckID, err := uuid.Parse(idStr)
	if err != nil {
		return s.Respond("")
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	decks, err := b.store.ListUserDecks(ctx, user.ID)
	if err != nil {
		return err
	}
	var (
		enabled bool
		found   bool
	)
	for _, d := range decks {
		if d.ID == deckID {
			enabled = d.Enabled
			found = true
			break
		}
	}
	if !found {
		return s.Respond("")
	}
	if err := b.store.SetUserDeckEnabled(ctx, user.ID, deckID, !enabled); err != nil {
		return err
	}
	return b.rerenderDeckPicker(ctx, s, user)
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

// sendNextResult renders a train.NextResult: a fresh exercise, a "nothing
// due"/"all caught up" message, a nudge to enable decks, or a no-content
// notice.
func (b *Bot) sendNextResult(ctx context.Context, s Session, user storage.User, res train.NextResult) error {
	switch res.Kind {
	case train.KindExercise:
		return b.sendPrompt(ctx, s, user, res.Prompt)
	case train.KindNothingDue:
		if !res.DueAt.IsZero() {
			loc := locationFor(user)
			return s.Send(fmt.Sprintf("Nothing due right now. Come back at %s.", res.DueAt.In(loc).Format("15:04")))
		}
		return s.Send(allCaughtUpText)
	case train.KindNoDecks:
		if err := s.Send(noDecksText); err != nil {
			return err
		}
		return b.sendDeckPicker(ctx, s, user)
	case train.KindNoContent:
		return s.Send(noContentText)
	default:
		return s.Send(fallbackText)
	}
}

// sendPrompt sends an exercise prompt with its answer buttons and records
// the sent message id so the keyboard can be edited in place at answer time.
func (b *Bot) sendPrompt(ctx context.Context, s Session, user storage.User, p *train.Prompt) error {
	rows := buttonRows(user.LabelStyle, p.Buttons)
	if p.Practice {
		// A Stop control on each practice exercise ends the session and shows a
		// tally (see handleStopPractice).
		rows = append(rows, []Btn{{Label: "⏹ Stop practice", Data: dataStopPractice}})
	}
	msgID, err := s.SendKeyboard(p.Text, rows)
	if err != nil {
		return err
	}
	return b.store.SetExerciseMessageID(ctx, p.ExerciseID, msgID)
}

// formatPracticeSummary is the quick tally shown when a /practice session is
// stopped.
func formatPracticeSummary(total, correct int) string {
	if total == 0 {
		return "⏹ Practice stopped — no answers this session.\n\nSend /practice to start again."
	}
	pct := float64(correct) / float64(total) * 100
	return fmt.Sprintf("⏹ Practice complete\n\nAnswered: %d\nCorrect: %d (%.0f%%)\n\nSend /practice to go again.", total, correct, pct)
}

// startOfLocalDay returns midnight in the user's timezone for the day of now.
func startOfLocalDay(user storage.User, now time.Time) time.Time {
	loc := locationFor(user)
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// sendDeckPicker loads the user's decks and renders the picker keyboard as a
// new message.
func (b *Bot) sendDeckPicker(ctx context.Context, s Session, user storage.User) error {
	decks, err := b.store.ListUserDecks(ctx, user.ID)
	if err != nil {
		return err
	}
	_, err = s.SendKeyboard(decksPickerText, deckPickerRows(decks))
	return err
}

// rerenderDeckPicker re-renders the picker keyboard in place (in response to
// a deck toggle) and acks the callback.
func (b *Bot) rerenderDeckPicker(ctx context.Context, s Session, user storage.User) error {
	decks, err := b.store.ListUserDecks(ctx, user.ID)
	if err != nil {
		return err
	}
	if err := s.EditKeyboard(s.MessageID(), deckPickerRows(decks)); err != nil {
		return err
	}
	return s.Respond("")
}

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

// telegramMaxMessageLen approximates Telegram's 4096-UTF-16-unit message
// text cap; counting runes with headroom keeps edits safely under it.
const telegramMaxMessageLen = 4000

// composeAnsweredText appends the recognition tip below the sentence for
// the in-place answer edit, rendering the tip as a Telegram blockquote. The
// output is HTML because Session.EditMessage sends it with Telegram's HTML
// parse mode — both the sentence and the tip must be escaped since sentences
// (and tips) can contain <, >, or &. ok=false means the combined text would
// exceed the edit limit — the caller then keeps the keyboard-only edit.
func composeAnsweredText(sentence, tip string) (string, bool) {
	text := html.EscapeString(sentence) + "\n\n<blockquote>💡 " + html.EscapeString(tip) + "</blockquote>"
	if utf8.RuneCountInString(text) > telegramMaxMessageLen {
		return "", false
	}
	return text, true
}

// buttonRows lays out the exercise answer buttons two per row (a trailing
// odd button sits alone on the last row), each rendered per the user's
// chosen label style (see answerLabel). Two per row keeps longer labels
// (e.g. "🇫🇮 Finnish") from being truncated by Telegram.
func buttonRows(style string, buttons []train.Button) [][]Btn {
	if len(buttons) == 0 {
		return nil
	}
	rows := make([][]Btn, 0, (len(buttons)+1)/2)
	for i := 0; i < len(buttons); i += 2 {
		btn := buttons[i]
		row := []Btn{{Label: answerLabel(style, btn.Key, btn.Label), Data: btn.CallbackData}}
		if i+1 < len(buttons) {
			next := buttons[i+1]
			row = append(row, Btn{Label: answerLabel(style, next.Key, next.Label), Data: next.CallbackData})
		}
		rows = append(rows, row)
	}
	return rows
}

// gradedButtonRows lays out the graded answer buttons two per row (a
// trailing odd button sits alone on the last row), each rendered per the
// user's chosen label style and decorated with ✅/❌, all wired to the inert
// noop callback. When a button has no raw Name (e.g. in tests) it falls back
// to its pre-decorated Label.
func gradedButtonRows(style string, buttons []train.GradedButton) [][]Btn {
	if len(buttons) == 0 {
		return nil
	}
	graded := func(btn train.GradedButton) Btn {
		label := btn.Label
		if btn.Name != "" {
			label = train.DecorateLabel(answerLabel(style, btn.Key, btn.Name), btn.Mark)
		}
		return Btn{Label: label, Data: train.DataNoop}
	}
	rows := make([][]Btn, 0, (len(buttons)+1)/2)
	for i := 0; i < len(buttons); i += 2 {
		row := []Btn{graded(buttons[i])}
		if i+1 < len(buttons) {
			row = append(row, graded(buttons[i+1]))
		}
		rows = append(rows, row)
	}
	return rows
}

// deckPickerRows renders one row per deck (✅/⬜ + name, toggling it). All the
// non-deck controls (cap, reminders, style) live in settingsRows / /settings.
func deckPickerRows(decks []storage.UserDeck) [][]Btn {
	rows := make([][]Btn, 0, len(decks))
	for _, d := range decks {
		mark := "⬜"
		if d.Enabled {
			mark = "✅"
		}
		label := mark + " " + d.Name
		if tips.DeckHasTips(d.Slug) {
			label = mark + " " + d.Name + " 💡"
		}
		rows = append(rows, []Btn{{Label: label, Data: "deck:" + d.ID.String()}})
	}
	return rows
}

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
			{Label: "🎯 -1", Data: "icap:dec"},
			{Label: fmt.Sprintf("intro cap: %d", *introCap), Data: "noop"},
			{Label: "🎯 +1", Data: "icap:inc"},
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

// formatStatsV2 renders the /stats view model as a readable multi-line
// message. Pure function — no Session/store dependency — so it's directly
// unit-testable.
func formatStatsV2(st StatsV2) string {
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
