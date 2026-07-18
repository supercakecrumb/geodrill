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

	practiceTotal   int
	practiceCorrect int
	practiceSince   time.Time // records the arg passed to PracticeStatsSince

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

func (s *stubStore) PracticeStatsSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, int, error) {
	s.practiceSince = since
	return s.practiceTotal, s.practiceCorrect, nil
}

func newTestBot(st *stubStore) *Bot {
	return &Bot{
		store:       st,
		logger:      slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 100})),
		now:         time.Now,
		remindState: make(map[uuid.UUID]reminderState),
	}
}

// ── /practice Stop control ───────────────────────────────────────────────
//
// The Stop button itself (its rendering) is covered by train_test.go's
// TestSendExercise_PracticeUsesPracPrefixAndStopButton; the tests below
// cover handleStopPractice, which is independent of Trainer/legacy trainer
// (it only reads storage.Store.PracticeStatsSince).

func TestHandleStopPractice(t *testing.T) {
	st := &stubStore{
		user:            storage.User{ID: uuid.New(), Timezone: "UTC"},
		practiceTotal:   8,
		practiceCorrect: 6,
	}
	b := newTestBot(st)
	// A recorded session start so the summary counts from there.
	b.markPracticeStart(7, time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC))

	s := &fakeSession{userID: 7, messageID: 55, data: dataStopPractice}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback(stop): %v", err)
	}

	if len(s.editedMsgs) != 1 {
		t.Fatalf("expected the last message to be edited once, got %d", len(s.editedMsgs))
	}
	edit := s.editedMsgs[0]
	if edit.messageID != 55 {
		t.Fatalf("expected edit on the Stop message (55), got %d", edit.messageID)
	}
	if len(edit.rows) != 0 {
		t.Fatalf("expected the keyboard removed on stop, got %d rows", len(edit.rows))
	}
	for _, want := range []string{"Answered: 8", "Correct: 6 (75%)"} {
		if !strings.Contains(edit.text, want) {
			t.Fatalf("expected summary to contain %q; got:\n%s", want, edit.text)
		}
	}
	// No next exercise should be sent when stopping.
	if len(s.keyboards) != 0 {
		t.Fatalf("expected no next exercise on stop, got %d keyboards", len(s.keyboards))
	}
	// The session start must be consumed (cleared).
	if _, ok := b.takePracticeStart(7); ok {
		t.Fatalf("expected the practice session start to be cleared after stop")
	}
}

func TestHandleStopPractice_NoAnswers(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(st)
	b.markPracticeStart(7, time.Now())
	s := &fakeSession{userID: 7, messageID: 55, data: dataStopPractice}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback(stop): %v", err)
	}
	if len(s.editedMsgs) != 1 || !strings.Contains(s.editedMsgs[0].text, "no answers") {
		t.Fatalf("expected a no-answers summary; got %+v", s.editedMsgs)
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
	assertHasBtn(t, all, Btn{Label: "🔤 Flag + name", Data: "style:cycle"})
	assertHasBtn(t, all, Btn{Label: "🔔 reminders: on", Data: "rem:toggle"})
	assertHasBtn(t, all, Btn{Label: "at 09:00", Data: "noop"})
	assertHasBtn(t, all, Btn{Label: "🕘 -1h", Data: "rhour:dec"})
	assertHasBtn(t, all, Btn{Label: "+1h 🕙", Data: "rhour:inc"})
	assertHasBtn(t, all, Btn{Label: "🔁 follow-up: on", Data: "fup:toggle"})
	assertHasBtn(t, all, Btn{Label: "⏱ follow-up after 60 min", Data: "fupdelay:cycle"})

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
	assertHasBtn(t, all, Btn{Label: "🎯 -1", Data: "icap:dec"})
	assertHasBtn(t, all, Btn{Label: "intro cap: 12", Data: "noop"})
	assertHasBtn(t, all, Btn{Label: "🎯 +1", Data: "icap:inc"})
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

	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 11 {
		t.Fatalf("expected icap:inc to raise the cap to 11, got %d", stub.cap)
	}
	if len(s.edits) != 1 {
		t.Fatalf("expected the settings keyboard re-rendered in place, got %d edits", len(s.edits))
	}

	s.data = "icap:dec"
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != 10 {
		t.Fatalf("expected icap:dec to lower the cap back to 10, got %d", stub.cap)
	}
}

func TestHandleIntroCapChange_ClampsToBounds(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubIntroCapStore{cap: maxIntroCap}
	b.introCap = stub

	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.cap != maxIntroCap {
		t.Fatalf("expected the cap clamped at %d, got %d", maxIntroCap, stub.cap)
	}
}

func TestHandleIntroCapChange_NilStoreIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, messageID: 42, data: "icap:inc"}
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

func TestHelpTextFor(t *testing.T) {
	if got := helpTextFor(false, false); got != helpText {
		t.Fatalf("helpTextFor(false, false) must return helpText verbatim")
	}
	studyOnly := helpTextFor(true, false)
	if !strings.Contains(studyOnly, "/study") || strings.Contains(studyOnly, "/topics") {
		t.Fatalf("helpTextFor(true, false) = %q, expected /study but not /topics", studyOnly)
	}
	topicsOnly := helpTextFor(false, true)
	if !strings.Contains(topicsOnly, "/topics") || strings.Contains(topicsOnly, "/study") {
		t.Fatalf("helpTextFor(false, true) = %q, expected /topics but not /study", topicsOnly)
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
	}

	out := formatStats(st)

	for _, want := range []string{
		"Reviews today: 12",
		"Reviews this week: 50",
		"Accuracy: 83%",
		"Streak: 4 days",
		"Introduced: 30 · Known: 5",
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

func TestHandleHelp(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	s := &fakeSession{userID: 1}

	if err := b.handleHelp(context.Background(), s); err != nil {
		t.Fatalf("handleHelp: %v", err)
	}
	if len(s.sent) != 1 {
		t.Fatalf("expected one help message, got %d", len(s.sent))
	}
	for _, want := range []string{"/train", "/practice", "/decks", "/stats", "/help"} {
		if !strings.Contains(s.sent[0], want) {
			t.Fatalf("expected help text to mention %q; got:\n%s", want, s.sent[0])
		}
	}
	if !strings.Contains(s.sent[0], "Sentences: Tatoeba (tatoeba.org), CC-BY.") {
		t.Fatalf("expected help text to carry the Tatoeba CC-BY credit line; got:\n%s", s.sent[0])
	}
}

func TestHandleHelp_MentionsStudyAndTopicsWhenWired(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	b.study = &stubStudyService{}
	b.topics = &stubTopicService{}
	s := &fakeSession{userID: 1}

	if err := b.handleHelp(context.Background(), s); err != nil {
		t.Fatalf("handleHelp: %v", err)
	}
	for _, want := range []string{"/study", "/introduce", "/topics"} {
		if !strings.Contains(s.sent[0], want) {
			t.Fatalf("expected help text to mention %q when wired; got:\n%s", want, s.sent[0])
		}
	}
}
