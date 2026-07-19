package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
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

// fakeEditMessageCall records one EditMessage invocation.
type fakeEditMessageCall struct {
	messageID int64
	text      string
	rows      [][]Btn
}

// fakePhotoCall records one SendPhoto invocation.
type fakePhotoCall struct {
	path    string
	caption string
	rows    [][]Btn
}

// fakeEditCaptionCall records one EditCaption invocation.
type fakeEditCaptionCall struct {
	messageID int64
	caption   string
	rows      [][]Btn
}

// fakeSession implements Session in memory, recording every call so tests
// can assert on them without a bot token or network.
type fakeSession struct {
	userID    int64
	username  string
	messageID int64
	data      string
	msgText   string

	sent         []string
	keyboards    []fakeKeyboardCall
	edits        []fakeEditCall
	editedMsgs   []fakeEditMessageCall
	photos       []fakePhotoCall
	editedCaps   []fakeEditCaptionCall
	responses    []string
	nextMsgID    int64
	failEditMsg  bool // make EditMessage fail (message too old, etc.)
	editMsgError error
}

func (f *fakeSession) UserID() int64       { return f.userID }
func (f *fakeSession) Username() string    { return f.username }
func (f *fakeSession) MessageID() int64    { return f.messageID }
func (f *fakeSession) Data() string        { return f.data }
func (f *fakeSession) MessageText() string { return f.msgText }

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

func (f *fakeSession) EditMessage(messageID int64, text string, rows [][]Btn) error {
	if f.failEditMsg {
		if f.editMsgError != nil {
			return f.editMsgError
		}
		return errEditMessage
	}
	f.editedMsgs = append(f.editedMsgs, fakeEditMessageCall{messageID: messageID, text: text, rows: rows})
	return nil
}

func (f *fakeSession) SendPhoto(path, caption string, rows [][]Btn) (int64, error) {
	f.photos = append(f.photos, fakePhotoCall{path: path, caption: caption, rows: rows})
	f.nextMsgID++
	return f.nextMsgID, nil
}

func (f *fakeSession) EditCaption(messageID int64, caption string, rows [][]Btn) error {
	f.editedCaps = append(f.editedCaps, fakeEditCaptionCall{messageID: messageID, caption: caption, rows: rows})
	return nil
}

func (f *fakeSession) Respond(toast string) error {
	f.responses = append(f.responses, toast)
	return nil
}

// errEditMessage is what fakeSession.EditMessage returns when failEditMsg
// is set and no specific editMsgError was provided.
var errEditMessage = errors.New("message can't be edited")

// stubStore implements userStore with a fixed user, so handlers can run
// without a database.
type stubStore struct {
	user storage.User

	remindUsers  []storage.User // returned by UsersWithReminders
	reviewsSince int            // returned by CountReviewsSince
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

func (s *stubStore) SetDailyCap(ctx context.Context, userID uuid.UUID, cap int) error {
	s.user.DailyNewCap = cap
	return nil
}

func (s *stubStore) SetReminders(ctx context.Context, userID uuid.UUID, enabled bool) error {
	s.user.RemindersEnabled = enabled
	return nil
}

func (s *stubStore) SetReminderHour(ctx context.Context, userID uuid.UUID, hour int) error {
	s.user.ReminderHour = hour
	return nil
}

func (s *stubStore) SetFollowUpEnabled(ctx context.Context, userID uuid.UUID, enabled bool) error {
	s.user.FollowUpEnabled = enabled
	return nil
}

func (s *stubStore) SetFollowUpDelay(ctx context.Context, userID uuid.UUID, minutes int) error {
	s.user.FollowUpDelayMin = minutes
	return nil
}

func (s *stubStore) SetLabelStyle(ctx context.Context, userID uuid.UUID, style string) error {
	s.user.LabelStyle = style
	return nil
}

func (s *stubStore) UsersWithReminders(ctx context.Context) ([]storage.User, error) {
	return s.remindUsers, nil
}

func (s *stubStore) CountReviewsSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	return s.reviewsSince, nil
}

func newTestBot(st *stubStore) *Bot {
	return &Bot{
		store:       st,
		logger:      slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 100})),
		now:         time.Now,
		remindState: make(map[uuid.UUID]reminderState),
	}
}

// TestHandleCallback_StartTrain covers the "Start reviewing" button on the
// daily reminder: it acks the tap and dispatches the next due exercise.
func TestHandleCallback_StartTrain(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(st)
	b.trainer = &stubTrainer{next: Prompt{
		Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "Uma frase.",
		Options: []Option{{Index: 0, Label: "🇵🇹 Portuguese"}, {Index: 1, Label: "🇪🇸 Spanish"}},
	}}

	s := &fakeSession{userID: 1, data: dataStartTrain}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback(start): %v", err)
	}
	// The tap must be acked (spinner cleared).
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected exactly one empty ack response, got %v", s.responses)
	}
	// The next exercise must be sent as a keyboard message (via Trainer).
	if len(s.keyboards) != 1 {
		t.Fatalf("expected exactly one exercise keyboard, got %d", len(s.keyboards))
	}
	if s.keyboards[0].text != "Uma frase." {
		t.Fatalf("expected the prompt text, got %q", s.keyboards[0].text)
	}
}

// ── /decks (retired onto /topics) ────────────────────────────────────────

func TestHandleDecks_AliasesTopicsWhenWired(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.topics = &stubTopicService{root: []TopicRow{{Name: "Languages"}}}

	s := &fakeSession{userID: 1}
	if err := b.handleDecks(context.Background(), s); err != nil {
		t.Fatalf("handleDecks: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != topicsRootText {
		t.Fatalf("expected /decks to alias the /topics root listing, got %+v", s.keyboards)
	}
}

func TestHandleDecks_UnavailableWhenTopicsNil(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)

	s := &fakeSession{userID: 1}
	if err := b.handleDecks(context.Background(), s); err != nil {
		t.Fatalf("handleDecks: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != decksUnavailableText {
		t.Fatalf("expected the topic-browser-unavailable message, got %+v", s.sent)
	}
}

// ── settingsRows ─────────────────────────────────────────────────────────

func TestSettingsRows(t *testing.T) {
	user := storage.User{
		DailyNewCap:      7,
		LabelStyle:       "name",
		RemindersEnabled: true,
		ReminderHour:     9,
		FollowUpEnabled:  true,
		FollowUpDelayMin: 60,
	}

	collect := func(u storage.User, introCap *int) []Btn {
		var all []Btn
		for _, row := range settingsRows(u, introCap) {
			all = append(all, row...)
		}
		return all
	}

	all := collect(user, nil)
	assertHasBtn(t, all, Btn{Label: "-5", Data: "cap:dec5"})
	assertHasBtn(t, all, Btn{Label: "cap: 7", Data: "noop"})
	assertHasBtn(t, all, Btn{Label: "+5", Data: "cap:inc5"})
	assertNoBtnData(t, all, "cap:dec")
	assertNoBtnData(t, all, "cap:inc")
	assertHasBtn(t, all, Btn{Label: "🔤 Flag + name", Data: "style:cycle"})
	assertHasBtn(t, all, Btn{Label: "🔔 reminders: on", Data: "rem:toggle"})
	assertHasBtn(t, all, Btn{Label: "at 09:00", Data: "noop"})
	assertHasBtn(t, all, Btn{Label: "🕘 -1h", Data: "rhour:dec"})
	assertHasBtn(t, all, Btn{Label: "+1h 🕙", Data: "rhour:inc"})
	assertHasBtn(t, all, Btn{Label: "🔁 follow-up: on", Data: "fup:toggle"})
	assertHasBtn(t, all, Btn{Label: "⏱ follow-up after 60 min", Data: "fupdelay:cycle"})
	assertHasBtn(t, all, Btn{Label: "⬅️ Menu", Data: dataMenuOpen})

	// Off states flip the labels.
	user.RemindersEnabled = false
	user.FollowUpEnabled = false
	allOff := collect(user, nil)
	assertHasBtn(t, allOff, Btn{Label: "🔔 reminders: off", Data: "rem:toggle"})
	assertHasBtn(t, allOff, Btn{Label: "🔁 follow-up: off", Data: "fup:toggle"})
}

func TestSettingsRows_IntroCapRow(t *testing.T) {
	user := storage.User{DailyNewCap: 7, LabelStyle: "name"}
	cap := 12
	var all []Btn
	for _, row := range settingsRows(user, &cap) {
		all = append(all, row...)
	}
	assertHasBtn(t, all, Btn{Label: "🎯 -5", Data: "icap:dec5"})
	assertHasBtn(t, all, Btn{Label: "intro cap: 12", Data: "noop"})
	assertHasBtn(t, all, Btn{Label: "🎯 +5", Data: "icap:inc5"})
	assertNoBtnData(t, all, "icap:dec")
	assertNoBtnData(t, all, "icap:inc")
}

// stubIntroCapStore implements IntroCapStore in memory.
type stubIntroCapStore struct {
	cap int
	err error
}

func (s *stubIntroCapStore) GetIntroCap(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.cap, s.err
}

func (s *stubIntroCapStore) SetIntroCap(ctx context.Context, userID uuid.UUID, cap int) error {
	s.cap = cap
	return s.err
}

func TestIntroCapFor(t *testing.T) {
	b := newTestBot(&stubStore{})
	if got := b.introCapFor(context.Background(), uuid.New()); got != nil {
		t.Fatalf("expected nil when IntroCapStore is unset, got %v", *got)
	}
	b.introCap = &stubIntroCapStore{cap: 15}
	got := b.introCapFor(context.Background(), uuid.New())
	if got == nil || *got != 15 {
		t.Fatalf("expected 15, got %v", got)
	}
}

func TestHandleIntroCapChange_AdjustsAndRerenders(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubIntroCapStore{cap: 10}
	b.introCap = stub

	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc5"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 15 {
		t.Fatalf("expected icap:inc5 to raise the cap to 15, got %d", stub.cap)
	}
	if len(s.edits) != 1 {
		t.Fatalf("expected the settings keyboard re-rendered in place, got %d edits", len(s.edits))
	}

	s.data = "icap:dec5"
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 10 {
		t.Fatalf("expected icap:dec5 to lower the cap back to 10, got %d", stub.cap)
	}
}

func TestHandleIntroCapChange_ClampsToBounds(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubIntroCapStore{cap: maxIntroCap}
	b.introCap = stub

	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc5"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != maxIntroCap {
		t.Fatalf("expected the cap clamped at %d, got %d", maxIntroCap, stub.cap)
	}
}

func TestHandleIntroCapChange_StepsOfFive(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubIntroCapStore{cap: 10}
	b.introCap = stub

	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc5"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 15 {
		t.Fatalf("expected icap:inc5 to raise the cap by 5 to 15, got %d", stub.cap)
	}
	if len(s.edits) != 1 {
		t.Fatalf("expected the settings keyboard re-rendered in place, got %d edits", len(s.edits))
	}

	s.data = "icap:dec5"
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 10 {
		t.Fatalf("expected icap:dec5 to lower the cap by 5 back to 10, got %d", stub.cap)
	}
}

func TestHandleIntroCapChange_ClampsAtNewMaximum(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubIntroCapStore{cap: 198}
	b.introCap = stub

	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc5"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 200 {
		t.Fatalf("expected +5 at 198 to clamp to the new maximum 200, got %d", stub.cap)
	}
	if maxIntroCap != 200 {
		t.Fatalf("expected maxIntroCap to be 200, got %d", maxIntroCap)
	}

	// A further +5 must stay clamped at the maximum.
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 200 {
		t.Fatalf("expected the cap to stay clamped at 200, got %d", stub.cap)
	}
}

func TestHandleIntroCapChange_ClampsAtMinimum(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubIntroCapStore{cap: 3}
	b.introCap = stub

	s := &fakeSession{userID: 1, messageID: 42, data: "icap:dec5"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != minIntroCap {
		t.Fatalf("expected -5 at 3 to clamp to the minimum %d, got %d", minIntroCap, stub.cap)
	}
}

func TestHandleIntroCapChange_NilStoreIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc5"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
	if len(s.edits) != 0 {
		t.Fatalf("expected no re-render when IntroCapStore is nil, got %d edits", len(s.edits))
	}
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

// assertNoBtnData fails if any button among btns carries the exact callback
// data — used to assert the retired ±1 cap/intro-cap buttons are gone
// (data is matched exactly so e.g. "cap:inc" doesn't false-positive on the
// still-present "cap:inc5").
func assertNoBtnData(t *testing.T, btns []Btn, data string) {
	t.Helper()
	for _, b := range btns {
		if b.Data == data {
			t.Fatalf("did not expect a button with data %q among %+v", data, btns)
		}
	}
}

// ── style:cycle callback ─────────────────────────────────────────────────

func TestHandleStyleCycle(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC", LabelStyle: "name"}}
	b := newTestBot(st)
	s := &fakeSession{userID: 1, messageID: 42, data: "style:cycle"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.LabelStyle != "code" {
		t.Fatalf("expected style to advance name -> code and persist via the store, got %q", st.user.LabelStyle)
	}
	if len(s.edits) != 1 {
		t.Fatalf("expected the picker to be re-rendered in place, got %d edits", len(s.edits))
	}

	// Cycle twice more: code -> plain -> name.
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.LabelStyle != "plain" {
		t.Fatalf("expected style to advance code -> plain, got %q", st.user.LabelStyle)
	}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.LabelStyle != "name" {
		t.Fatalf("expected style to wrap plain -> name, got %q", st.user.LabelStyle)
	}
}

// ── cap:inc5 / cap:dec5 callbacks ────────────────────────────────────────

func TestHandleCapChange_StepsOfFive(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC", DailyNewCap: 10}}
	b := newTestBot(st)
	s := &fakeSession{userID: 1, messageID: 42, data: "cap:inc5"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.DailyNewCap != 15 {
		t.Fatalf("expected cap:inc5 to raise the cap by 5 to 15, got %d", st.user.DailyNewCap)
	}
	if len(s.edits) != 1 {
		t.Fatalf("expected the picker to be re-rendered in place, got %d edits", len(s.edits))
	}

	s.data = "cap:dec5"
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.DailyNewCap != 10 {
		t.Fatalf("expected cap:dec5 to lower the cap by 5 back to 10, got %d", st.user.DailyNewCap)
	}
}

// TestHandleCallback_RetiredCapStepCallbacksAreGone covers Feature 2's
// removal of the ±1 cap/intro-cap callback cases: "cap:inc", "cap:dec",
// "icap:inc", "icap:dec" no longer appear in settingsRows (TestSettingsRows/
// TestSettingsRows_IntroCapRow), and this test confirms the dispatch side
// too — a tap with one of those payloads must fall through to the inert
// default branch (a single empty ack, no cap mutated, no re-render) instead
// of being handled.
func TestHandleCallback_RetiredCapStepCallbacksAreGone(t *testing.T) {
	for _, data := range []string{"cap:inc", "cap:dec", "icap:inc", "icap:dec"} {
		t.Run(data, func(t *testing.T) {
			st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC", DailyNewCap: 10}}
			b := newTestBot(st)
			introCap := &stubIntroCapStore{cap: 10}
			b.introCap = introCap

			s := &fakeSession{userID: 1, messageID: 42, data: data}
			if err := b.handleCallback(context.Background(), s); err != nil {
				t.Fatalf("handleCallback(%q): %v", data, err)
			}
			if len(s.responses) != 1 || s.responses[0] != "" {
				t.Fatalf("data %q: expected a single inert ack, got %v", data, s.responses)
			}
			if len(s.edits) != 0 {
				t.Fatalf("data %q: expected no re-render for a retired callback, got %d edits", data, len(s.edits))
			}
			if st.user.DailyNewCap != 10 {
				t.Fatalf("data %q: expected the daily cap untouched, got %d", data, st.user.DailyNewCap)
			}
			if introCap.cap != 10 {
				t.Fatalf("data %q: expected the intro cap untouched, got %d", data, introCap.cap)
			}
		})
	}
}

// ── /settings reminder controls ──────────────────────────────────────────

func TestHandleReminderHourChange(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC", ReminderHour: 9}}
	b := newTestBot(st)
	s := &fakeSession{userID: 1, messageID: 42, data: "rhour:inc"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.ReminderHour != 10 {
		t.Fatalf("expected +1h to advance 9 -> 10, got %d", st.user.ReminderHour)
	}
	if len(s.edits) != 1 {
		t.Fatalf("expected settings to re-render in place, got %d edits", len(s.edits))
	}

	// Decrement wraps 0 -> 23.
	st.user.ReminderHour = 0
	s.data = "rhour:dec"
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.ReminderHour != 23 {
		t.Fatalf("expected -1h to wrap 0 -> 23, got %d", st.user.ReminderHour)
	}
}

func TestHandleFollowUpToggle(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC", FollowUpEnabled: true}}
	b := newTestBot(st)
	s := &fakeSession{userID: 1, messageID: 42, data: "fup:toggle"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if st.user.FollowUpEnabled {
		t.Fatalf("expected follow-up to toggle on -> off")
	}
	if len(s.edits) != 1 {
		t.Fatalf("expected settings to re-render in place, got %d edits", len(s.edits))
	}
}

func TestHandleFollowUpDelayCycle(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC", FollowUpDelayMin: 30}}
	b := newTestBot(st)
	s := &fakeSession{userID: 1, messageID: 42, data: "fupdelay:cycle"}

	for _, want := range []int{60, 120, 30} { // 30 -> 60 -> 120 -> wrap 30
		if err := b.handleCallback(context.Background(), s); err != nil {
			t.Fatalf("handleCallback: %v", err)
		}
		if st.user.FollowUpDelayMin != want {
			t.Fatalf("expected follow-up delay to advance to %d, got %d", want, st.user.FollowUpDelayMin)
		}
	}
}

// ── decideReminder ───────────────────────────────────────────────────────

func TestDecideReminder(t *testing.T) {
	const day = "2026-07-18"
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	base := storage.User{RemindersEnabled: true, ReminderHour: 9, FollowUpEnabled: true, FollowUpDelayMin: 60}

	// A day's state where the first reminder went out `ago` before now.
	sent := func(ago time.Duration, followUps int) reminderState {
		return reminderState{day: day, firstSentAt: now.Add(-ago), lastSentAt: now.Add(-ago), followUps: followUps}
	}

	cases := []struct {
		name          string
		user          storage.User
		st            reminderState
		localHour     int
		due           int
		reviewedSince int
		introReady    int
		want          reminderKind
	}{
		{"first at chosen hour", base, reminderState{}, 9, 3, 0, 0, reminderFirst},
		{"nothing off-hour", base, reminderState{}, 8, 3, 0, 0, reminderNone},
		{"nothing when due is zero", base, reminderState{}, 9, 0, 0, 0, reminderNone},
		{"nothing when reminders disabled", storage.User{RemindersEnabled: false, ReminderHour: 9, FollowUpEnabled: true, FollowUpDelayMin: 60}, reminderState{}, 9, 3, 0, 0, reminderNone},
		{"stale prior-day state still fires first", base, reminderState{day: "2026-07-17", firstSentAt: now.Add(-24 * time.Hour)}, 9, 3, 0, 0, reminderFirst},
		{"follow-up after the delay", base, sent(90*time.Minute, 0), 11, 3, 0, 0, reminderFollowUp},
		{"no follow-up before the delay", base, sent(30*time.Minute, 0), 11, 3, 0, 0, reminderNone},
		{"no follow-up once engaged", base, sent(90*time.Minute, 0), 11, 3, 1, 0, reminderNone},
		{"no follow-up at the cap", base, sent(90*time.Minute, maxFollowUps), 11, 3, 0, 0, reminderNone},
		{"no follow-up when disabled", storage.User{RemindersEnabled: true, ReminderHour: 9, FollowUpEnabled: false, FollowUpDelayMin: 60}, sent(90*time.Minute, 0), 11, 3, 0, 0, reminderNone},
		// introReady-driven cases (architecture §5.3: StudyService extends the reminder gate).
		{"first purely from intro-ready, due zero", base, reminderState{}, 9, 0, 0, 5, reminderFirst},
		{"nothing when both due and introReady are zero", base, reminderState{}, 9, 0, 0, 0, reminderNone},
		{"nothing off-hour even with intro-ready", base, reminderState{}, 8, 0, 0, 5, reminderNone},
		{"follow-up still fires with intro-ready and due zero", base, sent(90*time.Minute, 0), 11, 0, 0, 5, reminderFollowUp},
		{"follow-up suppression unaffected by intro-ready (engaged)", base, sent(90*time.Minute, 0), 11, 0, 1, 5, reminderNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideReminder(tc.user, tc.st, day, tc.localHour, now, tc.due, tc.reviewedSince, tc.introReady)
			if got != tc.want {
				t.Fatalf("decideReminder = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReminderText(t *testing.T) {
	cases := []struct {
		due        int
		introReady int
		followUp   bool
		want       string
	}{
		{1, 0, false, "🔔 You have 1 review due today."},
		{5, 0, false, "🔔 You have 5 reviews due today."},
		{1, 0, true, "⏰ Still 1 review waiting — tap to start."},
		{3, 0, true, "⏰ Still 3 reviews waiting — tap to start."},
		// intro-only (due == 0).
		{0, 1, false, "✨ 1 new item ready to introduce."},
		{0, 5, false, "✨ 5 new items ready to introduce."},
		{0, 5, true, "⏰ Still 5 new items ready to introduce — tap to start."},
		// combined (architecture §5.3's "N reviews due · M new items to introduce").
		{3, 5, false, "🔔 3 reviews due · 5 new items to introduce."},
		{1, 1, false, "🔔 1 review due · 1 new item to introduce."},
		{3, 5, true, "⏰ Still 3 reviews due · 5 new items to introduce — tap to start."},
	}
	for _, tc := range cases {
		if got := reminderText(tc.due, tc.introReady, tc.followUp); got != tc.want {
			t.Fatalf("reminderText(%d, %d, %v) = %q, want %q", tc.due, tc.introReady, tc.followUp, got, tc.want)
		}
	}
}

// ── reminderButtonRows ───────────────────────────────────────────────────

func TestReminderButtonRows(t *testing.T) {
	dueOnly := reminderButtonRows(3, 0)
	assertHasBtn(t, dueOnly[0], Btn{Label: "▶️ Start reviewing", Data: dataStartTrain})
	for _, btn := range dueOnly[0] {
		if btn.Data == dataStudyStart {
			t.Fatalf("did not expect the Introduce-new button when introReady is 0")
		}
	}

	introOnly := reminderButtonRows(0, 5)
	assertHasBtn(t, introOnly[0], Btn{Label: "✨ Introduce new", Data: dataStudyStart})
	for _, btn := range introOnly[0] {
		if btn.Data == dataStartTrain {
			t.Fatalf("did not expect the Start-reviewing button when due is 0")
		}
	}

	both := reminderButtonRows(3, 5)
	var all []Btn
	for _, row := range both {
		all = append(all, row...)
	}
	assertHasBtn(t, all, Btn{Label: "▶️ Start reviewing", Data: dataStartTrain})
	assertHasBtn(t, all, Btn{Label: "✨ Introduce new", Data: dataStudyStart})

	if rows := reminderButtonRows(0, 0); rows != nil {
		t.Fatalf("expected no rows when both due and introReady are 0, got %+v", rows)
	}
}

// ── formatStats ────────────────────────────────────────────────────────

func TestFormatStats(t *testing.T) {
	st := Stats{
		ReviewsToday: 12,
		ReviewsWeek:  50,
		Streak:       4,
		Accuracy:     0.83,
		ByTopic: []TopicAccuracy{
			{Name: "Languages", Total: 20, Correct: 18, Accuracy: 0.9},
		},
		DueForecast: []int{3, 5, 0, 1, 2, 0, 4},
		Confusion: []ConfusionRow{
			{TargetLabel: "Portuguese", ChosenLabel: "Spanish", Count: 7, Share: 0.4},
		},
		Introduced: 30,
		Known:      5,
		Tier:       2,
		MaxTier:    6,
	}

	out := formatStats(st)

	for _, want := range []string{
		"Reviews today: 12",
		"Reviews this week: 50",
		"Accuracy: 83%",
		"Streak: 4 days",
		"Introduced: 30 · Known: 5",
		"🎚 Tier: 2 of 6",
		"Languages: 90% (18/20)",
		"3 5 0 1 2 0 4",
		"You mistake Portuguese for Spanish — 7 times (40%)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected formatStats output to contain %q; got:\n%s", want, out)
		}
	}
}

func TestFormatStats_SingularStreak(t *testing.T) {
	out := formatStats(Stats{Streak: 1})
	if !strings.Contains(out, "Streak: 1 day\n") {
		t.Fatalf("expected singular 'day' for a streak of 1; got:\n%s", out)
	}
}

// ── /help ────────────────────────────────────────────────────────────────

// TestHandleHelp_RootMenu covers the initial /help message: the overview
// text plus one button per subtopic, each on its own row, plus a trailing
// «⬅️ Menu» row back to the hub (hub-and-spoke rule).
func TestHandleHelp_RootMenu(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	s := &fakeSession{userID: 1}

	if err := b.handleHelp(context.Background(), s); err != nil {
		t.Fatalf("handleHelp: %v", err)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected one help menu message, got %d", len(s.keyboards))
	}
	kb := s.keyboards[0]
	if kb.text != helpRootText {
		t.Fatalf("expected the root overview text, got %q", kb.text)
	}
	wantLabels := []string{
		"📚 How spaced repetition works",
		"🎓 The intro buttons explained",
		"🗺 Topics & tiers",
		"🧭 Commands",
		"⬅️ Menu",
	}
	if len(kb.rows) != len(wantLabels) {
		t.Fatalf("expected %d rows (one button each), got %d", len(wantLabels), len(kb.rows))
	}
	for i, want := range wantLabels {
		if len(kb.rows[i]) != 1 || kb.rows[i][0].Label != want {
			t.Fatalf("row %d: expected single button %q, got %+v", i, want, kb.rows[i])
		}
	}
}

// TestHandleCallback_HelpSections covers each help:* subtopic tap: it must
// edit the tapped message in place with the section's own copy plus a
// single Back button.
func TestHandleCallback_HelpSections(t *testing.T) {
	cases := []struct {
		data       string
		wantPhrase string
	}{
		{dataHelpFSRS, "FSRS algorithm"},
		{dataHelpIntro, "does NOT consume the daily intro budget"},
		{dataHelpTiers, "unlocks the tier two levels up"},
	}
	for _, tc := range cases {
		t.Run(tc.data, func(t *testing.T) {
			b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
			s := &fakeSession{userID: 1, messageID: 42, data: tc.data}

			if err := b.handleCallback(context.Background(), s); err != nil {
				t.Fatalf("handleCallback: %v", err)
			}
			if len(s.editedMsgs) != 1 {
				t.Fatalf("expected the help message edited in place, got %d edits", len(s.editedMsgs))
			}
			edit := s.editedMsgs[0]
			if edit.messageID != 42 {
				t.Fatalf("expected the edit on message 42, got %d", edit.messageID)
			}
			if !strings.Contains(edit.text, tc.wantPhrase) {
				t.Fatalf("expected section text to contain %q; got:\n%s", tc.wantPhrase, edit.text)
			}
			if len(edit.rows) != 1 || len(edit.rows[0]) != 1 ||
				edit.rows[0][0].Label != "⬅️ Back" || edit.rows[0][0].Data != dataHelpRoot {
				t.Fatalf("expected a single Back button, got %+v", edit.rows)
			}
		})
	}
}

// TestHandleCallback_HelpCmds_GatedByWiredServices covers help:cmds: it must
// only mention /study and /topics when their services are wired, matching
// the retired helpTextFor's gating so /help never advertises a command that
// would just reply "🚧 coming soon".
func TestHandleCallback_HelpCmds_GatedByWiredServices(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	s := &fakeSession{userID: 1, messageID: 42, data: dataHelpCmds}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	text := s.editedMsgs[0].text
	for _, want := range []string{"/train", "/stats", "/settings", "/help"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected commands section to mention %q; got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "/study") || strings.Contains(text, "/topics") {
		t.Fatalf("expected /study and /topics omitted when their services are nil; got:\n%s", text)
	}

	b.study = &stubStudyService{}
	b.topics = &stubTopicService{}
	s2 := &fakeSession{userID: 1, messageID: 42, data: dataHelpCmds}
	if err := b.handleCallback(context.Background(), s2); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	text2 := s2.editedMsgs[0].text
	for _, want := range []string{"/study", "/introduce", "/topics"} {
		if !strings.Contains(text2, want) {
			t.Fatalf("expected commands section to mention %q when wired; got:\n%s", want, text2)
		}
	}
}

// TestHandleCallback_HelpRoot_RestoresMenu covers the Back button: tapping
// help:root must restore the exact root menu (overview text + 4 subtopic
// buttons + the trailing «⬅️ Menu» row).
func TestHandleCallback_HelpRoot_RestoresMenu(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	s := &fakeSession{userID: 1, messageID: 42, data: dataHelpRoot}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedMsgs) != 1 {
		t.Fatalf("expected the message edited back to the root menu, got %d edits", len(s.editedMsgs))
	}
	edit := s.editedMsgs[0]
	if edit.text != helpRootText {
		t.Fatalf("expected the root overview text restored, got %q", edit.text)
	}
	if len(edit.rows) != 5 {
		t.Fatalf("expected the 5-row root menu restored, got %d rows", len(edit.rows))
	}
}

// ── /menu ────────────────────────────────────────────────────────────────

// menuBtnData flattens a keyboard's rows into their callback data, in row
// order, for asserting which destinations a hub render carries.
func menuBtnData(rows [][]Btn) []string {
	var data []string
	for _, row := range rows {
		for _, b := range row {
			data = append(data, b.Data)
		}
	}
	return data
}

// TestHandleMenu_AllDestinationsWhenWired covers /menu with every optional
// service wired: one button per destination, /train, /stats, /settings, and
// /help always present alongside the gated /study, /game, /topics.
func TestHandleMenu_AllDestinationsWhenWired(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.study = &stubStudyService{}
	b.topics = &stubTopicService{}
	b.game = &stubGameService{}

	s := &fakeSession{userID: 1}
	if err := b.handleMenu(context.Background(), s); err != nil {
		t.Fatalf("handleMenu: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != menuText {
		t.Fatalf("expected the hub sent with its header text, got %+v", s.keyboards)
	}
	got := menuBtnData(s.keyboards[0].rows)
	want := []string{dataMenuStudy, dataMenuTrain, dataMenuGame, dataMenuTopics, dataMenuStats, dataMenuSettings, dataMenuHelp}
	if len(got) != len(want) {
		t.Fatalf("expected %d destination buttons, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("destination %d: got %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// TestHandleMenu_GatesUnwiredDestinations covers /menu with no optional
// service wired: only the always-available destinations must appear,
// mirroring helpCommandsText's own hasStudy/hasTopics/hasGame gating so the
// hub never advertises a destination that would just reply "🚧 coming soon".
func TestHandleMenu_GatesUnwiredDestinations(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})

	s := &fakeSession{userID: 1}
	if err := b.handleMenu(context.Background(), s); err != nil {
		t.Fatalf("handleMenu: %v", err)
	}
	got := menuBtnData(s.keyboards[0].rows)
	want := []string{dataMenuTrain, dataMenuStats, dataMenuSettings, dataMenuHelp}
	if len(got) != len(want) {
		t.Fatalf("expected only the always-available destinations, got %v", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("destination %d: got %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// TestHandleMenuCallback_OpenReRendersHubInPlace covers menu:open: it must
// edit the tapped message in place, mirroring rerenderSettings/
// handleHelpCallback's own in-place-edit convention for every other hub
// button.
func TestHandleMenuCallback_OpenReRendersHubInPlace(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, messageID: 42, data: dataMenuOpen}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 42 || s.editedMsgs[0].text != menuText {
		t.Fatalf("expected the hub edited in place on message 42, got %+v", s.editedMsgs)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single empty ack, got %v", s.responses)
	}
}

// TestHandleMenuCallback_OpensEachSection covers every menu:<dest> payload:
// each must ack the tap and then run that destination's normal entry
// point — exactly like typing its command (mirroring
// handleStartTrainCallback/handleStudyCallback's existing ack-then-send
// shape).
func TestHandleMenuCallback_OpensEachSection(t *testing.T) {
	cases := []struct {
		name string
		data string
		wire func(b *Bot)
		want func(t *testing.T, s *fakeSession)
	}{
		{
			name: "study",
			data: dataMenuStudy,
			wire: func(b *Bot) {
				b.study = &stubStudyService{nextCard: IntroCard{IntroID: uuid.New(), Text: "card", Reason: IntroOK}}
			},
			want: func(t *testing.T, s *fakeSession) {
				if len(s.keyboards) != 1 || s.keyboards[0].text != "card" {
					t.Fatalf("expected the intro card sent, got %+v", s.keyboards)
				}
			},
		},
		{
			name: "train",
			data: dataMenuTrain,
			wire: func(b *Bot) {
				b.trainer = &stubTrainer{next: Prompt{Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "exercise"}}
			},
			want: func(t *testing.T, s *fakeSession) {
				if len(s.keyboards) != 1 || s.keyboards[0].text != "exercise" {
					t.Fatalf("expected the next exercise sent, got %+v", s.keyboards)
				}
			},
		},
		{
			name: "game",
			data: dataMenuGame,
			wire: func(b *Bot) { b.game = &stubGameService{} },
			want: func(t *testing.T, s *fakeSession) {
				if len(s.keyboards) != 1 || s.keyboards[0].text != gameMenuText {
					t.Fatalf("expected the game menu sent, got %+v", s.keyboards)
				}
			},
		},
		{
			name: "topics",
			data: dataMenuTopics,
			wire: func(b *Bot) { b.topics = &stubTopicService{root: []TopicRow{{Name: "Languages"}}} },
			want: func(t *testing.T, s *fakeSession) {
				if len(s.keyboards) != 1 || s.keyboards[0].text != topicsRootText {
					t.Fatalf("expected the topics root listing sent, got %+v", s.keyboards)
				}
			},
		},
		{
			name: "stats",
			data: dataMenuStats,
			wire: func(b *Bot) { b.trainer = &stubTrainer{stats: Stats{ReviewsToday: 3}} },
			want: func(t *testing.T, s *fakeSession) {
				if len(s.keyboards) != 1 || !strings.Contains(s.keyboards[0].text, "Reviews today: 3") {
					t.Fatalf("expected the stats view sent, got %+v", s.keyboards)
				}
			},
		},
		{
			name: "settings",
			data: dataMenuSettings,
			wire: func(b *Bot) {},
			want: func(t *testing.T, s *fakeSession) {
				if len(s.keyboards) != 1 || s.keyboards[0].text != settingsText {
					t.Fatalf("expected the settings keyboard sent, got %+v", s.keyboards)
				}
			},
		},
		{
			name: "help",
			data: dataMenuHelp,
			wire: func(b *Bot) {},
			want: func(t *testing.T, s *fakeSession) {
				if len(s.keyboards) != 1 || s.keyboards[0].text != helpRootText {
					t.Fatalf("expected the help root menu sent, got %+v", s.keyboards)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBot(&stubStore{user: newTestUser()})
			tc.wire(b)
			s := &fakeSession{userID: 1, data: tc.data}

			if err := b.handleCallback(context.Background(), s); err != nil {
				t.Fatalf("handleCallback(%s): %v", tc.name, err)
			}
			if len(s.responses) != 1 || s.responses[0] != "" {
				t.Fatalf("expected a single empty ack, got %v", s.responses)
			}
			tc.want(t, s)
		})
	}
}

// TestHandleStart_EndsAtMenu covers /start: after the welcome text and the
// /topics send, it must close by sending the same hub /menu sends (Feature
// 1's "no dead ends" rule for the entry point itself).
func TestHandleStart_EndsAtMenu(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.topics = &stubTopicService{root: []TopicRow{{Name: "Languages"}}}
	s := &fakeSession{userID: 1}

	if err := b.handleStart(context.Background(), s); err != nil {
		t.Fatalf("handleStart: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != welcomeText {
		t.Fatalf("expected the welcome text sent first, got %v", s.sent)
	}
	// /topics then /menu each send a keyboard message.
	if len(s.keyboards) != 2 {
		t.Fatalf("expected two keyboard messages (topics root, then the hub), got %d: %+v", len(s.keyboards), s.keyboards)
	}
	if s.keyboards[0].text != topicsRootText {
		t.Fatalf("expected the topics root listing first, got %q", s.keyboards[0].text)
	}
	if s.keyboards[1].text != menuText {
		t.Fatalf("expected /start to end with the hub, got %q", s.keyboards[1].text)
	}
}
