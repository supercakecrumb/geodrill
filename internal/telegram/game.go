// game.go implements /game: the game zone's entry command and its one game
// today, Language Roulette (vibe/design-game-zone.md). Games are ephemeral
// sessions, not scheduled study — only aggregate stats persist
// (GameService.FinishRun) — so round-by-round state (streak, the catalog,
// used content ids, and the currently-open round) lives in memory per
// telegram user id, the same pattern bot.go's practiceStart already uses
// for /practice sessions.
package telegram

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/game"
)

// game: callback payloads (callback namespace "game:", per the design doc).
const (
	dataGameStartRoulette = "game:start:roulette"
	dataGameAgain         = "game:again"
	dataGameDone          = "game:done"
	dataGameAnswerPrefix  = "game:ans:"
)

// gameDormantText/gameMenuText/gameUnavailableText/gameStaleToast are the
// game zone's nil-safe / no-content / stale-tap copy, mirroring the other
// optional services' "🚧 coming soon" convention.
const (
	gameDormantText     = "🚧 /game is coming soon."
	gameMenuText        = "🎮 Game zone — quick, ungraded rounds that don't touch your review schedule.\n\nToday's game:"
	gameUnavailableText = "No sentences are ingested yet, so there's nothing to guess right now. " +
		"Try again once content has been ingested."
	gameStaleToast = "⏳ that run has expired — start a new one"
)

// gameRun is one in-progress Language Roulette run's in-memory state
// (design doc "Persistence": "Run state ... is in-memory per chat").
type gameRun struct {
	catalog []game.Language
	used    map[uuid.UUID]bool
	streak  int
	round   game.Round // the currently-open, ungraded round
}

// ── production wiring ────────────────────────────────────────────────────

// gameService adapts internal/game.Engine to GameService: it loads the
// catalog via the injected store, holds the engine's own seeded rng
// (guarded by a mutex, the same pattern internal/study.Service uses for its
// shuffle rng — math/rand.Rand is not concurrency-safe), and flattens
// Engine.FinishRun's storage.GameStats return into the plain
// bestStreak/runs pair this package renders (this package otherwise never
// imports internal/storage's types directly).
type gameService struct {
	engine *game.Engine
	store  game.CatalogStore

	mu  sync.Mutex
	rng *rand.Rand
}

// NewGameService builds the production GameService: engine runs rounds and
// persists stats, store's topic-tree read methods load the catalog
// (*storage.Store satisfies game.CatalogStore directly), and seed
// deterministically seeds the round-shuffle rng (mirrors
// internal/study.New's own seed parameter).
func NewGameService(engine *game.Engine, store game.CatalogStore, seed int64) GameService {
	return &gameService{engine: engine, store: store, rng: rand.New(rand.NewSource(seed))}
}

func (g *gameService) Catalog(ctx context.Context) ([]game.Language, error) {
	return game.LoadCatalog(ctx, g.store)
}

func (g *gameService) NextRound(ctx context.Context, userID uuid.UUID, catalog []game.Language, streak int, used map[uuid.UUID]bool) (game.Round, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.engine.NextRound(ctx, g.rng, userID, catalog, streak, used)
}

func (g *gameService) FinishRun(ctx context.Context, userID uuid.UUID, streak int, at time.Time) (bestStreak, runs int, err error) {
	stats, err := g.engine.FinishRun(ctx, userID, streak, at)
	if err != nil {
		return 0, 0, err
	}
	return stats.BestStreak, stats.Runs, nil
}

// ── /game ────────────────────────────────────────────────────────────────

func (b *Bot) handleGame(ctx context.Context, s Session) error {
	if b.game == nil {
		return s.Send(gameDormantText)
	}
	_, err := s.SendKeyboard(gameMenuText, gameMenuRows())
	return err
}

// gameMenuRows is the /game menu: one button per available game — today
// just Language Roulette (design doc "The game zone": the future
// city-listing game will slot into the same menu).
func gameMenuRows() [][]Btn {
	return [][]Btn{{{Label: "🎲 Language Roulette", Data: dataGameStartRoulette}}}
}

// ── callbacks ────────────────────────────────────────────────────────────

// handleGameCallback routes every "game:" callback payload.
func (b *Bot) handleGameCallback(ctx context.Context, s Session, data string) error {
	if b.game == nil {
		return s.Respond("")
	}
	switch {
	case data == dataGameStartRoulette, data == dataGameAgain:
		return b.startGameRun(ctx, s)
	case data == dataGameDone:
		return b.handleGameDone(ctx, s)
	case strings.HasPrefix(data, dataGameAnswerPrefix):
		return b.handleGameAnswer(ctx, s, data)
	default:
		return s.Respond("")
	}
}

// startGameRun begins a fresh Language Roulette run — from the menu's
// "🎲 Language Roulette" button or the closer's "▶️ Play again" — by
// loading the catalog, building the first round, and editing the tapped
// message into it in place.
func (b *Bot) startGameRun(ctx context.Context, s Session) error {
	if err := s.Respond(""); err != nil {
		return err
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	catalog, err := b.game.Catalog(ctx)
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return s.EditMessage(s.MessageID(), gameUnavailableText, nil)
	}

	used := map[uuid.UUID]bool{}
	round, ok, err := b.game.NextRound(ctx, user.ID, catalog, 0, used)
	if err != nil {
		return err
	}
	if !ok {
		return s.EditMessage(s.MessageID(), gameUnavailableText, nil)
	}
	used[round.ContentID] = true
	b.setGameRun(s.UserID(), &gameRun{catalog: catalog, used: used, streak: 0, round: round})
	return s.EditMessage(s.MessageID(), renderGameRound(round, 0), gameRoundRows(round))
}

// handleGameAnswer grades one tapped option against the caller's open
// round: correct advances the streak and edits in place to the next round
// (no repeat sentences within a run); wrong ends the run, persists the
// aggregate, and edits in place to the closer (final streak, personal
// best, runs played, and the missed language's recognition tip when one
// exists — design doc "Language Roulette").
func (b *Bot) handleGameAnswer(ctx context.Context, s Session, data string) error {
	run, found := b.getGameRun(s.UserID())
	if !found {
		return s.Respond(gameStaleToast)
	}
	idx, ok := parseGameAnswer(data)
	if !ok || idx < 0 || idx >= len(run.round.Options) {
		return s.Respond("")
	}
	chosen := run.round.Options[idx]

	if chosen.Key == run.round.Correct.Key {
		if err := s.Respond(correctToast); err != nil {
			return err
		}
		return b.advanceGameRun(ctx, s, run)
	}

	if err := s.Respond(wrongToast); err != nil {
		return err
	}
	return b.endGameRun(ctx, s, run)
}

// advanceGameRun grows run's streak by one, builds the next round, and
// edits the message in place — or ends the run gracefully if the corpus
// has no fresh sentence left to show (an edge case the design doc doesn't
// spell out, but a dead end must still close cleanly instead of erroring).
func (b *Bot) advanceGameRun(ctx context.Context, s Session, run *gameRun) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	streak := run.streak + 1
	round, ok, err := b.game.NextRound(ctx, user.ID, run.catalog, streak, run.used)
	if err != nil {
		return err
	}
	if !ok {
		return b.endGameRun(ctx, s, &gameRun{catalog: run.catalog, used: run.used, streak: streak})
	}
	run.used[round.ContentID] = true
	run.streak = streak
	run.round = round
	b.setGameRun(s.UserID(), run)
	return s.EditMessage(s.MessageID(), renderGameRound(round, streak), gameRoundRows(round))
}

// endGameRun persists the run's final streak (best_streak only ever
// grows, runs increments) and edits the message in place to the closer.
// run.round is the zero value when the run ended because the corpus ran
// dry rather than a wrong guess — no missed language, so no tip line.
func (b *Bot) endGameRun(ctx context.Context, s Session, run *gameRun) error {
	b.clearGameRun(s.UserID())
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	best, runs, err := b.game.FinishRun(ctx, user.ID, run.streak, b.now())
	if err != nil {
		return err
	}

	var missed game.Language
	var tip string
	if run.round.Correct.Key != "" {
		missed = run.round.Correct
		tip = game.TipFor(missed.Key, missed.Label, run.round.Prompt)
	}
	return s.EditMessage(s.MessageID(), renderGameCloser(run.streak, best, runs, missed, tip), gameCloserRows())
}

// handleGameDone ends the game-zone interaction: drops the closer's
// keyboard in place, leaving the summary as a plain message.
func (b *Bot) handleGameDone(ctx context.Context, s Session) error {
	if err := s.EditKeyboard(s.MessageID(), nil); err != nil {
		b.logger.Warn("telegram: edit game closer keyboard", "error", err)
	}
	return s.Respond("👋")
}

// ── in-memory run state ─────────────────────────────────────────────────

func (b *Bot) getGameRun(telegramID int64) (*gameRun, bool) {
	b.gameMu.Lock()
	defer b.gameMu.Unlock()
	run, ok := b.gameRuns[telegramID]
	return run, ok
}

func (b *Bot) setGameRun(telegramID int64, run *gameRun) {
	b.gameMu.Lock()
	defer b.gameMu.Unlock()
	if b.gameRuns == nil {
		b.gameRuns = make(map[int64]*gameRun)
	}
	b.gameRuns[telegramID] = run
}

func (b *Bot) clearGameRun(telegramID int64) {
	b.gameMu.Lock()
	defer b.gameMu.Unlock()
	delete(b.gameRuns, telegramID)
}

// ── pure rendering / parsing ─────────────────────────────────────────────

// parseGameAnswer parses a "game:ans:<idx>" payload. ok=false for anything
// malformed.
func parseGameAnswer(data string) (int, bool) {
	rest, hasPrefix := strings.CutPrefix(data, dataGameAnswerPrefix)
	if !hasPrefix {
		return 0, false
	}
	idx, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// gameAnswerData builds one option button's payload.
func gameAnswerData(idx int) string {
	return dataGameAnswerPrefix + strconv.Itoa(idx)
}

// renderGameRound renders one round's prompt, with a streak header once a
// run is under way (streak == 0 is the first round).
func renderGameRound(round game.Round, streak int) string {
	if streak == 0 {
		return "🎲 " + round.Prompt
	}
	return fmt.Sprintf("🔥 Streak: %d\n\n🎲 %s", streak, round.Prompt)
}

// gameRoundRows renders one round's language options as buttons, one per
// row (language names can run long, so this avoids squeezing several onto
// one row).
func gameRoundRows(round game.Round) [][]Btn {
	rows := make([][]Btn, len(round.Options))
	for i, opt := range round.Options {
		rows[i] = []Btn{{Label: opt.Label, Data: gameAnswerData(i)}}
	}
	return rows
}

// renderGameCloser renders the run-over summary (design doc "Language
// Roulette": final streak, personal best, runs played, and the missed
// language's tip when one exists). missed is the zero Language when the
// run ended without a wrong guess (the corpus ran dry) — no tip line then.
func renderGameCloser(streak, best, runs int, missed game.Language, tip string) string {
	var b strings.Builder
	if missed.Key != "" {
		fmt.Fprintf(&b, "❌ Wrong — it was %s.\n\n", missed.Label)
	} else {
		b.WriteString("🎉 Out of fresh sentences for now!\n\n")
	}
	fmt.Fprintf(&b, "🔥 Final streak: %d\n🏆 Personal best: %d\n🎮 Runs played: %d\n", streak, best, runs)
	if tip != "" {
		fmt.Fprintf(&b, "\n💡 %s\n", tip)
	}
	return b.String()
}

// gameCloserRows renders the closer's two controls (design doc: "▶️ Play
// again" / "🏁 Done").
func gameCloserRows() [][]Btn {
	return [][]Btn{{
		{Label: "▶️ Play again", Data: dataGameAgain},
		{Label: "🏁 Done", Data: dataGameDone},
	}}
}
