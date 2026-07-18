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

func newTestBot(tr *stubTrainer, st *stubStore) *Bot {
	return &Bot{
		store:       st,
		svc:         tr,
		logger:      slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 100})),
		now:         time.Now,
		remindState: make(map[uuid.UUID]reminderState),
	}
}

// ── /practice Stop control ───────────────────────────────────────────────

func TestSendPrompt_PracticeAddsStopButton(t *testing.T) {
	st := &stubStore{user: storage.User{ID: uuid.New(), LabelStyle: "name"}}
	b := newTestBot(&stubTrainer{}, st)
	s := &fakeSession{userID: 1}

	p := &train.Prompt{
		ExerciseID: uuid.New(),
		Text:       "Menen katsomaan.",
		Practice:   true,
		Buttons: []train.Button{
			{Key: "fin", Label: "Finnish", CallbackData: "prac:x:fin"},
			{Key: "swe", Label: "Swedish", CallbackData: "prac:x:swe"},
		},
	}
	if err := b.sendPrompt(context.Background(), s, st.user, p); err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected one keyboard sent, got %d", len(s.keyboards))
	}
	var sawStop bool
	for _, row := range s.keyboards[0].rows {
		for _, btn := range row {
			if btn.Data == dataStopPractice {
				sawStop = true
			}
		}
	}
	if !sawStop {
		t.Fatalf("expected a Stop-practice button on a practice prompt; rows=%+v", s.keyboards[0].rows)
	}

	// A non-practice prompt must NOT get a Stop button.
	s2 := &fakeSession{userID: 1}
	p.Practice = false
	if err := b.sendPrompt(context.Background(), s2, st.user, p); err != nil {
		t.Fatalf("sendPrompt (non-practice): %v", err)
	}
	for _, row := range s2.keyboards[0].rows {
		for _, btn := range row {
			if btn.Data == dataStopPractice {
				t.Fatalf("did not expect a Stop button on a /train prompt")
			}
		}
	}
}

func TestHandleStopPractice(t *testing.T) {
	st := &stubStore{
		user:            storage.User{ID: uuid.New(), Timezone: "UTC"},
		practiceTotal:   8,
		practiceCorrect: 6,
	}
	b := newTestBot(&stubTrainer{}, st)
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
	b := newTestBot(&stubTrainer{}, st)
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
	tr := &stubTrainer{
		next: train.NextResult{
			Kind: train.KindExercise,
			Prompt: &train.Prompt{
				ExerciseID: uuid.New(),
				Text:       "Uma frase.",
				Buttons: []train.Button{
					{Label: "🇵🇹 Portuguese", Key: "por"},
					{Label: "🇪🇸 Spanish", Key: "spa"},
				},
			},
		},
	}
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1, data: dataStartTrain}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback(start): %v", err)
	}
	// The tap must be acked (spinner cleared).
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected exactly one empty ack response, got %v", s.responses)
	}
	// The next exercise must be sent as a keyboard message.
	if len(s.keyboards) != 1 {
		t.Fatalf("expected exactly one exercise keyboard, got %d", len(s.keyboards))
	}
	if s.keyboards[0].text != "Uma frase." {
		t.Fatalf("expected the prompt text, got %q", s.keyboards[0].text)
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

// ── handleCallback: recognition tips ─────────────────────────────────────

// tipAnswerResult is a wrong-answer result carrying a sentence + tip, as
// train.Service.Answer produces when the tips provider is wired.
func tipAnswerResult() train.AnswerResult {
	return train.AnswerResult{
		Correct: false,
		Buttons: []train.GradedButton{
			{Key: "por", Name: "Portuguese", Label: "✅ Portuguese", Mark: train.MarkCorrect},
			{Key: "spa", Name: "Spanish", Label: "❌ Spanish", Mark: train.MarkWrong},
		},
		HasMessage:   true,
		MessageID:    99,
		SentenceText: "Não fales com ele.",
		Tip:          "Portuguese: “não” means “no”",
	}
}

func TestHandleCallback_TipEditsMessageInPlace(t *testing.T) {
	tr := &stubTrainer{
		answer: tipAnswerResult(),
		next:   train.NextResult{Kind: train.KindNothingDue},
	}
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1, messageID: 42, data: "ans:" + uuid.NewString() + ":spa"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	if len(s.editedMsgs) != 1 {
		t.Fatalf("expected exactly one EditMessage call, got %d", len(s.editedMsgs))
	}
	if len(s.edits) != 0 {
		t.Fatalf("EditKeyboard must not also run when the tip edit succeeded, got %d calls", len(s.edits))
	}
	em := s.editedMsgs[0]
	if em.messageID != 99 {
		t.Fatalf("expected the tip edit on message 99, got %d", em.messageID)
	}
	if !strings.Contains(em.text, "Não fales com ele.") {
		t.Fatalf("edited text must keep the sentence, got %q", em.text)
	}
	if !strings.Contains(em.text, "\n\n<blockquote>💡 Portuguese: “não” means “no”</blockquote>") {
		t.Fatalf("edited text must append the tip as a blockquote, got %q", em.text)
	}
	if len(em.rows) == 0 {
		t.Fatalf("the tip edit must carry the graded keyboard")
	}

	// Toast + next exercise still delivered.
	if len(s.responses) != 1 || s.responses[0] != wrongToast {
		t.Fatalf("expected toast %q, got %v", wrongToast, s.responses)
	}
	if len(s.sent) != 1 {
		t.Fatalf("expected the next-result message to be sent, got %d sends", len(s.sent))
	}
}

func TestHandleCallback_TipEditFailureFallsBackToKeyboard(t *testing.T) {
	tr := &stubTrainer{
		answer: tipAnswerResult(),
		next:   train.NextResult{Kind: train.KindNothingDue},
	}
	st := &stubStore{user: storage.User{ID: uuid.New(), Timezone: "UTC"}}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1, messageID: 42, data: "ans:" + uuid.NewString() + ":spa", failEditMsg: true}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	if len(s.edits) != 1 {
		t.Fatalf("expected the keyboard-only fallback edit, got %d EditKeyboard calls", len(s.edits))
	}
	if s.edits[0].messageID != 99 {
		t.Fatalf("fallback edit should target message 99, got %d", s.edits[0].messageID)
	}
	// The flow must survive the failure: toast + next exercise.
	if len(s.responses) != 1 || s.responses[0] != wrongToast {
		t.Fatalf("expected toast %q, got %v", wrongToast, s.responses)
	}
	if len(s.sent) != 1 {
		t.Fatalf("expected the next-result message to be sent, got %d sends", len(s.sent))
	}
}

func TestComposeAnsweredText(t *testing.T) {
	text, ok := composeAnsweredText("Han er søn af kongen.", `"af" — the classic Danish tell`)
	if !ok {
		t.Fatalf("compose should succeed for a short sentence")
	}
	want := "Han er søn af kongen.\n\n<blockquote>💡 &#34;af&#34; — the classic Danish tell</blockquote>"
	if text != want {
		t.Fatalf("composed = %q, want %q", text, want)
	}

	// Over the Telegram limit: skip the tip rather than risk a failed edit.
	if _, ok := composeAnsweredText(strings.Repeat("а", telegramMaxMessageLen), "tip"); ok {
		t.Fatalf("compose must refuse texts beyond the message limit")
	}
}

// TestComposeAnsweredText_EscapesHTML guards against a sentence containing
// HTML-special characters breaking Telegram's HTML parse mode: both the
// sentence and the tip must come out escaped, with the only raw angle
// brackets being the <blockquote> tags this function itself adds.
func TestComposeAnsweredText_EscapesHTML(t *testing.T) {
	text, ok := composeAnsweredText("a < b & c", "tip")
	if !ok {
		t.Fatalf("compose should succeed for a short sentence")
	}
	if !strings.Contains(text, "a &lt; b &amp; c") {
		t.Fatalf("composed text must escape the sentence, got %q", text)
	}
	withoutTags := strings.NewReplacer("<blockquote>", "", "</blockquote>", "").Replace(text)
	if strings.ContainsAny(withoutTags, "<>") {
		t.Fatalf("composed text must not contain raw angle brackets outside the blockquote tags, got %q", text)
	}
}

// ── deckPickerRows ───────────────────────────────────────────────────────

func TestDeckPickerRows(t *testing.T) {
	enabledID := uuid.New()
	disabledID := uuid.New()
	decks := []storage.UserDeck{
		{Deck: storage.Deck{ID: enabledID, Slug: "romance", Name: "Romance languages"}, Enabled: true},
		{Deck: storage.Deck{ID: disabledID, Slug: "nordic", Name: "Nordic"}, Enabled: false},
	}

	rows := deckPickerRows(decks)

	var all []Btn
	for _, row := range rows {
		all = append(all, row...)
	}

	// Romance has a curated + audited tips dataset (tips.DeckHasTips), so its
	// button carries the 💡 marker; Nordic doesn't have tips yet and stays plain.
	assertHasBtn(t, all, Btn{Label: "✅ Romance languages 💡", Data: "deck:" + enabledID.String()})
	assertHasBtn(t, all, Btn{Label: "⬜ Nordic", Data: "deck:" + disabledID.String()})

	// The deck picker is now deck-toggles only — cap/reminder/style controls
	// moved to /settings and must not leak back in.
	for _, b := range all {
		if b.Data == "cap:inc" || b.Data == "rem:toggle" || b.Data == "style:cycle" {
			t.Fatalf("deck picker must not carry the %q control (it belongs in /settings)", b.Data)
		}
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

	collect := func(u storage.User) []Btn {
		var all []Btn
		for _, row := range settingsRows(u) {
			all = append(all, row...)
		}
		return all
	}

	all := collect(user)
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
	allOff := collect(user)
	assertHasBtn(t, allOff, Btn{Label: "🔔 reminders: off", Data: "rem:toggle"})
	assertHasBtn(t, allOff, Btn{Label: "🔁 follow-up: off", Data: "fup:toggle"})
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
	b := newTestBot(&stubTrainer{}, st)
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
	b := newTestBot(&stubTrainer{}, st)
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
	b := newTestBot(&stubTrainer{}, st)
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
	b := newTestBot(&stubTrainer{}, st)
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
	b := newTestBot(&stubTrainer{}, st)
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
		want          reminderKind
	}{
		{"first at chosen hour", base, reminderState{}, 9, 3, 0, reminderFirst},
		{"nothing off-hour", base, reminderState{}, 8, 3, 0, reminderNone},
		{"nothing when due is zero", base, reminderState{}, 9, 0, 0, reminderNone},
		{"nothing when reminders disabled", storage.User{RemindersEnabled: false, ReminderHour: 9, FollowUpEnabled: true, FollowUpDelayMin: 60}, reminderState{}, 9, 3, 0, reminderNone},
		{"stale prior-day state still fires first", base, reminderState{day: "2026-07-17", firstSentAt: now.Add(-24 * time.Hour)}, 9, 3, 0, reminderFirst},
		{"follow-up after the delay", base, sent(90*time.Minute, 0), 11, 3, 0, reminderFollowUp},
		{"no follow-up before the delay", base, sent(30*time.Minute, 0), 11, 3, 0, reminderNone},
		{"no follow-up once engaged", base, sent(90*time.Minute, 0), 11, 3, 1, reminderNone},
		{"no follow-up at the cap", base, sent(90*time.Minute, maxFollowUps), 11, 3, 0, reminderNone},
		{"no follow-up when disabled", storage.User{RemindersEnabled: true, ReminderHour: 9, FollowUpEnabled: false, FollowUpDelayMin: 60}, sent(90*time.Minute, 0), 11, 3, 0, reminderNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideReminder(tc.user, tc.st, day, tc.localHour, now, tc.due, tc.reviewedSince)
			if got != tc.want {
				t.Fatalf("decideReminder = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReminderText(t *testing.T) {
	cases := []struct {
		due      int
		followUp bool
		want     string
	}{
		{1, false, "🔔 You have 1 review due today."},
		{5, false, "🔔 You have 5 reviews due today."},
		{1, true, "⏰ Still 1 review waiting — tap to start."},
		{3, true, "⏰ Still 3 reviews waiting — tap to start."},
	}
	for _, tc := range cases {
		if got := reminderText(tc.due, tc.followUp); got != tc.want {
			t.Fatalf("reminderText(%d, %v) = %q, want %q", tc.due, tc.followUp, got, tc.want)
		}
	}
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

// ── answer-button faces & one-row layout ─────────────────────────────────

func TestAnswerLabel(t *testing.T) {
	if got := answerLabel("name", "por", "Portuguese"); got != "🇵🇹 Portuguese" {
		t.Fatalf("style=name mapped key: got %q, want %q", got, "🇵🇹 Portuguese")
	}
	if got := answerLabel("code", "por", "Portuguese"); got != "🇵🇹 PT" {
		t.Fatalf("style=code mapped key: got %q, want %q", got, "🇵🇹 PT")
	}
	if got := answerLabel("plain", "por", "Portuguese"); got != "Portuguese" {
		t.Fatalf("style=plain mapped key: got %q, want %q", got, "Portuguese")
	}

	for _, style := range []string{"name", "code", "plain"} {
		if got := answerLabel(style, "xxx", "Fallback"); got != "Fallback" {
			t.Fatalf("style=%s unknown key should fall back: got %q, want %q", style, got, "Fallback")
		}
		if got := answerLabel(style, "", "Fallback"); got != "Fallback" {
			t.Fatalf("style=%s empty key should fall back: got %q, want %q", style, got, "Fallback")
		}
	}
}

func TestButtonRows_TwoPerRowWithFaces(t *testing.T) {
	rows := buttonRows("code", []train.Button{
		{Key: "por", Label: "Portuguese", CallbackData: "ans:1:por"},
		{Key: "spa", Label: "Spanish", CallbackData: "ans:1:spa"},
		{Key: "ita", Label: "Italian", CallbackData: "ans:1:ita"},
	})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows for 3 buttons (2 then 1), got %d rows", len(rows))
	}
	if len(rows[0]) != 2 {
		t.Fatalf("expected 2 buttons in the first row, got %d", len(rows[0]))
	}
	if len(rows[1]) != 1 {
		t.Fatalf("expected 1 button in the second (trailing) row, got %d", len(rows[1]))
	}

	var all []Btn
	for _, row := range rows {
		all = append(all, row...)
	}
	want := []Btn{
		{Label: "🇵🇹 PT", Data: "ans:1:por"},
		{Label: "🇪🇸 ES", Data: "ans:1:spa"},
		{Label: "🇮🇹 IT", Data: "ans:1:ita"},
	}
	for i, w := range want {
		if all[i] != w {
			t.Fatalf("button %d: got %+v, want %+v", i, all[i], w)
		}
	}
}

func TestGradedButtonRows_FacesDecoratedTwoPerRow(t *testing.T) {
	rows := gradedButtonRows("code", []train.GradedButton{
		{Key: "por", Name: "Portuguese", Label: "✅ Portuguese", Mark: train.MarkCorrect},
		{Key: "spa", Name: "Spanish", Label: "❌ Spanish", Mark: train.MarkWrong},
		{Key: "ita", Name: "Italian", Label: "Italian", Mark: train.MarkNone},
	})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows for 3 graded buttons (2 then 1), got %d rows", len(rows))
	}
	if len(rows[0]) != 2 {
		t.Fatalf("expected 2 buttons in the first row, got %d", len(rows[0]))
	}
	if len(rows[1]) != 1 {
		t.Fatalf("expected 1 button in the second (trailing) row, got %d", len(rows[1]))
	}

	var all []Btn
	for _, row := range rows {
		all = append(all, row...)
	}
	want := []Btn{
		{Label: "✅ 🇵🇹 PT", Data: train.DataNoop},
		{Label: "❌ 🇪🇸 ES", Data: train.DataNoop},
		{Label: "🇮🇹 IT", Data: train.DataNoop},
	}
	for i, w := range want {
		if all[i] != w {
			t.Fatalf("graded button %d: got %+v, want %+v", i, all[i], w)
		}
	}
}

func TestGradedButtonRows_FallbackWhenNoKey(t *testing.T) {
	// With no raw Name set (e.g. a future/unknown language, or a stale caller),
	// the pre-decorated label is used verbatim and the callback is still inert.
	rows := gradedButtonRows("code", []train.GradedButton{
		{Label: "✅ Portuguese", Mark: train.MarkCorrect},
	})
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("expected one button in one row, got rows=%d", len(rows))
	}
	if got := rows[0][0]; got.Label != "✅ Portuguese" || got.Data != train.DataNoop {
		t.Fatalf("fallback graded button: got %+v", got)
	}
}

// ── /help ────────────────────────────────────────────────────────────────

func TestHandleHelp(t *testing.T) {
	b := newTestBot(&stubTrainer{}, &stubStore{user: storage.User{ID: uuid.New()}})
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
