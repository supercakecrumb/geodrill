package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
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
	Stats(ctx context.Context, user storage.User, now time.Time) (train.Stats, error)
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
	UsersWithReminders(ctx context.Context) ([]storage.User, error)
}

// ── daily-cap bounds ────────────────────────────────────────────────────────

const (
	minDailyCap = 0
	maxDailyCap = 50
)

// ── user-facing copy ────────────────────────────────────────────────────────

const welcomeText = "Hi! I'm geodrill — I'll show you short sentences in different languages " +
	"and quiz you on which language they're in, spacing out repeats so they stick.\n\n" +
	"All decks start disabled. Pick at least one below, then send /train to begin."

const noDecksText = "You don't have any decks enabled yet. Pick at least one below, then /train again."
const noContentText = "The content for your due skills hasn't been ingested yet. Try again later, or enable a different deck via /decks."
const allCaughtUpText = "You're all caught up for now."
const fallbackText = "Something went wrong on my end. Please try again in a moment."
const staleToast = "⏳ already answered"
const correctToast = "✅ correct"
const wrongToast = "❌ wrong"
const decksPickerText = "Your decks — tap to toggle, adjust the daily cap, and set reminders:"

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
	res, err := b.svc.NextExercise(ctx, user, b.now())
	if err != nil {
		return err
	}
	return b.sendNextResult(ctx, s, user, res)
}

// ── /practice ────────────────────────────────────────────────────────────

func (b *Bot) handlePractice(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	res, err := b.svc.NextPractice(ctx, user, b.now())
	if err != nil {
		return err
	}
	return b.sendNextResult(ctx, s, user, res)
}

// ── /decks ───────────────────────────────────────────────────────────────

func (b *Bot) handleDecks(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	return b.sendDeckPicker(ctx, s, user)
}

// ── /stats ───────────────────────────────────────────────────────────────

func (b *Bot) handleStats(ctx context.Context, s Session) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	st, err := b.svc.Stats(ctx, user, b.now())
	if err != nil {
		return err
	}
	return s.Send(formatStats(st))
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
	case data == "cap:inc":
		return b.handleCapChange(ctx, s, 1)
	case data == "cap:dec":
		return b.handleCapChange(ctx, s, -1)
	case data == "rem:toggle":
		return b.handleRemindersToggle(ctx, s)
	default: // includes train.DataNoop and any unrecognized payload
		return s.Respond("")
	}
}

// handleAnswerCallback grades a /train or /practice tap, edits the keyboard
// in place, toasts the result, then sends the next exercise as a new
// message.
func (b *Bot) handleAnswerCallback(ctx context.Context, s Session, cb train.Callback, now time.Time) error {
	res, err := b.svc.Answer(ctx, cb, now)
	if err != nil {
		return err
	}
	if res.Stale {
		return s.Respond(staleToast)
	}

	msgID := s.MessageID()
	if res.HasMessage {
		msgID = res.MessageID
	}
	if err := s.EditKeyboard(msgID, gradedButtonRows(res.Buttons)); err != nil {
		return err
	}

	toast := wrongToast
	if res.Correct {
		toast = correctToast
	}
	if err := s.Respond(toast); err != nil {
		return err
	}

	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
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
	return b.rerenderDeckPicker(ctx, s, user)
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
	return b.rerenderDeckPicker(ctx, s, user)
}

// ── shared rendering helpers ─────────────────────────────────────────────

// sendNextResult renders a train.NextResult: a fresh exercise, a "nothing
// due"/"all caught up" message, a nudge to enable decks, or a no-content
// notice.
func (b *Bot) sendNextResult(ctx context.Context, s Session, user storage.User, res train.NextResult) error {
	switch res.Kind {
	case train.KindExercise:
		return b.sendPrompt(ctx, s, res.Prompt)
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
func (b *Bot) sendPrompt(ctx context.Context, s Session, p *train.Prompt) error {
	text := p.Text
	if p.Source != "" {
		text += "\n— " + p.Source
	}
	msgID, err := s.SendKeyboard(text, buttonRows(p.Buttons))
	if err != nil {
		return err
	}
	return b.store.SetExerciseMessageID(ctx, p.ExerciseID, msgID)
}

// sendDeckPicker loads the user's decks and renders the picker keyboard as a
// new message.
func (b *Bot) sendDeckPicker(ctx context.Context, s Session, user storage.User) error {
	decks, err := b.store.ListUserDecks(ctx, user.ID)
	if err != nil {
		return err
	}
	_, err = s.SendKeyboard(decksPickerText, deckPickerRows(decks, user.DailyNewCap, user.RemindersEnabled))
	return err
}

// rerenderDeckPicker re-renders the picker keyboard in place (in response to
// a deck/cap/reminder toggle) and acks the callback.
func (b *Bot) rerenderDeckPicker(ctx context.Context, s Session, user storage.User) error {
	decks, err := b.store.ListUserDecks(ctx, user.ID)
	if err != nil {
		return err
	}
	if err := s.EditKeyboard(s.MessageID(), deckPickerRows(decks, user.DailyNewCap, user.RemindersEnabled)); err != nil {
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

// buttonRows lays out exercise answer buttons two per row.
func buttonRows(buttons []train.Button) [][]Btn {
	rows := make([][]Btn, 0, (len(buttons)+1)/2)
	for i := 0; i < len(buttons); i += 2 {
		row := []Btn{{Label: buttons[i].Label, Data: buttons[i].CallbackData}}
		if i+1 < len(buttons) {
			row = append(row, Btn{Label: buttons[i+1].Label, Data: buttons[i+1].CallbackData})
		}
		rows = append(rows, row)
	}
	return rows
}

// gradedButtonRows lays out graded answer buttons (decorated with ✅/❌) two
// per row, all wired to the inert noop callback.
func gradedButtonRows(buttons []train.GradedButton) [][]Btn {
	rows := make([][]Btn, 0, (len(buttons)+1)/2)
	for i := 0; i < len(buttons); i += 2 {
		row := []Btn{{Label: buttons[i].Label, Data: train.DataNoop}}
		if i+1 < len(buttons) {
			row = append(row, Btn{Label: buttons[i+1].Label, Data: train.DataNoop})
		}
		rows = append(rows, row)
	}
	return rows
}

// deckPickerRows renders one row per deck (✅/⬜ + name, toggling it) plus a
// final row of daily-cap and reminder controls.
func deckPickerRows(decks []storage.UserDeck, dailyCap int, reminders bool) [][]Btn {
	rows := make([][]Btn, 0, len(decks)+1)
	for _, d := range decks {
		mark := "⬜"
		if d.Enabled {
			mark = "✅"
		}
		rows = append(rows, []Btn{{Label: mark + " " + d.Name, Data: "deck:" + d.ID.String()}})
	}

	remLabel := "🔔 off"
	if reminders {
		remLabel = "🔔 on"
	}
	rows = append(rows, []Btn{
		{Label: "➖ cap", Data: "cap:dec"},
		{Label: fmt.Sprintf("cap: %d", dailyCap), Data: "noop"},
		{Label: "➕ cap", Data: "cap:inc"},
		{Label: remLabel, Data: "rem:toggle"},
	})
	return rows
}

// formatStats renders the /stats view model as a readable multi-line
// message. Pure function — no Session/store dependency — so it's directly
// unit-testable.
func formatStats(st train.Stats) string {
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

	if len(st.ByDeck) > 0 {
		b.WriteString("\nBy deck:\n")
		for _, d := range st.ByDeck {
			fmt.Fprintf(&b, "  %s: %.0f%% (%d/%d)\n", d.Name, d.Accuracy*100, d.Correct, d.Total)
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
