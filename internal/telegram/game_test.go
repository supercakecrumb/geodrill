package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/game"
)

// ── stubGameService ──────────────────────────────────────────────────────

// stubGameService implements GameService with canned results, recording
// every call so tests can assert on the arguments passed through (mirrors
// study_test.go's stubStudyService).
type stubGameService struct {
	catalog    []game.Language
	catalogErr error

	round    game.Round
	roundOK  bool
	roundErr error

	nextRoundCalls []struct {
		userID uuid.UUID
		streak int
		used   map[uuid.UUID]bool
	}

	finishBest, finishRuns int
	finishErr              error
	finishCalls            []struct {
		userID uuid.UUID
		streak int
		at     time.Time
	}
}

func (s *stubGameService) Catalog(ctx context.Context) ([]game.Language, error) {
	return s.catalog, s.catalogErr
}

func (s *stubGameService) NextRound(ctx context.Context, userID uuid.UUID, catalog []game.Language, streak int, used map[uuid.UUID]bool) (game.Round, bool, error) {
	s.nextRoundCalls = append(s.nextRoundCalls, struct {
		userID uuid.UUID
		streak int
		used   map[uuid.UUID]bool
	}{userID, streak, used})
	return s.round, s.roundOK, s.roundErr
}

func (s *stubGameService) FinishRun(ctx context.Context, userID uuid.UUID, streak int, at time.Time) (int, int, error) {
	s.finishCalls = append(s.finishCalls, struct {
		userID uuid.UUID
		streak int
		at     time.Time
	}{userID, streak, at})
	return s.finishBest, s.finishRuns, s.finishErr
}

func mkGameLang(key, label, group string) game.Language {
	return game.Language{Key: key, Label: label, Group: group}
}

// ── /game ────────────────────────────────────────────────────────────────

func TestHandleGame_DormantWhenNil(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1}

	if err := b.handleGame(context.Background(), s); err != nil {
		t.Fatalf("handleGame: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != gameDormantText {
		t.Fatalf("expected the dormant message, got %v", s.sent)
	}
}

func TestHandleGame_SendsMenu(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = &stubGameService{}
	s := &fakeSession{userID: 1}

	if err := b.handleGame(context.Background(), s); err != nil {
		t.Fatalf("handleGame: %v", err)
	}
	if len(s.keyboards) != 1 {
		t.Fatalf("expected one menu message, got %d", len(s.keyboards))
	}
	kb := s.keyboards[0]
	if kb.text != gameMenuText {
		t.Fatalf("expected the menu text, got %q", kb.text)
	}
	if len(kb.rows) != 1 || len(kb.rows[0]) != 1 || kb.rows[0][0].Data != dataGameStartRoulette {
		t.Fatalf("expected a single Language Roulette button, got %+v", kb.rows)
	}
}

// ── game:start:roulette / game:again ────────────────────────────────────

func TestHandleCallback_GameStart_NoContent(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = &stubGameService{catalog: nil}
	s := &fakeSession{userID: 1, messageID: 42, data: dataGameStartRoulette}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].text != gameUnavailableText {
		t.Fatalf("expected the unavailable message edited in place, got %+v", s.editedMsgs)
	}
	// A dead end (no content) still needs a way back to the hub.
	if len(s.editedMsgs[0].rows) != 1 || s.editedMsgs[0].rows[0][0].Data != dataMenuOpen {
		t.Fatalf("expected a single «⬅️ Menu» button on the unavailable message, got %+v", s.editedMsgs[0].rows)
	}
}

func TestHandleCallback_GameStart_NoRoundAvailable(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = &stubGameService{catalog: []game.Language{mkGameLang("spa", "Spanish", "romance")}, roundOK: false}
	s := &fakeSession{userID: 1, messageID: 42, data: dataGameStartRoulette}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].text != gameUnavailableText {
		t.Fatalf("expected the unavailable message when NextRound reports not-ok, got %+v", s.editedMsgs)
	}
}

func TestHandleCallback_GameStart_BuildsFirstRound(t *testing.T) {
	stub := &stubGameService{
		catalog: []game.Language{mkGameLang("spa", "Spanish", "romance"), mkGameLang("por", "Portuguese", "romance")},
		round: game.Round{
			ContentID: uuid.New(),
			Prompt:    "El coche es rojo.",
			Correct:   mkGameLang("spa", "Spanish", "romance"),
			Options: []game.Language{
				mkGameLang("spa", "Spanish", "romance"),
				mkGameLang("por", "Portuguese", "romance"),
				mkGameLang("jpn", "Japanese", "cjk"),
				mkGameLang("kor", "Korean", "cjk"),
			},
		},
		roundOK: true,
	}
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = stub
	s := &fakeSession{userID: 7, messageID: 42, data: dataGameStartRoulette}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single empty ack, got %v", s.responses)
	}
	if len(s.editedMsgs) != 1 {
		t.Fatalf("expected the menu message edited into the first round, got %d edits", len(s.editedMsgs))
	}
	edit := s.editedMsgs[0]
	if edit.text != "🎲 El coche es rojo." {
		t.Fatalf("expected the first round's plain (no streak header) prompt, got %q", edit.text)
	}
	if len(edit.rows) != 4 {
		t.Fatalf("expected 4 option rows, got %d: %+v", len(edit.rows), edit.rows)
	}
	for i, row := range edit.rows {
		if len(row) != 1 || row[0].Data != gameAnswerData(i) {
			t.Fatalf("row %d: expected a single button with data %q, got %+v", i, gameAnswerData(i), row)
		}
	}
	if len(stub.nextRoundCalls) != 1 || stub.nextRoundCalls[0].streak != 0 {
		t.Fatalf("expected NextRound called once with streak=0, got %+v", stub.nextRoundCalls)
	}

	// The run must now be tracked in memory for the following answer tap.
	run, ok := b.getGameRun(7)
	if !ok {
		t.Fatal("expected a game run recorded for telegram user 7")
	}
	if run.streak != 0 || run.round.ContentID != stub.round.ContentID {
		t.Fatalf("unexpected recorded run: %+v", run)
	}
	if !run.used[stub.round.ContentID] {
		t.Fatalf("expected the first round's content id marked used")
	}
}

// ── game:ans:<idx> ───────────────────────────────────────────────────────

func setupGameRun(b *Bot, telegramID int64, streak int, options []game.Language, correct game.Language) {
	b.setGameRun(telegramID, &gameRun{
		catalog: options,
		used:    map[uuid.UUID]bool{},
		streak:  streak,
		round:   game.Round{ContentID: uuid.New(), Prompt: "Test prompt.", Correct: correct, Options: options},
	})
}

func TestHandleGameAnswer_NoActiveRun_StaleToast(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = &stubGameService{}
	s := &fakeSession{userID: 1, data: gameAnswerData(0)}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != gameStaleToast {
		t.Fatalf("expected the stale-run toast, got %v", s.responses)
	}
}

func TestHandleGameAnswer_OutOfRangeIndex_InertAck(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = &stubGameService{}
	options := []game.Language{mkGameLang("spa", "Spanish", "romance"), mkGameLang("por", "Portuguese", "romance")}
	setupGameRun(b, 1, 0, options, options[0])

	s := &fakeSession{userID: 1, data: gameAnswerData(9)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack for an out-of-range index, got %v", s.responses)
	}
}

func TestHandleGameAnswer_Correct_AdvancesRound(t *testing.T) {
	options := []game.Language{mkGameLang("spa", "Spanish", "romance"), mkGameLang("por", "Portuguese", "romance")}
	next := game.Round{
		ContentID: uuid.New(),
		Prompt:    "Yo tengo un perro.",
		Correct:   options[0],
		Options:   options,
	}
	stub := &stubGameService{round: next, roundOK: true}
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = stub
	setupGameRun(b, 1, 2, options, options[0]) // streak 2, correct = spa (index 0)

	s := &fakeSession{userID: 1, messageID: 55, data: gameAnswerData(0)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != correctToast {
		t.Fatalf("expected the correct-answer toast, got %v", s.responses)
	}
	if len(stub.nextRoundCalls) != 1 || stub.nextRoundCalls[0].streak != 3 {
		t.Fatalf("expected NextRound called with the advanced streak (3), got %+v", stub.nextRoundCalls)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 55 {
		t.Fatalf("expected the round message edited in place, got %+v", s.editedMsgs)
	}
	if s.editedMsgs[0].text != "🔥 Streak: 3\n\n🎲 Yo tengo un perro." {
		t.Fatalf("unexpected next-round text: %q", s.editedMsgs[0].text)
	}

	run, ok := b.getGameRun(1)
	if !ok || run.streak != 3 {
		t.Fatalf("expected the run's streak updated to 3, got ok=%v run=%+v", ok, run)
	}
	if len(stub.finishCalls) != 0 {
		t.Fatalf("a correct answer must not finish the run, got %+v", stub.finishCalls)
	}
}

func TestHandleGameAnswer_Wrong_EndsRunAndPersistsStats(t *testing.T) {
	options := []game.Language{mkGameLang("spa", "Spanish", "romance"), mkGameLang("por", "Portuguese", "romance")}
	stub := &stubGameService{finishBest: 7, finishRuns: 4}
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = stub
	setupGameRun(b, 1, 5, options, options[0]) // correct = spa (index 0); tap por (index 1) = wrong

	s := &fakeSession{userID: 1, messageID: 55, data: gameAnswerData(1)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != wrongToast {
		t.Fatalf("expected the wrong-answer toast, got %v", s.responses)
	}
	if len(stub.finishCalls) != 1 || stub.finishCalls[0].streak != 5 {
		t.Fatalf("expected FinishRun called once with the run's streak (5), got %+v", stub.finishCalls)
	}
	if len(s.editedMsgs) != 1 {
		t.Fatalf("expected the closer edited in place, got %d edits", len(s.editedMsgs))
	}
	closer := s.editedMsgs[0].text
	for _, want := range []string{"Wrong — it was Spanish", "Final streak: 5", "Personal best: 7", "Runs played: 4"} {
		if !strings.Contains(closer, want) {
			t.Fatalf("expected closer to contain %q; got:\n%s", want, closer)
		}
	}
	// The two closer controls on their own row, plus a trailing «⬅️ Menu»
	// row back to the hub (hub-and-spoke rule: the game-over screen is
	// /game's own terminal state).
	rows := s.editedMsgs[0].rows
	if len(rows) != 2 || len(rows[0]) != 2 {
		t.Fatalf("expected the two closer controls plus a Menu row, got %+v", rows)
	}
	if rows[0][0].Data != dataGameAgain || rows[0][1].Data != dataGameDone {
		t.Fatalf("expected Play-again then Done, got %+v", rows[0])
	}
	if rows[1][0].Data != dataMenuOpen {
		t.Fatalf("expected the trailing row to be «⬅️ Menu», got %+v", rows[1])
	}

	// The run must be cleared once it's over.
	if _, ok := b.getGameRun(1); ok {
		t.Fatalf("expected the run state cleared after it ended")
	}
}

func TestHandleGameAnswer_Wrong_NoTipWhenNoneMatches(t *testing.T) {
	// "por" (Portuguese) has curated tells, but this sentence contains none
	// of them — the closer must omit the tip line entirely.
	options := []game.Language{mkGameLang("por", "Portuguese", "romance"), mkGameLang("spa", "Spanish", "romance")}
	stub := &stubGameService{}
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = stub
	b.setGameRun(1, &gameRun{
		catalog: options,
		used:    map[uuid.UUID]bool{},
		streak:  0,
		round:   game.Round{ContentID: uuid.New(), Prompt: "Falta pouco.", Correct: options[0], Options: options},
	})

	s := &fakeSession{userID: 1, messageID: 55, data: gameAnswerData(1)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if strings.Contains(s.editedMsgs[0].text, "💡") {
		t.Fatalf("expected no tip line for a sentence with no matching tell, got:\n%s", s.editedMsgs[0].text)
	}
}

// ── game:done ────────────────────────────────────────────────────────────

// TestHandleGameDone_DropsKeyboard covers game:done: the Play-again/Done
// controls are dropped, but a one-button «⬅️ Menu» keyboard replaces them
// rather than clearing to nil (hub-and-spoke rule — the summary must never
// become a total dead end).
func TestHandleGameDone_DropsKeyboard(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = &stubGameService{}
	s := &fakeSession{userID: 1, messageID: 55, data: dataGameDone}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.edits) != 1 || s.edits[0].messageID != 55 {
		t.Fatalf("expected the keyboard replaced on message 55, got %+v", s.edits)
	}
	if len(s.edits[0].rows) != 1 || s.edits[0].rows[0][0].Data != dataMenuOpen {
		t.Fatalf("expected a single «⬅️ Menu» button left in place, got %+v", s.edits[0].rows)
	}
	if len(s.responses) != 1 || s.responses[0] != "👋" {
		t.Fatalf("expected the wave-goodbye ack, got %v", s.responses)
	}
}

// ── nil GameService / unknown payloads ────────────────────────────────────

func TestHandleCallback_Game_NilServiceIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	for _, data := range []string{dataGameStartRoulette, dataGameAgain, dataGameDone, gameAnswerData(0)} {
		s := &fakeSession{userID: 1, data: data}
		if err := b.handleCallback(context.Background(), s); err != nil {
			t.Fatalf("handleCallback(%q): %v", data, err)
		}
		if len(s.responses) != 1 || s.responses[0] != "" {
			t.Fatalf("data %q: expected a single inert ack, got %v", data, s.responses)
		}
	}
}

func TestHandleGameCallback_UnknownPayloadIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.game = &stubGameService{}
	s := &fakeSession{userID: 1, data: "game:bogus"}

	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack for an unrecognized game: payload, got %v", s.responses)
	}
}

// ── pure parsing / rendering ─────────────────────────────────────────────

func TestParseGameAnswer_RoundTrip(t *testing.T) {
	for _, idx := range []int{0, 1, 2, 3, 10} {
		data := gameAnswerData(idx)
		got, ok := parseGameAnswer(data)
		if !ok || got != idx {
			t.Fatalf("parseGameAnswer(%q) = (%d, %v), want (%d, true)", data, got, ok, idx)
		}
	}
}

func TestParseGameAnswer_Invalid(t *testing.T) {
	for _, data := range []string{"", "game:ans:", "game:ans:x", "game:start:roulette", "ans:0"} {
		if _, ok := parseGameAnswer(data); ok {
			t.Fatalf("parseGameAnswer(%q) unexpectedly succeeded", data)
		}
	}
}

func TestRenderGameRound_FirstRoundHasNoStreakHeader(t *testing.T) {
	round := game.Round{Prompt: "Hola."}
	if got := renderGameRound(round, 0); got != "🎲 Hola." {
		t.Fatalf("renderGameRound(streak=0) = %q", got)
	}
}

func TestRenderGameRound_LaterRoundHasStreakHeader(t *testing.T) {
	round := game.Round{Prompt: "Hola."}
	got := renderGameRound(round, 4)
	if !strings.Contains(got, "Streak: 4") || !strings.Contains(got, "Hola.") {
		t.Fatalf("renderGameRound(streak=4) = %q", got)
	}
}

func TestRenderGameCloser_CorpusExhausted_NoWrongLanguage(t *testing.T) {
	got := renderGameCloser(6, 6, 2, game.Language{}, "")
	if strings.Contains(got, "Wrong") {
		t.Fatalf("expected no 'Wrong' phrasing when the run ended from an exhausted corpus, got:\n%s", got)
	}
	if !strings.Contains(got, "Out of fresh sentences") {
		t.Fatalf("expected the exhausted-corpus phrasing, got:\n%s", got)
	}
}
