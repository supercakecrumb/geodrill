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
// NextExercise.
type stubTrainer struct {
	next     Prompt
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

// TestHandleStats_RendersViewModel covers /stats when Trainer is wired: it
// must now carry a one-button «⬅️ Menu» keyboard (hub-and-spoke rule — it
// had no keyboard at all before).
func TestHandleStats_RendersViewModel(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.trainer = &stubTrainer{stats: Stats{ReviewsToday: 3, Streak: 2}}

	s := &fakeSession{userID: 1}
	if err := b.handleStats(context.Background(), s); err != nil {
		t.Fatalf("handleStats: %v", err)
	}
	if len(s.keyboards) != 1 || !strings.Contains(s.keyboards[0].text, "Reviews today: 3") {
		t.Fatalf("expected the stats rendered as a keyboard message, got %+v", s.keyboards)
	}
	if len(s.keyboards[0].rows) != 1 || s.keyboards[0].rows[0][0].Data != dataMenuOpen {
		t.Fatalf("expected a single «⬅️ Menu» button, got %+v", s.keyboards[0].rows)
	}
}

// TestSendPrompt_NothingDueUsesUserTimezone covers /train's "nothing due"
// terminal screen: the localized due time (still respected), the reworded
// self-explanatory next-review line, the progress stats Prompt.Summary
// carries, and a one-button «⬅️ Menu» keyboard (hub-and-spoke rule — it had
// no keyboard at all before).
func TestSendPrompt_NothingDueUsesUserTimezone(t *testing.T) {
	user := storage.User{Timezone: "America/New_York"}
	due := time.Date(2026, 7, 18, 20, 0, 0, 0, time.UTC) // 16:00 in America/New_York
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	p := Prompt{
		Kind:  PromptKindNothingDue,
		DueAt: due,
		Summary: DueSummary{
			ReviewsToday:     4,
			ReviewsScheduled: 12,
			LeftToLearn:      7,
		},
	}
	if err := b.sendPrompt(s, user, p); err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected one message sent, got %+v", s.keyboards)
	}
	text := s.keyboards[0].text
	for _, want := range []string{
		"Reviewed today: 4",
		"12 reviews scheduled",
		"7 left to learn",
		"Your next review unlocks at 16:00",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected text to contain %q, got %q", want, text)
		}
	}
	if len(s.keyboards[0].rows) != 1 || s.keyboards[0].rows[0][0].Data != dataMenuOpen {
		t.Fatalf("expected a single «⬅️ Menu» button, got %+v", s.keyboards[0].rows)
	}
}

// TestSendPrompt_NothingDueAllCaughtUp covers the DueAt-zero branch: nothing
// scheduled AND nothing left to learn (study.Service.NextExercise only
// returns a zero DueAt in that case) — the message must say so plainly and
// must NOT print a bogus "come back at 00:00" time.
func TestSendPrompt_NothingDueAllCaughtUp(t *testing.T) {
	user := storage.User{Timezone: "America/New_York"}
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	p := Prompt{
		Kind:    PromptKindNothingDue,
		Summary: DueSummary{ReviewsToday: 9},
	}
	if err := b.sendPrompt(s, user, p); err != nil {
		t.Fatalf("sendPrompt: %v", err)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected one message sent, got %+v", s.keyboards)
	}
	text := s.keyboards[0].text
	if !strings.Contains(text, "all caught up") {
		t.Fatalf("expected an all-caught-up message, got %q", text)
	}
	if !strings.Contains(text, "Reviewed today: 9") {
		t.Fatalf("expected the reviewed-today stat, got %q", text)
	}
	if strings.Contains(text, "unlocks at") || strings.Contains(text, "Come back at") {
		t.Fatalf("expected no bogus next-review time when DueAt is zero, got %q", text)
	}
	if len(s.keyboards[0].rows) != 1 || s.keyboards[0].rows[0][0].Data != dataMenuOpen {
		t.Fatalf("expected a single «⬅️ Menu» button, got %+v", s.keyboards[0].rows)
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

func TestSendExercise_AutocompleteAddsInlineQueryButton(t *testing.T) {
	s := &fakeSession{}
	b := newTestBot(&stubStore{})
	p := Prompt{Kind: PromptKindExercise, ExerciseID: uuid.New(), Text: "France", Mode: quiz.ModeText, Autocomplete: true}
	if err := b.sendExercise(s, p); err != nil {
		t.Fatalf("sendExercise: %v", err)
	}
	rows := s.keyboards[0].rows
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("expected exactly one autocomplete button row, got %+v", rows)
	}
	btn := rows[0][0]
	if !btn.InlineQueryChat {
		t.Fatalf("expected an InlineQueryChat button, got %+v", btn)
	}
	if btn.Label != autocompleteButtonLabel {
		t.Fatalf("expected the autocomplete button label, got %q", btn.Label)
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
	if len(s.keyboards) != 1 { // KindNothingDue -> SendKeyboard with the Menu button
		t.Fatalf("expected the next-result message sent, got %d", len(s.keyboards))
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

// ── stripBotMention ──────────────────────────────────────────────────────

// TestStripBotMention covers handleText's fix for "Nagoya→Japan marked
// wrong (answer parsing)" / "Case-insensitive answer matching": Telegram's
// inline autocomplete inserts the bot's own @mention ahead of a tapped
// suggestion in a group chat, so the raw message becomes
// "@geodriller_bot Japan" instead of "Japan" — grading that raw text fails
// even though the answer is correct, and looked like a case-sensitivity bug
// to the reporter (quiz.TextMatcher already casefolds via quiz.Normalize
// once the mention is out of the way).
func TestStripBotMention(t *testing.T) {
	cases := []struct {
		name string
		text string
		bot  string
		want string
	}{
		{"leading mention stripped", "@geodriller_bot Japan", "geodriller_bot", "Japan"},
		{"username match is case-insensitive", "@GeoDriller_Bot Japan", "geodriller_bot", "Japan"},
		{"surrounding and internal whitespace trimmed", "  @geodriller_bot   spain  ", "geodriller_bot", "spain"},
		{"no mention present", "Japan", "geodriller_bot", "Japan"},
		{"multi-word answer preserved", "Costa Rica", "geodriller_bot", "Costa Rica"},
		{"mid-string mention not stripped", "email me @geodriller_bot", "geodriller_bot", "email me @geodriller_bot"},
		{"empty bot username leaves text unchanged", "@geodriller_bot Japan", "", "@geodriller_bot Japan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripBotMention(c.text, c.bot); got != c.want {
				t.Fatalf("stripBotMention(%q, %q) = %q, want %q", c.text, c.bot, got, c.want)
			}
		})
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
	// No callback to Respond() to — the toast is a plain follow-up message;
	// the next-result (KindNothingDue) is now a keyboard message with the
	// Menu button.
	if len(s.sent) != 1 {
		t.Fatalf("expected a single toast message, got %d sends: %v", len(s.sent), s.sent)
	}
	if s.sent[0] != wrongToast {
		t.Fatalf("expected the wrong toast as a plain message, got %q", s.sent[0])
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected the next-result message sent as a keyboard, got %d", len(s.keyboards))
	}
}

// TestHandleText_SuggestionArrival_UsesExistingFreeTextGradingPath documents
// vibe/spike-autocomplete-inline.md §2's verdict: a message that lands in
// the chat from a tapped inline suggestion is an ordinary OnText update,
// indistinguishable from one the user typed by hand — this package has no
// ChosenInlineResult handling and no result-id decoding anywhere, because
// Session (session.go) exposes no such hooks in the first place:
// MessageText() is the only channel handleText ever reads incoming text
// from, whether it was typed or arrived via InputTextMessageContent.Text
// from a tapped suggestion (buildQueryResults's own doc, bot.go). This test
// feeds handleText exactly that arrival shape — a bare label, "France", as
// a tapped suggestion would send — and asserts it is graded through the
// SAME AnswerText call and edit/advance flow as
// TestHandleText_GradesAndAdvances's hand-typed case, immediately above; no
// separate branch exists to find.
func TestHandleText_SuggestionArrival_UsesExistingFreeTextGradingPath(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubTrainer{
		textOK: true,
		answer: AnswerResult{Correct: true, Text: "France", HasMessage: true, MessageID: 5},
		next:   Prompt{Kind: PromptKindNothingDue},
	}
	b.trainer = stub

	s := &fakeSession{userID: 1, msgText: "France"}
	if err := b.handleText(context.Background(), s); err != nil {
		t.Fatalf("handleText: %v", err)
	}
	if stub.textCall.typed != "France" {
		t.Fatalf("expected AnswerText called with the arrived text verbatim, got %q", stub.textCall.typed)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 5 {
		t.Fatalf("expected the exercise graded/edited exactly as a typed answer would be, got %+v", s.editedMsgs)
	}
	if len(s.sent) != 1 {
		t.Fatalf("expected a single toast message, got %d sends: %v", len(s.sent), s.sent)
	}
	if s.sent[0] != correctToast {
		t.Fatalf("expected the correct toast as a plain message, got %q", s.sent[0])
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected the next-result message sent as a keyboard, got %d", len(s.keyboards))
	}
}
