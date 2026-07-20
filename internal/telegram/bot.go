// Package telegram is geodrill's Telegram bot layer (architecture contract
// §5). It renders internal/study's mode-aware exercise/intro/topic/stats
// output over gopkg.in/telebot.v4, and stays deliberately thin: all
// scheduling, generation, and grading logic lives in internal/study; this
// package only talks to Telegram and internal/storage's user bookkeeping.
//
// Handler logic (handlers.go) is written against the small Session
// interface (session.go) rather than telebot.Context directly, so it can be
// unit-tested without a bot token, a database, or the network. bot.go wires
// a real telebot.Bot and adapts its Context to Session via tbSession.
package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"
	telebot "gopkg.in/telebot.v4"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/suggest"
)

// reminderCheckInterval is how often the reminder goroutine wakes up to
// check whether any user's chosen hour (or a follow-up window) has arrived.
const reminderCheckInterval = 30 * time.Minute

// maxFollowUps caps how many follow-up nudges are sent after the first daily
// reminder ("repeat, capped"). 2 → at most 3 messages in a local day.
const maxFollowUps = 2

// reminderKind is the action the loop decides to take for one user on a tick.
type reminderKind int

const (
	reminderNone reminderKind = iota
	reminderFirst
	reminderFollowUp
)

// reminderState tracks one user's reminder progress for a single local day. It
// lives in memory (like the old remindedOn map): a mid-day restart resets it,
// at worst causing one extra or one skipped follow-up that day.
type reminderState struct {
	day         string    // user-local yyyy-mm-dd this state describes
	firstSentAt time.Time // when the first reminder went out today
	lastSentAt  time.Time // when the most recent reminder (first or follow-up) went out
	followUps   int       // follow-ups sent so far today
}

// Config configures a Bot. StudyService, TopicService, and IntroCapStore
// are OPTIONAL and nil-safe: every feature they gate degrades to a
// "🚧 coming soon" reply until wired. Trainer is REQUIRED
// (deliverable 5's cutover): /train, /stats, and the
// reminder loop's due-review count all call it unconditionally now — the
// legacy trainer fallback they used to have is gone, and the free-text
// OnText handler is only registered when it's non-nil.
type Config struct {
	Token  string
	Store  *storage.Store
	Logger *slog.Logger
	Now    func() time.Time

	// StudyService powers /study, /introduce, and the reminder loop's
	// introduction nudge (architecture §5.1, §5.3).
	StudyService StudyService
	// TopicService powers the /topics tree browser (architecture §5.2).
	TopicService TopicService
	// Trainer powers the mode-aware exercise path (architecture §1.6):
	// /train, /stats, and the reminder loop's due count all
	// require it now (see this type's doc comment); its presence also
	// decides whether the free-text OnText handler is registered at all.
	Trainer Trainer
	// IntroCapStore powers the /settings daily intro-cap row.
	IntroCapStore IntroCapStore
	// TierRecomputer rebuilds the gating cache after the /settings
	// GeoGuessr-only toggle flips users.gg_only (nil-safe; see the interface's
	// doc in services.go).
	TierRecomputer TierRecomputer
	// Game powers /game and its game zone (vibe/design-game-zone.md):
	// today, Language Roulette.
	Game GameService
	// Suggest powers inline-mode autocomplete answers
	// (vibe/spike-autocomplete-inline.md): a nil Suggest keeps
	// telebot.OnQuery unregistered entirely, the same nil-safe convention
	// every other optional Config field follows.
	Suggest Suggester
	// Feedback files /feedback notes into snagbox ([[snagbox-integration]]).
	// Nil-safe: with no reporter wired (SNAGBOX_URL / SNAGBOX_INGEST_TOKEN
	// unset), /feedback replies that feedback isn't available rather than
	// failing — the same convention every optional field above follows.
	Feedback FeedbackReporter
}

// mediaStore is the narrow slice of *storage.Store SendPhoto needs to cache
// a photo's Telegram file_id after first upload (architecture §2.8 decision
// 6): look up media_files by local_path, and write back the file_id
// Telegram returns from the first send so later sends reuse it instead of
// re-uploading. Declared locally (the topics-package convention) so
// tbSession depends on an interface, not *storage.Store directly —
// *storage.Store satisfies it already (internal/storage/media.go).
type mediaStore interface {
	GetMediaByLocalPath(ctx context.Context, localPath string) (storage.MediaFile, bool, error)
	SetMediaTelegramFileID(ctx context.Context, mediaID uuid.UUID, fileID string) error
}

// openExerciseStore is the narrow store surface handleQuery needs to look up
// a user's currently open ModeText exercise, so inline-query autocomplete
// suggestions can be scoped to its answer domain (suggest.Domain) instead of
// always merging countries and capitals regardless of which direction is
// open (kanban card "Autocomplete must be scoped to the question's answer
// domain"). Declared locally the same way mediaStore is, just above, so
// *storage.Store satisfies it directly with no adapter —
// GetOpenExerciseByMode (internal/storage/exercises_reviews.go) already has
// this exact shape.
type openExerciseStore interface {
	GetOpenExerciseByMode(ctx context.Context, userID uuid.UUID, mode int16) (storage.Exercise, bool, error)
}

// Bot wires telebot to geodrill's study/storage layers.
type Bot struct {
	tb     *telebot.Bot
	store  userStore
	logger *slog.Logger
	now    func() time.Time

	// username is the bot's own @handle (telebot.Bot.Me.Username, no leading
	// "@"), set once in New() after tb is constructed. handleText uses it to
	// strip a leading "@<username>" mention that Telegram's inline
	// autocomplete inserts into a tapped suggestion (train.go's
	// stripBotMention) before grading the reply text.
	username string

	// media powers SendPhoto's file_id cache (mediaStore's doc comment). Set
	// from Config.Store, which already satisfies mediaStore directly — the
	// same "nil is fine, caching just falls back to always sending from
	// disk" tolerance every other optional Bot field follows, in case a
	// caller ever constructs a Bot without a Store (existing telegram
	// package tests build fakeSession/Session directly and never exercise
	// tbSession at all, so they're unaffected either way).
	media mediaStore

	// study is nil until a later wave wires Config.StudyService — every
	// call site checks it before use (see study.go).
	study StudyService
	// topics is nil until a later wave wires Config.TopicService — every
	// call site checks it before use (see topics_ui.go).
	topics TopicService
	// trainer is required (Config.Trainer, see this package's Config
	// doc comment) — /train, /stats, and the reminder loop's
	// due count call it unconditionally (train.go); only OnText's
	// registration still checks it for nil (bot.go's New).
	trainer  Trainer
	introCap IntroCapStore
	// tiers rebuilds the gating cache on the /settings GeoGuessr-only toggle
	// (TierRecomputer's doc). Nil-safe: the toggle still persists gg_only
	// without it.
	tiers TierRecomputer
	// game is nil until Config.Game is wired — every call site checks it
	// before use (see game.go), the same nil-safe convention study/topics
	// follow.
	game GameService
	// suggest is nil until Config.Suggest is wired — handleQuery isn't even
	// registered in New() until it is, mirroring Trainer's OnText gate.
	suggest Suggester
	// exerciseStore looks up a user's open ModeText exercise for
	// handleQuery's domain scoping (openExerciseStore's doc). Set from
	// Config.Store, the same *storage.Store already satisfying userStore
	// and mediaStore above — nil-safe the same way those are.
	exerciseStore openExerciseStore
	// feedback files /feedback notes into snagbox — nil until Config.Feedback
	// is wired, at which point handleFeedback files instead of replying that
	// feedback is unavailable (see feedback.go).
	feedback FeedbackReporter

	remindedMu  sync.Mutex
	remindState map[uuid.UUID]reminderState // userID -> today's reminder progress

	gameMu   sync.Mutex
	gameRuns map[int64]*gameRun // telegram user id -> current /game run state (design doc "Persistence": run state is in-memory per chat)
}

// New builds a Bot: constructs the underlying telebot.Bot with a 10s
// long-poller and registers all command + callback handlers. It does not
// start polling — call (*Bot).Start for that.
func New(cfg Config) (*Bot, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	tb, err := telebot.NewBot(telebot.Settings{
		Token:  cfg.Token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("telegram: create bot: %w", err)
	}

	b := &Bot{
		tb:            tb,
		store:         cfg.Store,
		media:         cfg.Store,
		logger:        logger,
		now:           now,
		username:      tb.Me.Username,
		study:         cfg.StudyService,
		topics:        cfg.TopicService,
		trainer:       cfg.Trainer,
		introCap:      cfg.IntroCapStore,
		tiers:         cfg.TierRecomputer,
		game:          cfg.Game,
		suggest:       cfg.Suggest,
		exerciseStore: cfg.Store,
		feedback:      cfg.Feedback,
		remindState:   make(map[uuid.UUID]reminderState),
		gameRuns:      make(map[int64]*gameRun),
	}

	tb.Handle("/start", b.wrap(b.handleStart))
	tb.Handle("/menu", b.wrap(b.handleMenu))
	tb.Handle("/train", b.wrap(b.handleTrain))
	tb.Handle("/decks", b.wrap(b.handleDecks))
	tb.Handle("/settings", b.wrap(b.handleSettings))
	tb.Handle("/stats", b.wrap(b.handleStats))
	tb.Handle("/help", b.wrap(b.handleHelp))
	tb.Handle("/study", b.wrap(b.handleStudy))
	tb.Handle("/introduce", b.wrap(b.handleStudy)) // alias that fetches more intro cards on demand (decision 2)
	tb.Handle("/topics", b.wrap(b.handleTopics))
	tb.Handle("/game", b.wrap(b.handleGame))
	// /feedback is registered unconditionally (like /study, /topics, /game):
	// handleFeedback is nil-safe and replies "not available" until a reporter
	// is wired, so it never intercepts anything it can't serve.
	tb.Handle("/feedback", b.wrap(b.handleFeedback))
	tb.Handle(telebot.OnCallback, b.wrap(b.handleCallback))
	// OnText (free-typed answers) is only registered when Trainer is
	// wired: the legacy bot never listened for plain text at all, and
	// nil-safety means that stays true until a real Trainer arrives —
	// registering it unconditionally would start intercepting every plain
	// message the moment this field exists, even with Config.Trainer nil.
	if cfg.Trainer != nil {
		tb.Handle(telebot.OnText, b.wrap(b.handleText))
	}
	// OnQuery (inline-mode autocomplete, vibe/spike-autocomplete-inline.md)
	// is only registered when a Suggester is wired, the same nil-safe gate
	// as OnText above. It's registered directly against telebot.Context
	// (not via b.wrap/Session): telebot.Query's shape has no
	// callback/message concept for Session to adapt (see queryContext's
	// doc, below).
	if cfg.Suggest != nil {
		tb.Handle(telebot.OnQuery, func(c telebot.Context) error {
			return b.handleQuery(c)
		})
	}

	// Populate the in-app "/" command menu (best-effort; a failure here must
	// not prevent the bot from starting).
	if err := tb.SetCommands(botCommands); err != nil {
		logger.Warn("telegram: set commands menu", "error", err)
	}

	return b, nil
}

// botCommands is the "/" autocomplete menu shown in Telegram clients.
var botCommands = []telebot.Command{
	{Text: "menu", Description: "Main menu — jump to any section"},
	{Text: "train", Description: "Next due exercise"},
	{Text: "study", Description: "Introduce new items"},
	{Text: "topics", Description: "Browse topics, tiers & progress"},
	{Text: "decks", Description: "Now points to /topics"},
	{Text: "game", Description: "Play a quick game (no scheduling)"},
	{Text: "settings", Description: "Daily cap, reminders, button style"},
	{Text: "stats", Description: "Your progress and mix-ups"},
	{Text: "feedback", Description: "Report a bug or send feedback"},
	{Text: "help", Description: "How geodrill works"},
	{Text: "start", Description: "Register and pick decks"},
}

// wrap adapts a Session-based handler into a telebot.HandlerFunc: it never
// lets an error or panic escape to the poller, logging both instead.
func (b *Bot) wrap(h func(context.Context, Session) error) telebot.HandlerFunc {
	return func(c telebot.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error("telegram: handler panic", "recover", r)
			}
		}()

		s := &tbSession{bot: b.tb, ctx: c, media: b.media, logger: b.logger}
		if herr := h(context.Background(), s); herr != nil {
			b.logger.Error("telegram: handler error", "error", herr)
		}
		return nil
	}
}

// Start starts the reminder loop and the long-poller, blocking until ctx is
// canceled, at which point the poller is stopped and Start returns nil.
func (b *Bot) Start(ctx context.Context) error {
	go b.remindLoop(ctx)

	done := make(chan struct{})
	go func() {
		b.tb.Start()
		close(done)
	}()

	select {
	case <-ctx.Done():
		b.tb.Stop()
		<-done
	case <-done:
	}
	return nil
}

// ── reminders ────────────────────────────────────────────────────────────

// remindLoop periodically checks every user opted into reminders and nudges
// them if they have anything due: once per user-local day at their chosen
// hour, plus capped follow-ups when they haven't engaged within the window
// (see decideReminder).
func (b *Bot) remindLoop(ctx context.Context) {
	ticker := time.NewTicker(reminderCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.sendReminders(ctx)
		}
	}
}

func (b *Bot) sendReminders(ctx context.Context) {
	users, err := b.store.UsersWithReminders(ctx)
	if err != nil {
		b.logger.Error("telegram: reminders: list users", "error", err)
		return
	}

	now := b.now()
	for _, u := range users {
		local := now.In(locationFor(u))
		day := local.Format("2006-01-02")
		st := b.getReminderState(u.ID)

		due, err := b.trainer.DueCount(ctx, u.ID)
		if err != nil {
			b.logger.Error("telegram: reminders: due count", "user", u.ID, "error", err)
			continue
		}

		// "Available introductions" (architecture §5.3) = min(available,
		// budgetLeft): candidates exist AND today's daily cap isn't already
		// spent. Only queried when StudyService is wired; degrades to 0
		// (due-only reminders, today's behavior) on nil or on error rather
		// than skipping the whole tick over a StudyService hiccup.
		introReady := 0
		if b.study != nil {
			available, budgetLeft, ierr := b.study.IntroSummary(ctx, u.ID)
			if ierr != nil {
				b.logger.Error("telegram: reminders: intro summary", "user", u.ID, "error", ierr)
			} else {
				introReady = min(available, budgetLeft)
			}
		}

		// The engagement check (has the user answered anything since the first
		// reminder?) only matters for the follow-up decision, so only query it
		// once a first reminder has already gone out today.
		reviewedSince := 0
		if st.day == day && !st.firstSentAt.IsZero() {
			reviewedSince, err = b.store.CountReviewsSince(ctx, u.ID, st.firstSentAt)
			if err != nil {
				b.logger.Error("telegram: reminders: reviews since", "user", u.ID, "error", err)
				continue
			}
		}

		switch decideReminder(u, st, day, local.Hour(), now, due, reviewedSince, introReady) {
		case reminderFirst:
			if !b.sendReminderMessage(u, reminderText(due, introReady, false), due, introReady) {
				continue
			}
			b.setReminderState(u.ID, reminderState{day: day, firstSentAt: now, lastSentAt: now})
		case reminderFollowUp:
			if !b.sendReminderMessage(u, reminderText(due, introReady, true), due, introReady) {
				continue
			}
			st.lastSentAt = now
			st.followUps++
			b.setReminderState(u.ID, st)
		}
	}
}

// decideReminder decides whether to send a first reminder, a follow-up, or
// nothing to a user on a tick. It is pure (no I/O) so the timing rules are
// unit-testable: st is the user's current in-memory state, day is the user's
// local date, localHour their local hour, due their due-review count,
// reviewedSince how many reviews they've answered since st.firstSentAt, and
// introReady how many items are ready to introduce today (architecture
// §5.3; always 0 when StudyService is nil — see sendReminders). Follow-up
// suppression (engagement, cap, delay) is unchanged by introReady: it only
// widens the gate that fires the FIRST reminder of the day.
func decideReminder(u storage.User, st reminderState, day string, localHour int, now time.Time, due, reviewedSince, introReady int) reminderKind {
	if due <= 0 && introReady <= 0 {
		return reminderNone
	}
	// No first reminder yet today: send one when we reach the user's chosen hour.
	if st.day != day || st.firstSentAt.IsZero() {
		if u.RemindersEnabled && localHour == u.ReminderHour {
			return reminderFirst
		}
		return reminderNone
	}
	// First already sent today — consider a follow-up.
	if !u.FollowUpEnabled || st.followUps >= maxFollowUps {
		return reminderNone
	}
	if reviewedSince > 0 { // engaged since the first reminder → stop nagging
		return reminderNone
	}
	if now.Before(st.lastSentAt.Add(time.Duration(u.FollowUpDelayMin) * time.Minute)) {
		return reminderNone
	}
	return reminderFollowUp
}

// reminderText renders the reminder body for the given due/introReady
// counts (architecture §5.3): due-only keeps today's exact wording
// (introReady == 0 is the common case while StudyService is unwired),
// intro-only and combined ("N reviews due · M new items to introduce") are
// new. followUp switches every variant to the terser nudge wording.
func reminderText(due, introReady int, followUp bool) string {
	switch {
	case due > 0 && introReady > 0:
		return combinedReminderText(due, introReady, followUp)
	case introReady > 0:
		return introOnlyReminderText(introReady, followUp)
	default:
		return dueOnlyReminderText(due, followUp)
	}
}

func dueOnlyReminderText(due int, followUp bool) string {
	plural := plural(due)
	if followUp {
		return fmt.Sprintf("⏰ Still %d review%s waiting — tap to start.", due, plural)
	}
	return fmt.Sprintf("🔔 You have %d review%s due today.", due, plural)
}

func introOnlyReminderText(introReady int, followUp bool) string {
	plural := plural(introReady)
	if followUp {
		return fmt.Sprintf("⏰ Still %d new item%s ready to introduce — tap to start.", introReady, plural)
	}
	return fmt.Sprintf("✨ %d new item%s ready to introduce.", introReady, plural)
}

func combinedReminderText(due, introReady int, followUp bool) string {
	if followUp {
		return fmt.Sprintf("⏰ Still %d review%s due · %d new item%s to introduce — tap to start.",
			due, plural(due), introReady, plural(introReady))
	}
	return fmt.Sprintf("🔔 %d review%s due · %d new item%s to introduce.",
		due, plural(due), introReady, plural(introReady))
}

// plural returns "s" unless n is exactly 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sendReminderMessage sends a reminder with "▶️ Start reviewing" (due > 0)
// and/or "✨ Introduce new" (introReady > 0) buttons — architecture §5.3's
// combined nudge. It returns false (and logs) on failure so the caller
// skips recording state.
func (b *Bot) sendReminderMessage(u storage.User, text string, due, introReady int) bool {
	markup := buildMarkup(reminderButtonRows(due, introReady))
	if _, err := b.tb.Send(telebot.ChatID(u.TelegramID), text, markup); err != nil {
		b.logger.Error("telegram: reminders: send", "user", u.ID, "error", err)
		return false
	}
	return true
}

// reminderButtonRows lays out the reminder's action buttons on one row: ▶️
// Start reviewing when reviews are due, ✨ Introduce new when items are
// ready to introduce — either, both, or (defensively) neither.
func reminderButtonRows(due, introReady int) [][]Btn {
	var row []Btn
	if due > 0 {
		row = append(row, Btn{Label: "▶️ Start reviewing", Data: dataStartTrain})
	}
	if introReady > 0 {
		row = append(row, Btn{Label: "✨ Introduce new", Data: dataStudyStart})
	}
	if len(row) == 0 {
		return nil
	}
	return [][]Btn{row}
}

func (b *Bot) getReminderState(userID uuid.UUID) reminderState {
	b.remindedMu.Lock()
	defer b.remindedMu.Unlock()
	return b.remindState[userID]
}

func (b *Bot) setReminderState(userID uuid.UUID, st reminderState) {
	b.remindedMu.Lock()
	defer b.remindedMu.Unlock()
	b.remindState[userID] = st
}

// ── telebot.Context adapter ──────────────────────────────────────────────

// tbSession adapts a telebot.Context (plus the *telebot.Bot, needed for the
// send/edit calls that must return the sent message id) to Session.
type tbSession struct {
	bot    *telebot.Bot
	ctx    telebot.Context
	media  mediaStore
	logger *slog.Logger
}

func (s *tbSession) UserID() int64 {
	if u := s.ctx.Sender(); u != nil {
		return u.ID
	}
	return 0
}

func (s *tbSession) Username() string {
	if u := s.ctx.Sender(); u != nil {
		return u.Username
	}
	return ""
}

func (s *tbSession) MessageID() int64 {
	if s.ctx.Callback() == nil {
		return 0
	}
	m := s.ctx.Message()
	if m == nil {
		return 0
	}
	return int64(m.ID)
}

func (s *tbSession) Send(text string) error {
	return s.ctx.Send(text)
}

func (s *tbSession) SendKeyboard(text string, rows [][]Btn) (int64, error) {
	msg, err := s.bot.Send(s.ctx.Chat(), text, buildMarkup(rows))
	if err != nil {
		return 0, err
	}
	return int64(msg.ID), nil
}

func (s *tbSession) EditKeyboard(messageID int64, rows [][]Btn) error {
	var chatID int64
	if chat := s.ctx.Chat(); chat != nil {
		chatID = chat.ID
	}
	sm := telebot.StoredMessage{
		MessageID: strconv.FormatInt(messageID, 10),
		ChatID:    chatID,
	}
	_, err := s.bot.EditReplyMarkup(sm, buildMarkup(rows))
	return err
}

func (s *tbSession) EditMessage(messageID int64, text string, rows [][]Btn) error {
	var chatID int64
	if chat := s.ctx.Chat(); chat != nil {
		chatID = chat.ID
	}
	sm := telebot.StoredMessage{
		MessageID: strconv.FormatInt(messageID, 10),
		ChatID:    chatID,
	}
	// telebot dispatches to editMessageText, replacing text and reply markup
	// in one API call. ModeHTML is passed explicitly since text is HTML
	// (see Session.EditMessage) — the caller is responsible for escaping.
	_, err := s.bot.Edit(sm, text, buildMarkup(rows), telebot.ModeHTML)
	return err
}

// SendPhoto sends path as a photo message (architecture §5.1 decision 6:
// media introduction/exercise cards are photo messages from birth). caption
// is sent with ModeHTML — the same parse mode EditCaption edits it with
// later, so a card never changes rendering between its initial send and an
// in-place edit (see the Session.SendPhoto doc comment).
//
// File_id caching (architecture §2.8 decision 6): the FIRST send for a
// given local path uploads from disk and caches the Telegram file_id
// Telegram returns into media_files; every SUBSEQUENT send for the same
// path reuses that cached file_id (telebot.File{FileID: ...}) instead of
// re-uploading the file from disk. A nil media store, a lookup error, or a
// path with no media_files row (asset not ingested yet) all fall back to
// the original always-send-from-disk behavior — caching is a pure
// optimization, never a correctness requirement, so any failure along this
// path is logged (when a logger is available) rather than surfaced to the
// caller.
func (s *tbSession) SendPhoto(path, caption string, rows [][]Btn) (int64, error) {
	file := telebot.FromDisk(path)
	var mediaID uuid.UUID
	cached := false

	if s.media != nil {
		if mf, ok, err := s.media.GetMediaByLocalPath(context.Background(), path); err != nil {
			if s.logger != nil {
				s.logger.Warn("telegram: media file_id lookup failed", "path", path, "error", err)
			}
		} else if ok {
			mediaID = mf.ID
			if mf.TelegramFileID != "" {
				file = telebot.File{FileID: mf.TelegramFileID}
				cached = true
			}
		}
	}

	photo := &telebot.Photo{File: file, Caption: caption}
	msg, err := s.bot.Send(s.ctx.Chat(), photo, buildMarkup(rows), telebot.ModeHTML)
	if err != nil {
		return 0, err
	}

	if !cached && mediaID != uuid.Nil && msg.Photo != nil && msg.Photo.FileID != "" {
		if serr := s.media.SetMediaTelegramFileID(context.Background(), mediaID, msg.Photo.FileID); serr != nil && s.logger != nil {
			s.logger.Warn("telegram: cache media file_id failed", "path", path, "error", serr)
		}
	}

	return int64(msg.ID), nil
}

// EditCaption replaces a photo message's caption and keyboard in place, the
// photo counterpart to EditMessage (same ModeHTML contract).
func (s *tbSession) EditCaption(messageID int64, caption string, rows [][]Btn) error {
	var chatID int64
	if chat := s.ctx.Chat(); chat != nil {
		chatID = chat.ID
	}
	sm := telebot.StoredMessage{
		MessageID: strconv.FormatInt(messageID, 10),
		ChatID:    chatID,
	}
	_, err := s.bot.EditCaption(sm, caption, buildMarkup(rows), telebot.ModeHTML)
	return err
}

func (s *tbSession) Respond(toast string) error {
	return s.ctx.Respond(&telebot.CallbackResponse{Text: toast})
}

// Data returns the raw callback payload. Buttons are built with only Text +
// Data set (no Unique — see buildMarkup), so telebot never prefixes the
// payload with "\f<unique>|"; the TrimPrefix below is defensive only, in
// case a future button sets Unique and telebot round-trips a "\f..." data
// string here.
func (s *tbSession) Data() string {
	if s.ctx.Callback() == nil {
		return s.ctx.Data()
	}
	return strings.TrimPrefix(s.ctx.Callback().Data, "\f")
}

// MessageText returns the incoming message's text (or caption), matching
// telebot's own Text() — deliberately NOT Data(), which for a plain message
// update is only the command payload (the trailing arguments after
// "/command "), not the full text a free-typed answer needs.
func (s *tbSession) MessageText() string {
	return s.ctx.Text()
}

// buildMarkup builds an inline-keyboard ReplyMarkup from rows. A callback
// button sets only Text and Data (no Unique): telebot's processButtons only
// rewrites Data into the "\f<unique>|<data>" form when Unique is non-empty,
// so leaving it empty guarantees the callback_data Telegram sends back is
// exactly btn.Data, letting handleCallback's prefix-based routing parse it
// directly. A Btn with InlineQueryChat set instead becomes a
// switch_inline_query_current_chat button (empty prefill, Btn's own doc) —
// Data is left unset on it, since Telegram (and telebot's Btn/InlineButton
// wire shape, markup.go) treats a button as exactly one of a callback or an
// inline-query switch, never both.
func buildMarkup(rows [][]Btn) *telebot.ReplyMarkup {
	markup := &telebot.ReplyMarkup{}
	trows := make([]telebot.Row, len(rows))
	for i, row := range rows {
		btns := make([]telebot.Btn, len(row))
		for j, btn := range row {
			if btn.InlineQueryChat {
				btns[j] = telebot.Btn{Text: btn.Label, InlineQueryChat: ""}
				continue
			}
			btns[j] = telebot.Btn{Text: btn.Label, Data: btn.Data}
		}
		trows[i] = markup.Row(btns...)
	}
	markup.Inline(trows...)
	return markup
}

// ── inline query (autocomplete, vibe/spike-autocomplete-inline.md) ──────────

// maxQueryResults caps how many suggestions handleQuery returns per
// inline-query keystroke — comfortably under Telegram's 50-result
// server-side cap for answerInlineQuery (spike §1, not enforced anywhere in
// the vendored telebot client); more than ~20 is noise for a human picking
// from a dropdown list anyway.
const maxQueryResults = 20

// queryCacheTimeSeconds is QueryResponse.CacheTime: telebot's own
// json:"cache_time,omitempty" tag means a Go zero value (0) is dropped from
// the outgoing JSON entirely, which is indistinguishable from never setting
// it at all — Telegram then falls back to its server-side default of 300s
// (spike §1), the opposite of what per-keystroke, typo-tolerant suggestions
// need. A small non-zero value is what actually reaches Telegram: 1s trades
// away only same-keystroke-repeat caching (spike's Open Risks section
// itself prefers "a few seconds, not 0, purely for duplicate-keystroke
// efficiency" over a literal 0) while keeping suggestions fresh per new
// character typed.
const queryCacheTimeSeconds = 1

// QueryResult is one inline-query suggestion, decoupled from telebot's
// *ArticleResult so buildQueryResults' ranking/label composition is
// unit-testable without any telebot dependency.
type QueryResult struct {
	// Title is the line shown in the suggestion list — the bare label, with
	// no flag-emoji prefix (kanban card "Autocomplete must be scoped to the
	// question's answer domain": the flag was noise and has been dropped).
	Title string
	// Text is the exact string sent into the chat when the suggestion is
	// tapped. The existing free-text grading path (handleText/
	// Trainer.AnswerText) grades this verbatim against whatever exercise
	// is open, indistinguishable from a hand-typed answer (spike §2).
	// Always equal to Title now that Title carries no decoration either,
	// but kept as its own field since the two answer different questions
	// (what's shown vs. what's sent).
	Text string
}

// buildQueryResults asks suggester for up to maxQueryResults matches for
// query, scoped to domain, and renders them as QueryResults. Pure (no I/O,
// no telebot dependency), so ranking/label composition is unit-testable
// without a telebot.Context. A nil suggester yields no results —
// handleQuery's own registration is already gated on a non-nil Suggester
// (see New), so this nil check only matters for a direct unit test of this
// function.
func buildQueryResults(suggester Suggester, query string, domain suggest.Domain, ggOnly bool) []QueryResult {
	if suggester == nil {
		return nil
	}
	matches := suggester.MatchDomain(query, domain, ggOnly, maxQueryResults)
	out := make([]QueryResult, len(matches))
	for i, m := range matches {
		out[i] = QueryResult{Title: m.Label, Text: m.Label}
	}
	return out
}

// queryContext is the minimal telebot.Context surface handleQuery needs —
// Query() and Answer() — narrowed the same way Session narrows
// telebot.Context for every other handler in this package (this package's
// doc comment), so handleQuery is unit-testable with a small hand-written
// fake instead of a bot token or the network. Unlike tbSession, no adapter
// type is needed here: a real telebot.Context's own Query()/Answer()
// methods already match this interface exactly, so it satisfies
// queryContext directly — telebot.Query simply has no callback/message
// concept for Session itself to adapt (vibe/spike-autocomplete-inline.md
// §1).
type queryContext interface {
	Query() *telebot.Query
	Answer(resp *telebot.QueryResponse) error
}

// queryScope resolves how q's suggestions should be scoped: the answer
// Domain (from the querying user's currently open ModeText exercise's
// CorrectAnswer, via Suggester.DomainForAnswer's country-first membership)
// and whether the user has the GeoGuessr-only filter on (ggOnly, so
// non-coverage countries are dropped from suggestions too). It defaults to
// (suggest.DomainCountry, false) — the same Domain default DomainForAnswer
// falls back to, and gg_only off (show everything) — when there's no
// Suggester wired, no query sender, or no matching internal user; once the
// user is resolved, ggOnly reflects their real setting even if no exercise
// is open. None of these are treated as errors (this package's "never block
// on a missing optional dependency" convention, e.g. mediaStore's doc).
func (b *Bot) queryScope(ctx context.Context, q *telebot.Query) (domain suggest.Domain, ggOnly bool) {
	if b.suggest == nil || q == nil || q.Sender == nil {
		return suggest.DomainCountry, false
	}
	u, ok, err := b.store.GetUserByTelegramID(ctx, q.Sender.ID)
	if err != nil || !ok {
		return suggest.DomainCountry, false
	}
	if b.exerciseStore == nil {
		return suggest.DomainCountry, u.GGOnly
	}
	ex, found, err := b.exerciseStore.GetOpenExerciseByMode(ctx, u.ID, int16(quiz.ModeText))
	if err != nil || !found {
		return suggest.DomainCountry, u.GGOnly
	}
	return b.suggest.DomainForAnswer(ex.CorrectAnswer), u.GGOnly
}

// handleQuery answers an inline query (telebot.OnQuery) with up to
// maxQueryResults typo-tolerant Suggest matches for the query text, scoped
// to the querying user's currently open ModeText exercise's answer domain
// (queryDomain) — a "capital of X?" question suggests capitals, every other
// question suggests countries (kanban card "Autocomplete must be scoped to
// the question's answer domain"). A query can only be answered once
// (telebot's Bot.Answer doc comment, bot_raw.go) — an Answer error is
// logged, not returned, and a panic is recovered, mirroring wrap's
// never-let-anything-escape contract for every other handler (this one
// bypasses wrap/Session entirely — see queryContext's doc).
func (b *Bot) handleQuery(c queryContext) (err error) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("telegram: query handler panic", "recover", r)
		}
	}()

	ctx := context.Background()
	domain, ggOnly := b.queryScope(ctx, c.Query())

	results := buildQueryResults(b.suggest, c.Query().Text, domain, ggOnly)
	articles := make(telebot.Results, len(results))
	for i, r := range results {
		articles[i] = &telebot.ArticleResult{
			ResultBase: telebot.ResultBase{
				// Content, never the legacy ArticleResult.Text shortcut
				// (spike's Open Risks: that field predates the current Bot
				// API schema and may be dead on modern servers).
				Content: &telebot.InputTextMessageContent{Text: r.Text},
			},
			Title: r.Title,
		}
	}

	if aerr := c.Answer(&telebot.QueryResponse{
		Results: articles,
		// IsPersonal: true — per-user grading context (which exercise is
		// open) is never encoded in the result at all (spike §2), so one
		// user's cached list must never leak to a different user typing
		// the same query text.
		IsPersonal: true,
		CacheTime:  queryCacheTimeSeconds,
	}); aerr != nil {
		b.logger.Error("telegram: answer query", "error", aerr)
	}
	return nil
}
