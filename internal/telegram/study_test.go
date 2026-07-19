package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// stubStudyService implements StudyService with canned results, recording
// every AnswerIntro call so tests can assert on the outcome/introID passed
// through.
type stubStudyService struct {
	nextCard    IntroCard
	nextCards   []IntroCard // when set, NextIntro pops one per call, falling back to nextCard once exhausted
	ack         IntroAck
	answerCalls []struct {
		userID  uuid.UUID
		introID uuid.UUID
		outcome engram.IntroOutcome
	}
	summaryAvailable, summaryBudgetLeft int
}

func (s *stubStudyService) NextIntro(ctx context.Context, userID uuid.UUID) (IntroCard, error) {
	if len(s.nextCards) > 0 {
		c := s.nextCards[0]
		s.nextCards = s.nextCards[1:]
		return c, nil
	}
	return s.nextCard, nil
}

func (s *stubStudyService) AnswerIntro(ctx context.Context, userID, introID uuid.UUID, outcome engram.IntroOutcome) (IntroAck, error) {
	s.answerCalls = append(s.answerCalls, struct {
		userID  uuid.UUID
		introID uuid.UUID
		outcome engram.IntroOutcome
	}{userID, introID, outcome})
	return s.ack, nil
}

func (s *stubStudyService) IntroSummary(ctx context.Context, userID uuid.UUID) (int, int, error) {
	return s.summaryAvailable, s.summaryBudgetLeft, nil
}

// ── parseIntroCallback / introCallbackData ──────────────────────────────

func TestIntroCallbackData_RoundTrip(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		outcome engram.IntroOutcome
		media   bool
	}{
		{engram.IntroGotIt, false},
		{engram.IntroKnown, false},
		{engram.IntroTestMe, false},
		{engram.IntroGotIt, true},
		{engram.IntroKnown, true},
		{engram.IntroTestMe, true},
	}
	for _, tc := range cases {
		data := introCallbackData(id, tc.outcome, tc.media)
		gotID, gotOutcome, gotMedia, ok := parseIntroCallback(data)
		if !ok {
			t.Fatalf("parseIntroCallback(%q) failed to parse", data)
		}
		if gotID != id || gotOutcome != tc.outcome || gotMedia != tc.media {
			t.Fatalf("round-trip mismatch for %q: got (%v,%v,%v), want (%v,%v,%v)", data, gotID, gotOutcome, gotMedia, id, tc.outcome, tc.media)
		}
	}
}

func TestIntroCallbackData_Format(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	got := introCallbackData(id, engram.IntroGotIt, true)
	want := "intro:00000000-0000-0000-0000-000000000001:g:1"
	if got != want {
		t.Fatalf("introCallbackData = %q, want %q", got, want)
	}
	if len(got) > 64 {
		t.Fatalf("intro callback data exceeds Telegram's 64-byte budget: %d bytes", len(got))
	}
}

func TestParseIntroCallback_Invalid(t *testing.T) {
	cases := []string{
		"",
		"noop",
		"ans:" + uuid.New().String() + ":por", // a different prefix entirely
		"intro:" + uuid.New().String(),        // missing outcome+media
		"intro:not-a-uuid:g:0",
		"intro:" + uuid.New().String() + ":x:0",  // unknown outcome char
		"intro:" + uuid.New().String() + ":g:2",  // unknown media char
		"intro:" + uuid.New().String() + ":gg:0", // outcome segment too long
	}
	for _, data := range cases {
		if _, _, _, ok := parseIntroCallback(data); ok {
			t.Fatalf("parseIntroCallback(%q) unexpectedly succeeded", data)
		}
	}
}

// ── sendIntroCard ────────────────────────────────────────────────────────

func TestSendIntroCard_Text(t *testing.T) {
	s := &fakeSession{}
	introID := uuid.New()
	card := IntroCard{IntroID: introID, Text: "New letter: ў — where is it used?", Reason: IntroOK}

	b := newTestBot(&stubStore{})
	if err := b.sendIntroCard(context.Background(), s, card); err != nil {
		t.Fatalf("sendIntroCard: %v", err)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected one keyboard sent, got %d", len(s.keyboards))
	}
	if s.keyboards[0].text != card.Text {
		t.Fatalf("expected the card text sent verbatim (plain mode), got %q", s.keyboards[0].text)
	}
	if len(s.keyboards[0].rows) != 1 || len(s.keyboards[0].rows[0]) != 3 {
		t.Fatalf("expected the 3 outcome buttons on one row, got %+v", s.keyboards[0].rows)
	}
	var gotData []string
	for _, btn := range s.keyboards[0].rows[0] {
		gotData = append(gotData, btn.Data)
	}
	wantG := introCallbackData(introID, engram.IntroGotIt, false)
	wantK := introCallbackData(introID, engram.IntroKnown, false)
	wantT := introCallbackData(introID, engram.IntroTestMe, false)
	for _, want := range []string{wantG, wantK, wantT} {
		found := false
		for _, got := range gotData {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected button data %q among %v", want, gotData)
		}
	}
}

func TestSendIntroCard_Photo(t *testing.T) {
	s := &fakeSession{}
	introID := uuid.New()
	card := IntroCard{IntroID: introID, Text: "a < b", MediaPath: "/seeds/media/x.jpg", Reason: IntroOK}

	b := newTestBot(&stubStore{})
	if err := b.sendIntroCard(context.Background(), s, card); err != nil {
		t.Fatalf("sendIntroCard: %v", err)
	}
	if len(s.photos) != 1 {
		t.Fatalf("expected one photo sent, got %d", len(s.photos))
	}
	if s.photos[0].path != card.MediaPath {
		t.Fatalf("expected photo path %q, got %q", card.MediaPath, s.photos[0].path)
	}
	if s.photos[0].caption != "a &lt; b" {
		t.Fatalf("expected the photo caption HTML-escaped, got %q", s.photos[0].caption)
	}
	// The media flag must be baked into each button's callback data.
	for _, btn := range s.photos[0].rows[0] {
		if !strings.HasSuffix(btn.Data, ":1") {
			t.Fatalf("expected media=1 in button data for a photo card, got %q", btn.Data)
		}
	}
}

// TestSendIntroCard_ClosersByReason covers /study's terminal states (nothing
// left to introduce, or today's budget spent): the message text is
// untouched (that's a separate task's concern), but each now carries a
// one-button «⬅️ Menu» keyboard back to the hub (hub-and-spoke rule).
func TestSendIntroCard_ClosersByReason(t *testing.T) {
	cases := []struct {
		name string
		card IntroCard
		want string
	}{
		{"none available", IntroCard{Reason: IntroNoneAvailable}, "Nothing new to introduce"},
		{"budget exhausted", IntroCard{Reason: IntroBudgetExhausted, IntroducedToday: 7}, "introduced 7 today"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeSession{}
			b := newTestBot(&stubStore{})
			if err := b.sendIntroCard(context.Background(), s, tc.card); err != nil {
				t.Fatalf("sendIntroCard: %v", err)
			}
			if len(s.sent) != 0 {
				t.Fatalf("expected no plain Send (now a keyboard message), got %d", len(s.sent))
			}
			if len(s.keyboards) != 1 {
				t.Fatalf("expected one keyboard message sent, got %d", len(s.keyboards))
			}
			if !strings.Contains(s.keyboards[0].text, tc.want) {
				t.Fatalf("expected closer text to contain %q, got %q", tc.want, s.keyboards[0].text)
			}
			if len(s.keyboards[0].rows) != 1 || len(s.keyboards[0].rows[0]) != 1 ||
				s.keyboards[0].rows[0][0].Data != dataMenuOpen {
				t.Fatalf("expected a single «⬅️ Menu» button, got %+v", s.keyboards[0].rows)
			}
		})
	}
}

// ── /study, /introduce ───────────────────────────────────────────────────

func TestHandleStudy_DormantWhenNil(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1}
	if err := b.handleStudy(context.Background(), s); err != nil {
		t.Fatalf("handleStudy: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != studyDormantText {
		t.Fatalf("expected the dormant message, got %v", s.sent)
	}
}

func TestHandleStudy_SendsCard(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.study = &stubStudyService{nextCard: IntroCard{IntroID: uuid.New(), Text: "hello", Reason: IntroOK}}

	s := &fakeSession{userID: 1}
	if err := b.handleStudy(context.Background(), s); err != nil {
		t.Fatalf("handleStudy: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != "hello" {
		t.Fatalf("expected the card sent as a keyboard message, got %+v", s.keyboards)
	}
}

// ── study: callback ──────────────────────────────────────────────────────

func TestHandleCallback_StudyStart(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	b.study = &stubStudyService{nextCard: IntroCard{IntroID: uuid.New(), Text: "card text", Reason: IntroOK}}

	s := &fakeSession{userID: 1, data: dataStudyStart}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected an empty ack, got %v", s.responses)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected the intro card sent, got %d keyboards", len(s.keyboards))
	}
}

// ── intro: callback ──────────────────────────────────────────────────────

func TestHandleIntroCallback_GradesAndAdvancesText(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	introID := uuid.New()
	stub := &stubStudyService{
		ack:      IntroAck{Text: "Got it — now in your reviews."},
		nextCard: IntroCard{IntroID: uuid.New(), Text: "next card", Reason: IntroOK},
	}
	b.study = stub

	s := &fakeSession{userID: 1, messageID: 55, data: introCallbackData(introID, engram.IntroGotIt, false)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}

	if len(stub.answerCalls) != 1 {
		t.Fatalf("expected exactly one AnswerIntro call, got %d", len(stub.answerCalls))
	}
	call := stub.answerCalls[0]
	if call.introID != introID || call.outcome != engram.IntroGotIt || call.userID != st.user.ID {
		t.Fatalf("unexpected AnswerIntro call: %+v", call)
	}

	if len(s.editedMsgs) != 1 {
		t.Fatalf("expected one EditMessage call (text card), got %d", len(s.editedMsgs))
	}
	if s.editedMsgs[0].messageID != 55 {
		t.Fatalf("expected the edit on message 55, got %d", s.editedMsgs[0].messageID)
	}
	if s.editedMsgs[0].text != "Got it — now in your reviews." {
		t.Fatalf("expected the escaped ack text, got %q", s.editedMsgs[0].text)
	}
	if len(s.editedCaps) != 0 {
		t.Fatalf("a text card must not use EditCaption, got %d calls", len(s.editedCaps))
	}

	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected an empty ack response, got %v", s.responses)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != "next card" {
		t.Fatalf("expected the next card sent, got %+v", s.keyboards)
	}
}

func TestHandleIntroCallback_PhotoCardUsesEditCaption(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	introID := uuid.New()
	b.study = &stubStudyService{
		ack:      IntroAck{Text: "ok"},
		nextCard: IntroCard{Reason: IntroNoneAvailable},
	}

	s := &fakeSession{userID: 1, messageID: 55, data: introCallbackData(introID, engram.IntroKnown, true)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedCaps) != 1 {
		t.Fatalf("expected one EditCaption call for a photo card, got %d", len(s.editedCaps))
	}
	if len(s.editedMsgs) != 0 {
		t.Fatalf("a photo card must not use EditMessage, got %d calls", len(s.editedMsgs))
	}
	// Nothing left to introduce: the closer message, sent as a one-button
	// «⬅️ Menu» keyboard (hub-and-spoke rule), not a next card.
	if len(s.keyboards) != 1 {
		t.Fatalf("expected the closer message sent as a keyboard, got %d", len(s.keyboards))
	}
}

func TestHandleIntroCallback_InvalidDataIsInert(t *testing.T) {
	st := &stubStore{user: newTestUser()}
	b := newTestBot(st)
	stub := &stubStudyService{}
	b.study = stub

	s := &fakeSession{userID: 1, data: "intro:garbage"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(stub.answerCalls) != 0 {
		t.Fatalf("expected no AnswerIntro call for malformed data, got %d", len(stub.answerCalls))
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

func TestHandleIntroCallback_NilStudyServiceIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: introCallbackData(uuid.New(), engram.IntroGotIt, false)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
	if len(s.editedMsgs) != 0 || len(s.keyboards) != 0 {
		t.Fatalf("expected no edit/send when StudyService is nil")
	}
}

// newTestUser is a small helper for tests that don't care about most User
// fields but need a stable ID for stub call assertions.
func newTestUser() storage.User {
	return storage.User{ID: uuid.New(), Timezone: "UTC"}
}
