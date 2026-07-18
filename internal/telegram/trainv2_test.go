package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/train"
)

// stubTrainerV2 implements TrainerV2 with canned results. next backs
// NextExerciseV2; practice backs NextPracticeV2 — kept separate since a test
// exercising /practice's "next" step must not accidentally observe /train's
// canned result (or vice versa).
type stubTrainerV2 struct {
	next     PromptV2
	practice PromptV2
	answer   AnswerResultV2
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

func (s *stubTrainerV2) NextExerciseV2(ctx context.Context, userID uuid.UUID) (PromptV2, error) {
	return s.next, nil
}

func (s *stubTrainerV2) NextPracticeV2(ctx context.Context, userID uuid.UUID) (PromptV2, error) {
	return s.practice, nil
}

func (s *stubTrainerV2) AnswerV2(ctx context.Context, userID, exerciseID uuid.UUID, optionIndex int) (AnswerResultV2, error) {
	s.answerCall.userID = userID
	s.answerCall.exerciseID = exerciseID
	s.answerCall.index = optionIndex
	return s.answer, nil
}

func (s *stubTrainerV2) AnswerText(ctx context.Context, userID uuid.UUID, typed string) (AnswerResultV2, bool, error) {
	s.textCall.userID = userID
	s.textCall.typed = typed
	return s.answer, s.textOK, s.textErr
}

// ── v2a: callback parsing ────────────────────────────────────────────────

func TestV2AnswerCallbackData_RoundTrip(t *testing.T) {
	id := uuid.New()
	data := v2AnswerCallbackData(id, 3)
	gotID, gotIdx, ok := parseV2AnswerCallback(data)
	if !ok || gotID != id || gotIdx != 3 {
		t.Fatalf("round-trip failed: got (%v,%v,%v), want (%v,3,true)", gotID, gotIdx, ok, id)
	}
	if len(data) > 64 {
		t.Fatalf("v2a callback data exceeds Telegram's 64-byte budget: %d bytes", len(data))
	}
}

func TestParseV2AnswerCallback_Invalid(t *testing.T) {
	cases := []string{
		"",
		"noop",
		"ans:" + uuid.New().String() + ":por",
		"v2a:" + uuid.New().String(), // missing index
		"v2a:not-a-uuid:0",
		"v2a:" + uuid.New().String() + ":-1", // negative index
		"v2a:" + uuid.New().String() + ":x",  // non-numeric index
	}
	for _, data := range cases {
		if _, _, ok := parseV2AnswerCallback(data); ok {
			t.Fatalf("parseV2AnswerCallback(%q) unexpectedly succeeded", data)
		}
	}
}

// ── v2p: callback parsing ────────────────────────────────────────────────

func TestV2PracticeCallbackData_RoundTrip(t *testing.T) {
	id := uuid.New()
	data := v2PracticeCallbackData(id, 2)
	gotID, gotIdx, ok := parseV2PracticeCallback(data)
	if !ok || gotID != id || gotIdx != 2 {
		t.Fatalf("round-trip failed: got (%v,%v,%v), want (%v,2,true)", gotID, gotIdx, ok, id)
	}
	if len(data) > 64 {
		t.Fatalf("v2p callback data exceeds Telegram's 64-byte budget: %d bytes", len(data))
	}
	// v2a: and v2p: must never parse as each other's shape.
	if _, _, ok := parseV2AnswerCallback(data); ok {
		t.Fatalf("a v2p: payload must not parse as a v2a: callback")
	}
}

func TestParseV2PracticeCallback_Invalid(t *testing.T) {
	cases := []string{
		"",
		"noop",
		"v2a:" + uuid.New().String() + ":0", // the /train prefix, not /practice
		"v2p:" + uuid.New().String(),        // missing index
		"v2p:not-a-uuid:0",
		"v2p:" + uuid.New().String() + ":-1",
	}
	for _, data := range cases {
		if _, _, ok := parseV2PracticeCallback(data); ok {
			t.Fatalf("parseV2PracticeCallback(%q) unexpectedly succeeded", data)
		}
	}
}

// ── /train V2 preference ─────────────────────────────────────────────────

func TestHandleTrain_PrefersV2WhenWired(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	b.trainerV2 = &stubTrainerV2{next: PromptV2{
		Kind: PromptV2KindExercise, ExerciseID: uuid.New(), Text: "hola",
		Options: []OptionV2{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}}

	s := &fakeSession{userID: 1}
	if err := b.handleTrain(context.Background(), s); err != nil {
		t.Fatalf("handleTrain: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != "hola" {
		t.Fatalf("expected the v2 prompt sent, got %+v", s.keyboards)
	}
}

func TestHandleTrain_LegacyWhenV2Nil(t *testing.T) {
	tr := &stubTrainer{next: train.NextResult{Kind: train.KindNothingDue}}
	st := &stubStore{user: newTestUser()}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1}
	if err := b.handleTrain(context.Background(), s); err != nil {
		t.Fatalf("handleTrain: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != allCaughtUpText {
		t.Fatalf("expected the legacy nothing-due path, got %v", s.sent)
	}
}

// ── /practice V2 preference ──────────────────────────────────────────────

func TestHandlePractice_PrefersV2WhenWired(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	b.trainerV2 = &stubTrainerV2{practice: PromptV2{
		Kind: PromptV2KindExercise, ExerciseID: uuid.New(), Text: "hola", Practice: true,
		Options: []OptionV2{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}}

	s := &fakeSession{userID: 1}
	if err := b.handlePractice(context.Background(), s); err != nil {
		t.Fatalf("handlePractice: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != "hola" {
		t.Fatalf("expected the v2 practice prompt sent, got %+v", s.keyboards)
	}
	var sawStop bool
	for _, row := range s.keyboards[0].rows {
		for _, btn := range row {
			if btn.Data == dataStopPractice {
				sawStop = true
			}
			if strings.HasPrefix(btn.Data, dataV2AnswerPrefix) {
				t.Fatalf("a v2 practice option must use the v2p: prefix, not v2a:, got %q", btn.Data)
			}
		}
	}
	if !sawStop {
		t.Fatalf("expected a Stop-practice button on a v2 practice prompt; rows=%+v", s.keyboards[0].rows)
	}
}

func TestHandlePractice_LegacyWhenV2Nil(t *testing.T) {
	tr := &stubTrainer{next: train.NextResult{Kind: train.KindNoDecks}}
	st := &stubStore{user: newTestUser()}
	b := newTestBot(tr, st)

	s := &fakeSession{userID: 1}
	if err := b.handlePractice(context.Background(), s); err != nil {
		t.Fatalf("handlePractice: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != noDecksText {
		t.Fatalf("expected the legacy no-decks path, got %v", s.sent)
	}
}

func TestSendPromptV2_NoTopics(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubTrainer{}, &stubStore{})
	if err := b.sendPromptV2(s, storage.User{}, PromptV2{Kind: PromptV2KindNoTopics}); err != nil {
		t.Fatalf("sendPromptV2: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != noTopicsText {
		t.Fatalf("expected noTopicsText sent, got %v", s.sent)
	}
}

// ── sendPromptV2 / sendExerciseV2 ────────────────────────────────────────

func TestSendPromptV2_NothingDueUsesUserTimezone(t *testing.T) {
	user := storage.User{Timezone: "America/New_York"}
	due := time.Date(2026, 7, 18, 20, 0, 0, 0, time.UTC) // 16:00 in America/New_York
	s := &fakeSession{}
	b := newTestBot(&stubTrainer{}, &stubStore{})
	if err := b.sendPromptV2(s, user, PromptV2{Kind: PromptV2KindNothingDue, DueAt: due}); err != nil {
		t.Fatalf("sendPromptV2: %v", err)
	}
	if len(s.sent) != 1 || !strings.Contains(s.sent[0], "16:00") {
		t.Fatalf("expected the due time localized to 16:00, got %v", s.sent)
	}
}

func TestSendExerciseV2_ModeTextHasNoButtons(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubTrainer{}, &stubStore{})
	p := PromptV2{Kind: PromptV2KindExercise, ExerciseID: uuid.New(), Text: "ulica", Mode: quiz.ModeText}
	if err := b.sendExerciseV2(s, p); err != nil {
		t.Fatalf("sendExerciseV2: %v", err)
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

func TestSendExerciseV2_ModeSingleHasButtons(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubTrainer{}, &stubStore{})
	exID := uuid.New()
	p := PromptV2{
		Kind: PromptV2KindExercise, ExerciseID: exID, Text: "hola", Mode: quiz.ModeSingle,
		Options: []OptionV2{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}
	if err := b.sendExerciseV2(s, p); err != nil {
		t.Fatalf("sendExerciseV2: %v", err)
	}
	if len(s.keyboards[0].rows) != 1 || len(s.keyboards[0].rows[0]) != 2 {
		t.Fatalf("expected 2 options on one row, got %+v", s.keyboards[0].rows)
	}
	if s.keyboards[0].rows[0][0].Data != v2AnswerCallbackData(exID, 0) {
		t.Fatalf("expected the option's index-based callback data, got %q", s.keyboards[0].rows[0][0].Data)
	}
}

func TestSendExerciseV2_PracticeUsesV2pPrefixAndStopButton(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubTrainer{}, &stubStore{})
	exID := uuid.New()
	p := PromptV2{
		Kind: PromptV2KindExercise, ExerciseID: exID, Text: "hola", Mode: quiz.ModeSingle, Practice: true,
		Options: []OptionV2{{Index: 0, Label: "🇪🇸 Spanish"}, {Index: 1, Label: "🇵🇹 Portuguese"}},
	}
	if err := b.sendExerciseV2(s, p); err != nil {
		t.Fatalf("sendExerciseV2: %v", err)
	}
	rows := s.keyboards[0].rows
	if rows[0][0].Data != v2PracticeCallbackData(exID, 0) {
		t.Fatalf("expected the v2p: callback data for a practice option, got %q", rows[0][0].Data)
	}
	if len(rows) != 2 || len(rows[1]) != 1 || rows[1][0].Data != dataStopPractice {
		t.Fatalf("expected a trailing Stop-practice row, got %+v", rows)
	}
}

func TestSendExerciseV2_PhotoEscapesCaption(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubTrainer{}, &stubStore{})
	p := PromptV2{Kind: PromptV2KindExercise, MediaPath: "/x.jpg", Text: "a < b", Mode: quiz.ModeSingle}
	if err := b.sendExerciseV2(s, p); err != nil {
		t.Fatalf("sendExerciseV2: %v", err)
	}
	if len(s.photos) != 1 || s.photos[0].caption != "a &lt; b" {
		t.Fatalf("expected an escaped photo caption, got %+v", s.photos)
	}
}

// ── v2a: callback handling ───────────────────────────────────────────────

func TestHandleV2AnswerCallback_GradesEditsAndAdvances(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	exID := uuid.New()
	stub := &stubTrainerV2{
		answer: AnswerResultV2{
			Correct: true, Text: "hola", HasMessage: true, MessageID: 99,
			Options: []GradedOptionV2{{Label: "🇪🇸 Spanish", Mark: train.MarkCorrect}},
		},
		next: PromptV2{Kind: PromptV2KindNothingDue},
	}
	b.trainerV2 = stub

	s := &fakeSession{userID: 1, messageID: 42, data: v2AnswerCallbackData(exID, 1)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.answerCall.exerciseID != exID || stub.answerCall.index != 1 {
		t.Fatalf("unexpected AnswerV2 call: %+v", stub.answerCall)
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

func TestHandleV2AnswerCallback_Stale(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	b.trainerV2 = &stubTrainerV2{answer: AnswerResultV2{Stale: true}}

	s := &fakeSession{userID: 1, data: v2AnswerCallbackData(uuid.New(), 0)}
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

func TestHandleV2AnswerCallback_PhotoUsesEditCaption(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	b.trainerV2 = &stubTrainerV2{
		answer: AnswerResultV2{Correct: true, Text: "ok", MediaPath: "/x.jpg"},
		next:   PromptV2{Kind: PromptV2KindNothingDue},
	}

	s := &fakeSession{userID: 1, messageID: 7, data: v2AnswerCallbackData(uuid.New(), 0)}
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

func TestHandleV2AnswerCallback_NilTrainerV2IsInert(t *testing.T) {
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: v2AnswerCallbackData(uuid.New(), 0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

// ── v2p: callback handling ───────────────────────────────────────────────

func TestHandleV2PracticeAnswerCallback_GradesAndAdvancesViaPractice(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	exID := uuid.New()
	stub := &stubTrainerV2{
		answer:   AnswerResultV2{Correct: true, Text: "hola", HasMessage: true, MessageID: 99},
		practice: PromptV2{Kind: PromptV2KindNothingDue},
		next:     PromptV2{Kind: PromptV2KindExercise, ExerciseID: uuid.New(), Text: "SHOULD NOT BE SENT"},
	}
	b.trainerV2 = stub

	s := &fakeSession{userID: 1, messageID: 42, data: v2PracticeCallbackData(exID, 1)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.answerCall.exerciseID != exID || stub.answerCall.index != 1 {
		t.Fatalf("unexpected AnswerV2 call: %+v", stub.answerCall)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 99 {
		t.Fatalf("expected the edit on message 99 (HasMessage), got %+v", s.editedMsgs)
	}
	if len(s.responses) != 1 || s.responses[0] != correctToast {
		t.Fatalf("expected the correct toast, got %v", s.responses)
	}
	// Must advance via NextPracticeV2 (KindNothingDue -> allCaughtUpText),
	// never via NextExerciseV2's canned "next" result.
	if len(s.sent) != 1 || s.sent[0] != allCaughtUpText {
		t.Fatalf("expected the practice-loop advance, got sent=%v", s.sent)
	}
}

func TestHandleV2PracticeAnswerCallback_Stale(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	b.trainerV2 = &stubTrainerV2{answer: AnswerResultV2{Stale: true}}

	s := &fakeSession{userID: 1, data: v2PracticeCallbackData(uuid.New(), 0)}
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

func TestHandleV2PracticeAnswerCallback_NilTrainerV2IsInert(t *testing.T) {
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: v2PracticeCallbackData(uuid.New(), 0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

// ── OnText / handleText ──────────────────────────────────────────────────

func TestHandleText_NilTrainerV2IsNoop(t *testing.T) {
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, msgText: "ulica"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	if len(s.sent) != 0 || len(s.editedMsgs) != 0 {
		t.Fatalf("expected no side effects when TrainerV2 is nil")
	}
}

func TestHandleText_IgnoresCommands(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	stub := &stubTrainerV2{textOK: true}
	b.trainerV2 = stub

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
	b := newTestBot(&stubTrainer{}, st)
	b.trainerV2 = &stubTrainerV2{textOK: false}

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
	b := newTestBot(&stubTrainer{}, st)
	stub := &stubTrainerV2{
		textOK: true,
		answer: AnswerResultV2{Correct: false, Text: "ulica", HasMessage: true, MessageID: 5},
		next:   PromptV2{Kind: PromptV2KindNothingDue},
	}
	b.trainerV2 = stub

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

func TestHandleText_PracticeAdvancesViaNextPracticeV2(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(&stubTrainer{}, st)
	stub := &stubTrainerV2{
		textOK:   true,
		answer:   AnswerResultV2{Correct: true, Text: "ulica", Practice: true},
		next:     PromptV2{Kind: PromptV2KindExercise, ExerciseID: uuid.New(), Text: "SHOULD NOT BE SENT"},
		practice: PromptV2{Kind: PromptV2KindNothingDue},
	}
	b.trainerV2 = stub

	s := &fakeSession{userID: 1, msgText: "street"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	// A practice-flagged AnswerResultV2 must advance via NextPracticeV2
	// (KindNothingDue -> allCaughtUpText), never NextExerciseV2's "next".
	if len(s.sent) != 2 || s.sent[1] != allCaughtUpText {
		t.Fatalf("expected the toast plus the practice-loop advance, got %v", s.sent)
	}
}
