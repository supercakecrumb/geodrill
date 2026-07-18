// Package telegram is geodrill's Telegram bot layer (architecture contract
// §5). It renders internal/train's exercise/answer/stats output over
// gopkg.in/telebot.v4, and stays deliberately thin: all scheduling,
// generation, and grading logic lives in internal/train; this package only
// talks to Telegram and internal/storage's user/deck bookkeeping.
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
	telebot "gopkg.in/telebot.v4"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/train"
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
// "🚧 coming with v2 wiring" reply until wired. TrainerV2 is REQUIRED
// (deliverable 5's v2 cutover): /train, /practice, /stats, and the
// reminder loop's due-review count all call it unconditionally now — the
// legacy trainer fallback they used to have is gone, and the free-text
// OnText handler is only registered when it's non-nil.
type Config struct {
	Token   string
	Store   *storage.Store
	Service *train.Service
	Logger  *slog.Logger
	Now     func() time.Time

	// StudyService powers /study, /introduce, and the reminder loop's
	// introduction nudge (architecture §5.1, §5.3).
	StudyService StudyService
	// TopicService powers the /topics tree browser (architecture §5.2).
	TopicService TopicService
	// TrainerV2 powers the mode-aware v2 exercise path (architecture §1.6):
	// /train, /practice, /stats, and the reminder loop's due count all
	// require it now (see this type's doc comment); its presence also
	// decides whether the free-text OnText handler is registered at all.
	TrainerV2 TrainerV2
	// IntroCapStore powers the /settings daily intro-cap row.
	IntroCapStore IntroCapStore
}

// Bot wires telebot to geodrill's train/storage layers.
type Bot struct {
	tb     *telebot.Bot
	store  userStore
	svc    trainer
	logger *slog.Logger
	now    func() time.Time

	// study is nil until a later wave wires Config.StudyService — every
	// call site checks it before use (see study.go).
	study StudyService
	// topics is nil until a later wave wires Config.TopicService — every
	// call site checks it before use (see topics_ui.go).
	topics TopicService
	// trainerV2 is required (Config.TrainerV2, see this package's Config
	// doc comment) — /train, /practice, /stats, and the reminder loop's
	// due count call it unconditionally (trainv2.go); only OnText's
	// registration still checks it for nil (bot.go's New).
	trainerV2 TrainerV2
	introCap  IntroCapStore

	remindedMu  sync.Mutex
	remindState map[uuid.UUID]reminderState // userID -> today's reminder progress

	practiceMu    sync.Mutex
	practiceStart map[int64]time.Time // telegram user id -> current /practice session start
}

// markPracticeStart records (or resets) the start of a user's /practice
// session, so the Stop control can summarize just that run.
func (b *Bot) markPracticeStart(telegramID int64, t time.Time) {
	b.practiceMu.Lock()
	defer b.practiceMu.Unlock()
	if b.practiceStart == nil {
		b.practiceStart = make(map[int64]time.Time)
	}
	b.practiceStart[telegramID] = t
}

// takePracticeStart returns and clears a user's /practice session start. ok is
// false if none is recorded (e.g. after a bot restart mid-session).
func (b *Bot) takePracticeStart(telegramID int64) (time.Time, bool) {
	b.practiceMu.Lock()
	defer b.practiceMu.Unlock()
	t, ok := b.practiceStart[telegramID]
	delete(b.practiceStart, telegramID)
	return t, ok
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
		svc:           cfg.Service,
		logger:        logger,
		now:           now,
		study:         cfg.StudyService,
		topics:        cfg.TopicService,
		trainerV2:     cfg.TrainerV2,
		introCap:      cfg.IntroCapStore,
		remindState:   make(map[uuid.UUID]reminderState),
		practiceStart: make(map[int64]time.Time),
	}

	tb.Handle("/start", b.wrap(b.handleStart))
	tb.Handle("/train", b.wrap(b.handleTrain))
	tb.Handle("/practice", b.wrap(b.handlePractice))
	tb.Handle("/decks", b.wrap(b.handleDecks))
	tb.Handle("/settings", b.wrap(b.handleSettings))
	tb.Handle("/stats", b.wrap(b.handleStats))
	tb.Handle("/help", b.wrap(b.handleHelp))
	tb.Handle("/study", b.wrap(b.handleStudy))
	tb.Handle("/introduce", b.wrap(b.handleStudy)) // alias that fetches more intro cards on demand (decision 2)
	tb.Handle("/topics", b.wrap(b.handleTopics))
	tb.Handle(telebot.OnCallback, b.wrap(b.handleCallback))
	// OnText (free-typed answers) is only registered when TrainerV2 is
	// wired: the pre-v2 bot never listened for plain text at all, and
	// nil-safety means that stays true until a real TrainerV2 arrives —
	// registering it unconditionally would start intercepting every plain
	// message the moment this field exists, even with Config.TrainerV2 nil.
	if cfg.TrainerV2 != nil {
		tb.Handle(telebot.OnText, b.wrap(b.handleText))
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
	{Text: "train", Description: "Next due exercise"},
	{Text: "practice", Description: "Endless practice (no scheduling)"},
	{Text: "study", Description: "Introduce new items (v2)"},
	{Text: "topics", Description: "Browse topics, tiers & progress (v2)"},
	{Text: "decks", Description: "Now points to /topics"},
	{Text: "settings", Description: "Daily cap, reminders, button style"},
	{Text: "stats", Description: "Your progress and mix-ups"},
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

		s := &tbSession{bot: b.tb, ctx: c}
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

		due, err := b.trainerV2.DueCount(ctx, u.ID)
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
	bot *telebot.Bot
	ctx telebot.Context
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
func (s *tbSession) SendPhoto(path, caption string, rows [][]Btn) (int64, error) {
	photo := &telebot.Photo{File: telebot.FromDisk(path), Caption: caption}
	msg, err := s.bot.Send(s.ctx.Chat(), photo, buildMarkup(rows), telebot.ModeHTML)
	if err != nil {
		return 0, err
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

// buildMarkup builds an inline-keyboard ReplyMarkup from rows. Buttons set
// only Text and Data (no Unique): telebot's processButtons only rewrites
// Data into the "\f<unique>|<data>" form when Unique is non-empty, so
// leaving it empty guarantees the callback_data Telegram sends back is
// exactly btn.Data, letting train.ParseCallback parse it directly.
func buildMarkup(rows [][]Btn) *telebot.ReplyMarkup {
	markup := &telebot.ReplyMarkup{}
	trows := make([]telebot.Row, len(rows))
	for i, row := range rows {
		btns := make([]telebot.Btn, len(row))
		for j, btn := range row {
			btns[j] = telebot.Btn{Text: btn.Label, Data: btn.Data}
		}
		trows[i] = markup.Row(btns...)
	}
	markup.Inline(trows...)
	return markup
}
