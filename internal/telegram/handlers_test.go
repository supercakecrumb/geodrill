package telegram

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/train"
)

// ── fakes ────────────────────────────────────────────────────────────────

// fakeKeyboardCall records one SendKeyboard invocation.
type fakeKeyboardCall struct {
	text string
	rows [][]Btn
}

// fakeEditCall records one EditKeyboard invocation.
type fakeEditCall struct {
	messageID int64
	rows      [][]Btn
}

// fakeSession implements Session in memory, recording every call so tests
// can assert on them without a bot token or network.
type fakeSession struct {
	userID    int64
	username  string
	messageID int64
	data      string

	sent      []string
	keyboards []fakeKeyboardCall
	edits     []fakeEditCall
	responses []string
	nextMsgID int64
}

func (f *fakeSession) UserID() int64    { return f.userID }
func (f *fakeSession) Username() string { return f.username }
func (f *fakeSession) MessageID() int64 { return f.messageID }
func (f *fakeSession) Data() string     { return f.data }

func (f *fakeSession) Send(text string) error {
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakeSession) SendKeyboard(text string, rows [][]Btn) (int64, error) {
	f.keyboards = append(f.keyboards, fakeKeyboardCall{text: text, rows: rows})
	f.nextMsgID++
	return f.nextMsgID, nil
}

func (f *fakeSession) EditKeyboard(messageID int64, rows [][]Btn) error {
	f.edits = append(f.edits, fakeEditCall{messageID: messageID, rows: rows})
	return nil
}

func (f *fakeSession) Respond(toast string) error {
	f.responses = append(f.responses, toast)
	return nil
}

// stubTrainer implements trainer with canned results, so the callback
// handler can be exercised without internal/train or a database.
type stubTrainer struct {
	answer    train.AnswerResult
	next      train.NextResult
	statsData train.Stats
}

func (s *stubTrainer) NextExercise(ctx context.Context, user storage.User, now time.Time) (train.NextResult, error) {
	return s.next, nil
}

func (s *stubTrainer) NextPractice(ctx context.Context, user storage.User, now time.Time) (train.NextResult, error) {
	return s.next, nil
}

func (s *stubTrainer) Answer(ctx context.Context, cb train.Callback, now time.Time) (train.AnswerResult, error) {
	return s.answer, nil
}

func (s *stubTrainer) Stats(ctx context.Context, user storage.User, now time.Time) (train.Stats, error) {
	return s.statsData, nil
}

func (s *stubTrainer) DueCount(ctx context.Context, user storage.User, now time.Time) (int, error) {
	return 0, nil
}

// stubStore implements userStore with a fixed user, so handlers can run
// without a database.
type stubStore struct {
	user storage.User
}

func (s *stubStore) UpsertUser(ctx context.Context, telegramID int64, username string) (storage.User, error) {
	return s.user, nil
}

func (s *stubStore) GetUserByTelegramID(ctx context.Context, telegramID int64) (storage.User, bool, error) {
	return s.user, true, nil
}

func (s *stubStore) SetExerciseMessageID(ctx context.Context, exerciseID uuid.UUID, messageID int64) error {
	return nil
}

func (s *stubStore) ListUserDecks(ctx context.Context, userID uuid.UUID) ([]storage.UserDeck, error) {
	return nil, nil
}

func (s *stubStore) SetUserDeckEnabled(ctx context.Context, userID, deckID uuid.UUID, enabled bool) error {
	return nil
}

func (s *stubStore) CountEnabledDecks(ctx context.Context, userID uuid.UUID) (int, error) {
	return 0, nil
}

func (s *stubStore) SetDailyCap(ctx context.Context, userID uuid.UUID, cap int) error {
	return nil
}

func (s *stubStore) SetReminders(ctx context.Context, userID uuid.UUID, enabled bool) error {
	return nil
}

func (s *stubStore) UsersWithReminders(ctx context.Context) ([]storage.User, error) {
	return nil, nil
}

func newTestBot(tr *stubTrainer, st *stubStore) *Bot {
	return &Bot{
		store:  st,
		svc:    tr,
		logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 100})),
		now:    time.Now,
	}
}

// ── handleCallback: stale / correct / wrong ─────────────────────────────

func TestHandleCallback_Stale(t *testing.T) {
	tr := &stubTrainer{
		answer: train.AnswerResult{Stale: true},
		next:   train.NextResult{Kind: train.KindNothingDue},
	}
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1, messageID: 42, data: "ans:" + uuid.NewString() + ":por"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	if len(s.edits) != 0 {
		t.Fatalf("expected no EditKeyboard call on a stale tap, got %d", len(s.edits))
	}
	if len(s.responses) != 1 || s.responses[0] != staleToast {
		t.Fatalf("expected toast %q, got %v", staleToast, s.responses)
	}
}

func TestHandleCallback_Correct(t *testing.T) {
	tr := &stubTrainer{
		answer: train.AnswerResult{
			Correct: true,
			Buttons: []train.GradedButton{
				{Label: "✅ Portuguese", Mark: train.MarkCorrect},
				{Label: "Spanish", Mark: train.MarkNone},
			},
			HasMessage: true,
			MessageID:  99,
		},
		next: train.NextResult{Kind: train.KindNothingDue},
	}
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1, messageID: 42, data: "ans:" + uuid.NewString() + ":por"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	if len(s.edits) != 1 {
		t.Fatalf("expected exactly one EditKeyboard call, got %d", len(s.edits))
	}
	edit := s.edits[0]
	if edit.messageID != 99 {
		t.Fatalf("expected edit on message 99 (AnswerResult.MessageID, HasMessage=true), got %d", edit.messageID)
	}

	var sawDecoratedLabel bool
	for _, row := range edit.rows {
		for _, btn := range row {
			if btn.Data != train.DataNoop {
				t.Fatalf("expected every graded button to carry noop callback data, got %q", btn.Data)
			}
			if btn.Label == "✅ Portuguese" {
				sawDecoratedLabel = true
			}
		}
	}
	if !sawDecoratedLabel {
		t.Fatalf("expected the ✅-decorated correct label to be preserved in the edit")
	}

	if len(s.responses) != 1 || s.responses[0] != correctToast {
		t.Fatalf("expected toast %q, got %v", correctToast, s.responses)
	}

	// The next exercise send path should have run (KindNothingDue -> a plain
	// Send, not a keyboard).
	if len(s.sent) != 1 {
		t.Fatalf("expected the next-result message to be sent, got %d sends", len(s.sent))
	}
}

func TestHandleCallback_Wrong(t *testing.T) {
	tr := &stubTrainer{
		answer: train.AnswerResult{
			Correct: false,
			Buttons: []train.GradedButton{
				{Label: "✅ Portuguese", Mark: train.MarkCorrect},
				{Label: "❌ Spanish", Mark: train.MarkWrong},
			},
			HasMessage: false, // fall back to the session's current message id
		},
		next: train.NextResult{Kind: train.KindNothingDue},
	}
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1, messageID: 42, data: "ans:" + uuid.NewString() + ":spa"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	if len(s.edits) != 1 {
		t.Fatalf("expected exactly one EditKeyboard call, got %d", len(s.edits))
	}
	edit := s.edits[0]
	if edit.messageID != 42 {
		t.Fatalf("expected fallback to session MessageID (42) when HasMessage=false, got %d", edit.messageID)
	}

	var sawWrongLabel bool
	for _, row := range edit.rows {
		for _, btn := range row {
			if btn.Data != train.DataNoop {
				t.Fatalf("expected every graded button to carry noop callback data, got %q", btn.Data)
			}
			if btn.Label == "❌ Spanish" {
				sawWrongLabel = true
			}
		}
	}
	if !sawWrongLabel {
		t.Fatalf("expected the ❌-decorated wrong label to be preserved in the edit")
	}

	if len(s.responses) != 1 || s.responses[0] != wrongToast {
		t.Fatalf("expected toast %q, got %v", wrongToast, s.responses)
	}
}

// ── deckPickerRows ───────────────────────────────────────────────────────

func TestDeckPickerRows(t *testing.T) {
	enabledID := uuid.New()
	disabledID := uuid.New()
	decks := []storage.UserDeck{
		{Deck: storage.Deck{ID: enabledID, Name: "Romance languages"}, Enabled: true},
		{Deck: storage.Deck{ID: disabledID, Name: "Nordic"}, Enabled: false},
	}

	rows := deckPickerRows(decks, 7, true)

	var all []Btn
	for _, row := range rows {
		all = append(all, row...)
	}

	assertHasBtn(t, all, Btn{Label: "✅ Romance languages", Data: "deck:" + enabledID.String()})
	assertHasBtn(t, all, Btn{Label: "⬜ Nordic", Data: "deck:" + disabledID.String()})
	assertHasBtn(t, all, Btn{Label: "➖ cap", Data: "cap:dec"})
	assertHasBtn(t, all, Btn{Label: "cap: 7", Data: "noop"})
	assertHasBtn(t, all, Btn{Label: "➕ cap", Data: "cap:inc"})
	assertHasBtn(t, all, Btn{Label: "🔔 on", Data: "rem:toggle"})

	// Reminders off -> label flips.
	rowsOff := deckPickerRows(decks, 7, false)
	var allOff []Btn
	for _, row := range rowsOff {
		allOff = append(allOff, row...)
	}
	assertHasBtn(t, allOff, Btn{Label: "🔔 off", Data: "rem:toggle"})
}

func assertHasBtn(t *testing.T, btns []Btn, want Btn) {
	t.Helper()
	for _, b := range btns {
		if b == want {
			return
		}
	}
	t.Fatalf("expected button %+v among %+v", want, btns)
}

// ── formatStats ──────────────────────────────────────────────────────────

func TestFormatStats(t *testing.T) {
	st := train.Stats{
		ReviewsToday: 12,
		ReviewsWeek:  50,
		Streak:       4,
		Accuracy:     0.83,
		ByDeck: []train.DeckAccuracy{
			{Slug: "romance", Name: "Romance languages", Total: 20, Correct: 18, Accuracy: 0.9},
		},
		DueForecast: []int{3, 5, 0, 1, 2, 0, 4},
		Confusion: []train.ConfusionRow{
			{TargetKey: "por", TargetLabel: "Portuguese", ChosenKey: "spa", ChosenLabel: "Spanish", Count: 7, Share: 0.4},
		},
	}

	out := formatStats(st)

	for _, want := range []string{
		"Reviews today: 12",
		"Reviews this week: 50",
		"Accuracy: 83%",
		"Streak: 4 days",
		"Romance languages: 90% (18/20)",
		"3 5 0 1 2 0 4",
		"You mistake Portuguese for Spanish — 7 times (40%)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected formatStats output to contain %q; got:\n%s", want, out)
		}
	}
}

func TestFormatStats_SingularStreak(t *testing.T) {
	out := formatStats(train.Stats{Streak: 1})
	if !strings.Contains(out, "Streak: 1 day\n") {
		t.Fatalf("expected singular 'day' for a streak of 1; got:\n%s", out)
	}
}
