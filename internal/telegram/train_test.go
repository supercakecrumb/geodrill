package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// stubTrainer implements Trainer with canned results. next backs
// NextExercise; practice backs NextPractice — kept separate since a test
// exercising /practice's "next" step must not accidentally observe /train's
// canned result (or vice versa).
type stubTrainer struct {
	next     Prompt
	practice Prompt
	answer   AnswerResult
	stats    Stats
	statsErr error
	due      int
	dueErr   error
	textOK   bool
	textErr  error
	textCall struct {
		userID uuid.UUID
		typed  string
	}
	answerCall struct {
		userID     uuid.UUID
		exerciseID uuid.UUID
		index      int
	}
}

func (s *stubTrainer) NextExercise(ctx context.Context, userID uuid.UUID) (Prompt, error) {
	return s.next, nil
}

func (s *stubTrainer) NextPractice(ctx context.Context, userID uuid.UUID) (Prompt, error) {
	return s.practice, nil
}

func (s *stubTrainer) Answer(ctx context.Context, userID, exerciseID uuid.UUID, optionIndex int) (AnswerResult, error) {
	s.answerCall.userID = userID
	s.answerCall.exerciseID = exerciseID
	s.answerCall.index = optionIndex
	return s.answer, nil
}

func (s *stubTrainer) AnswerText(ctx context.Context, userID uuid.UUID, typed string) (AnswerResult, bool, error) {
	s.textCall.userID = userID
	s.textCall.typed = typed
	return s.answer, s.textOK, s.textErr
}

func (s *stubTrainer) Stats(ctx context.Context, userID uuid.UUID) (Stats, error) {
	return s.stats, s.statsErr
}

func (s *stubTrainer) DueCount(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.due, s.dueErr
}

// ── ans: callback parsing ────────────────────────────────────────────────

func TestAnswerCallbackData_RoundTrip(t *testing.T) {
	id := uuid.New()
	data := answerCallbackData(id, 3)
	gotID, gotIdx, ok := parseAnswerCallback(data)
	if !ok || gotID != id || gotIdx != 3 {
		t.Fatalf("round-trip failed: got (%v,%v,%v), want (%v,3,true)", gotID, gotIdx, ok, id)
	}
	if len(data) > 64 {
		t.Fatalf("ans callback data exceeds Telegram's 64-byte budget: %d bytes", len(data))
	}
}

func TestParseAnswerCallback_Invalid(t *testing.T) {
	cases := []string{
		"",
		"noop",
		"ans:" + uuid.New().String() + ":por",
		"ans:" + uuid.New().String(), // missing index
		"ans:not-a-uuid:0",
		"ans:" + uuid.New().String() + ":-1", // negative index
		"ans:" + uuid.New().String() + ":x",  // non-numeric index
	}
	for _, data := range cases {
		if _, _, ok := parseAnswerCallback(data); ok {
			t.Fatalf("parseAnswerCallback(%q) unexpectedly succeeded", data)
		}
	}
}

// ── prac: callback parsing ────────────────────────────────────────────────

func TestPracticeCallbackData_RoundTrip(t *testing.T) {
	id := uuid.New()
	data := practiceCallbackData(id, 2)
	gotID, gotIdx, ok := parsePracticeCallback(data)
	if !ok || gotID != id || gotIdx != 2 {
		t.Fatalf("round-trip failed: got (%v,%v,%v), want (%v,2,true)", gotID, gotIdx, ok, id)
	}
	if len(data) > 64 {
		t.Fatalf("prac callback data exceeds Telegram's 64-byte budget: %d bytes", len(data))
	}
	// ans: and prac: must never parse as each other's shape.
	if _, _, ok := parseAnswerCallback(data); ok {
		t.Fatalf("a prac: payload must not parse as a ans: callback")
	}
}

func TestParsePracticeCallback_Invalid(t *testing.T) {
	cases := []string{
		"",
		"noop",
		"ans:" + uuid.New().String() + ":0", // the /train prefix, not /practice
		"prac:" + uuid.New().String(),       // missing index
		"prac:not-a-uuid:0",
		"prac:" + uuid.New().String() + ":-1",
	}
	for _, data := range cases {
		if _, _, ok := parsePracticeCallback(data); ok {
			t.Fatalf("parsePracticeCallback(%q) unexpectedly succeeded", data)
		}
	}
}

// ── /train uses the wired trainer ──────────────────────────────────────────────────

func TestHandleTrain_UsesTrainerWhenWired(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{next: Prompt{
		Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "hola",
		Options: []Option{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}}

	s := &fakeSession{userID: 1}
	if err := b.handleTrain(context.Background(), s); err != nil {
		t.Fatalf("handleTrain: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != "hola" {
		t.Fatalf("expected the prompt sent, got %+v", s.keyboards)
	}
}

// ── /practice uses the wired trainer ───────────────────────────────────────────────

func TestHandlePractice_UsesTrainerWhenWired(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{practice: Prompt{
		Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "hola", Practice: true,
		Options: []Option{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}}

	s := &fakeSession{userID: 1}
	if err := b.handlePractice(context.Background(), s); err != nil {
		t.Fatalf("handlePractice: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != "hola" {
		t.Fatalf("expected the practice prompt sent, got %+v", s.keyboards)
	}
	var sawStop bool
	for _, row := range s.keyboards[0].rows {
		for _, btn := range row {
			if btn.Data == dataStopPractice {
				sawStop = true
			}
			if strings.HasPrefix(btn.Data, dataAnswerPrefix) {
				t.Fatalf("a practice option must use the prac: prefix, not ans:, got %q", btn.Data)
			}
		}
	}
	if !sawStop {
		t.Fatalf("expected a Stop-practice button on a practice prompt; rows=%+v", s.keyboards[0].rows)
	}
}

func TestSendPrompt_NoTopics(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	if err := b.sendPrompt(s, storage.User{}, Prompt{Kind: PromptKindNoTopics}); err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != noTopicsText {
		t.Fatalf("expected noTopicsText sent, got %v", s.sent)
	}
}

// ── sendPrompt / sendExercise ────────────────────────────────────────

// ── /stats ─────────────────────────────────────────────────────────────

func TestHandleStats_DormantWhenTrainerNil(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1}
	if err := b.handleStats(context.Background(), s); err != nil {
		t.Fatalf("handleStats: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != statsDormantText {
		t.Fatalf("expected the dormant text, got %v", s.sent)
	}
}

func TestHandleStats_RendersViewModel(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{stats: Stats{ReviewsToday: 3, Streak: 2}}

	s := &fakeSession{userID: 1}
	if err := b.handleStats(context.Background(), s); err != nil {
		t.Fatalf("handleStats: %v", err)
	}
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "Reviews today: 3") {
		t.Fatalf("expected the stats rendered, got %v", s.sent)
	}
}

func TestSendPrompt_NothingDueUsesUserTimezone(t *testing.T) {
	user := storage.User{Timezone: "America/New_York"}
	due := time.Date(2026, 7, 18, 20, 0, 0, 0, time.UTC) // 16:00 in America/New_York
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	if err := b.sendPrompt(s, user, Prompt{Kind: PromptKindNothingDue, DueAt: due}); err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "16:00") {
		t.Fatalf("expected the due time localized to 16:00, got %v", s.sent)
	}
}

func TestSendExercise_ModeTextHasNoButtons(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	p := Prompt{Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "ulica", Mode: quiz.ModeText}
	if err := b.sendExercise(s, p); err != nil {
		t.Fatalf("sendExercise: %v", err)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected one message sent, got %d", len(s.keyboards))
	}
	if s.keyboards[0].rows != nil {
		t.Fatalf("a ModeText exercise must have no buttons, got %+v", s.keyboards[0].rows)
	}
	if !strings.Contains(s.keyboards[0].text, "Type your answer") {
		t.Fatalf("expected a type-your-answer prompt, got %q", s.keyboards[0].text)
	}
}

func TestSendExercise_ModeSingleHasButtons(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	exID := uuid.New()
	p := Prompt{
		Kind: PromptKindExercise, ExerciseID: exID, Text: "hola", Mode: quiz.ModeSingle,
		Options: []Option{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}
	if err := b.sendExercise(s, p); err != nil {
		t.Fatalf("sendExercise: %v", err)
	}
	if len(s.keyboards[0].rows) != 1 || len(s.keyboards[0].rows[0]) != 2 {
		t.Fatalf("expected 2 options on one row, got %+v", s.keyboards[0].rows)
	}
	if s.keyboards[0].rows[0][0].Data != answerCallbackData(exID, 0) {
		t.Fatalf("expected the option's index-based callback data, got %q", s.keyboards[0].rows[0][0].Data)
	}
}

func TestSendExercise_PracticeUsesPracPrefixAndStopButton(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	exID := uuid.New()
	p := Prompt{
		Kind: PromptKindExercise, ExerciseID: exID, Text: "hola", Mode: quiz.ModeSingle, Practice: true,
		Options: []Option{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}
	if err := b.sendExercise(s, p); err != nil {
		t.Fatalf("sendExercise: %v", err)
	}
	rows := s.keyboards[0].rows
	if rows[0][0].Data != practiceCallbackData(exID, 0) {
		t.Fatalf("expected the prac: callback data for a practice option, got %q", rows[0][0].Data)
	}
	if len(rows) != 2 || len(rows[1]) != 1 || rows[1][0].Data != dataStopPractice {
		t.Fatalf("expected a trailing Stop-practice row, got %+v", rows)
	}
}

func TestSendExercise_PhotoEscapesCaption(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	p := Prompt{Kind: PromptKindExercise, MediaPath: "/x.jpg", Text: "a < b", Mode: quiz.ModeSingle}
	if err := b.sendExercise(s, p); err != nil {
		t.Fatalf("sendExercise: %v", err)
	}
	if len(s.photos) != 1 || s.photos[0].caption != "a &lt; b" {
		t.Fatalf("expected an escaped photo caption, got %+v", s.photos)
	}
}

// ── ans: callback handling ───────────────────────────────────────────────

func TestHandleAnswerCallback_GradesEditsAndAdvances(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	exID := uuid.New()
	stub := &stubTrainer{
		answer: AnswerResult{
			Correct: true, Text: "hola", HasMessage: true, MessageID: 99,
			Options: []GradedOption{{Label: "🇪🇸 Spanish", Mark: MarkCorrect}},
		},
		next: Prompt{Kind: PromptKindNothingDue},
	}
	b.trainer = stub

	s := &fakeSession{userID: 1, messageID: 42, data: answerCallbackData(exID, 1)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.answerCall.exerciseID != exID || stub.answerCall.index != 1 {
		t.Fatalf("unexpected Answer call: %+v", stub.answerCall)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 99 {
		t.Fatalf("expected the edit on message 99 (HasMessage), got %+v", s.editedMsgs)
	}
	if len(s.responses) != 1 || s.responses[0] != correctToast {
		t.Fatalf("expected the correct toast, got %v", s.responses)
	}
	if len(s.sent) != 1 { // KindNothingDue -> plain Send
		t.Fatalf("expected the next-result message sent, got %d", len(s.sent))
	}
}

func TestHandleAnswerCallback_Stale(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{answer: AnswerResult{Stale: true}}

	s := &fakeSession{userID: 1, data: answerCallbackData(uuid.New(), 0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != staleToast {
		t.Fatalf("expected the stale toast, got %v", s.responses)
	}
	if len(s.editedMsgs) != 0 {
		t.Fatalf("a stale answer must not edit the message, got %d edits", len(s.editedMsgs))
	}
}

func TestHandleAnswerCallback_PhotoUsesEditCaption(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{
		answer: AnswerResult{Correct: true, Text: "ok", MediaPath: "/x.jpg"},
		next:   Prompt{Kind: PromptKindNothingDue},
	}

	s := &fakeSession{userID: 1, messageID: 7, data: answerCallbackData(uuid.New(), 0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedCaps) != 1 {
		t.Fatalf("expected an EditCaption call for a photo exercise, got %d", len(s.editedCaps))
	}
	if len(s.editedMsgs) != 0 {
		t.Fatalf("a photo exercise must not use EditMessage, got %d", len(s.editedMsgs))
	}
}

func TestHandleAnswerCallback_NilTrainerIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: answerCallbackData(uuid.New(), 0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

// ── prac: callback handling ───────────────────────────────────────────────

func TestHandlePracticeAnswerCallback_GradesAndAdvancesViaPractice(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	exID := uuid.New()
	stub := &stubTrainer{
		answer:   AnswerResult{Correct: true, Text: "hola", HasMessage: true, MessageID: 99},
		practice: Prompt{Kind: PromptKindNothingDue},
		next:     Prompt{Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "SHOULD NOT BE SENT"},
	}
	b.trainer = stub

	s := &fakeSession{userID: 1, messageID: 42, data: practiceCallbackData(exID, 1)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.answerCall.exerciseID != exID || stub.answerCall.index != 1 {
		t.Fatalf("unexpected Answer call: %+v", stub.answerCall)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 99 {
		t.Fatalf("expected the edit on message 99 (HasMessage), got %+v", s.editedMsgs)
	}
	if len(s.responses) != 1 || s.responses[0] != correctToast {
		t.Fatalf("expected the correct toast, got %v", s.responses)
	}
	// Must advance via NextPractice (KindNothingDue -> allCaughtUpText),
	// never via NextExercise's canned "next" result.
	if len(s.sent) != 1 || s.sent[0] != allCaughtUpText {
		t.Fatalf("expected the practice-loop advance, got sent=%v", s.sent)
	}
}

func TestHandlePracticeAnswerCallback_Stale(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{answer: AnswerResult{Stale: true}}

	s := &fakeSession{userID: 1, data: practiceCallbackData(uuid.New(), 0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != staleToast {
		t.Fatalf("expected the stale toast, got %v", s.responses)
	}
	if len(s.editedMsgs) != 0 {
		t.Fatalf("a stale answer must not edit the message, got %d edits", len(s.editedMsgs))
	}
}

func TestHandlePracticeAnswerCallback_NilTrainerIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: practiceCallbackData(uuid.New(), 0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

// ── OnText / handleText ──────────────────────────────────────────────────

func TestHandleText_NilTrainerIsNoop(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, msgText: "ulica"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	if len(s.sent) != 0 || len(s.editedMsgs) != 0 {
		t.Fatalf("expected no side effects when Trainer is nil")
	}
}

func TestHandleText_IgnoresCommands(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubTrainer{textOK: true}
	b.trainer = stub

	s := &fakeSession{userID: 1, msgText: "/unknowncmd"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	if stub.textCall.typed != "" {
		t.Fatalf("expected AnswerText NOT to be called for a command-shaped message, got %q", stub.textCall.typed)
	}
}

func TestHandleText_NoOpenExercise(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{textOK: false}

	s := &fakeSession{userID: 1, msgText: "ulica"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	if len(s.sent) != 0 && len(s.editedMsgs) != 0 {
		t.Fatalf("expected no reply when AnswerText reports ok=false")
	}
}

func TestHandleText_GradesAndAdvances(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubTrainer{
		textOK: true,
		answer: AnswerResult{Correct: false, Text: "ulica", HasMessage: true, MessageID: 5},
		next:   Prompt{Kind: PromptKindNothingDue},
	}
	b.trainer = stub

	s := &fakeSession{userID: 1, msgText: "street"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	if stub.textCall.typed != "street" {
		t.Fatalf("expected AnswerText called with the typed text, got %q", stub.textCall.typed)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 5 {
		t.Fatalf("expected the exercise edited via its own MessageID, got %+v", s.editedMsgs)
	}
	// No callback to Respond() to — the toast is a plain follow-up message.
	if len(s.sent) != 2 { // toast + next-result (KindNothingDue)
		t.Fatalf("expected a toast message plus the next-result message, got %d sends: %v", len(s.sent), s.sent)
	}
	if s.sent[0] != wrongToast {
		t.Fatalf("expected the wrong toast as a plain message, got %q", s.sent[0])
	}
}

func TestHandleText_PracticeAdvancesViaNextPractice(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubTrainer{
		textOK:   true,
		answer:   AnswerResult{Correct: true, Text: "ulica", Practice: true},
		next:     Prompt{Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "SHOULD NOT BE SENT"},
		practice: Prompt{Kind: PromptKindNothingDue},
	}
	b.trainer = stub

	s := &fakeSession{userID: 1, msgText: "street"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	// A practice-flagged AnswerResult must advance via NextPractice
	// (KindNothingDue -> allCaughtUpText), never NextExercise's "next".
	if len(s.sent) != 2 || s.sent[1] != allCaughtUpText {
		t.Fatalf("expected the toast plus the practice-loop advance, got %v", s.sent)
	}
}
