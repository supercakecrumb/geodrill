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
// check whether any user's local morning has arrived.
const reminderCheckInterval = 30 * time.Minute

// reminderLocalHour is the local hour (in the user's timezone) at which a
// due-reviews reminder is sent, at most once per local day.
const reminderLocalHour = 9

// Config configures a Bot.
type Config struct {
	Token   string
	Store   *storage.Store
	Service *train.Service
	Logger  *slog.Logger
	Now     func() time.Time
}

// Bot wires telebot to geodrill's train/storage layers.
type Bot struct {
	tb     *telebot.Bot
	store  userStore
	svc    trainer
	logger *slog.Logger
	now    func() time.Time

	remindedMu sync.Mutex
	remindedOn map[uuid.UUID]string // userID -> yyyy-mm-dd (user-local) last reminded
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
		tb:         tb,
		store:      cfg.Store,
		svc:        cfg.Service,
		logger:     logger,
		now:        now,
		remindedOn: make(map[uuid.UUID]string),
	}

	tb.Handle("/start", b.wrap(b.handleStart))
	tb.Handle("/train", b.wrap(b.handleTrain))
	tb.Handle("/practice", b.wrap(b.handlePractice))
	tb.Handle("/decks", b.wrap(b.handleDecks))
	tb.Handle("/stats", b.wrap(b.handleStats))
	tb.Handle(telebot.OnCallback, b.wrap(b.handleCallback))

	return b, nil
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

// remindLoop periodically checks every user opted into reminders and, once
// per user-local day at reminderLocalHour, nudges them if they have
// anything due.
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
		if local.Hour() != reminderLocalHour {
			continue
		}

		today := local.Format("2006-01-02")
		if b.alreadyReminded(u.ID, today) {
			continue
		}

		due, err := b.svc.DueCount(ctx, u, now)
		if err != nil {
			b.logger.Error("telegram: reminders: due count", "user", u.ID, "error", err)
			continue
		}
		if due <= 0 {
			continue
		}

		text := fmt.Sprintf("🔔 You have %d review", due)
		if due != 1 {
			text += "s"
		}
		text += " due today."

		if _, err := b.tb.Send(telebot.ChatID(u.TelegramID), text); err != nil {
			b.logger.Error("telegram: reminders: send", "user", u.ID, "error", err)
			continue
		}
		b.markReminded(u.ID, today)
	}
}

func (b *Bot) alreadyReminded(userID uuid.UUID, day string) bool {
	b.remindedMu.Lock()
	defer b.remindedMu.Unlock()
	return b.remindedOn[userID] == day
}

func (b *Bot) markReminded(userID uuid.UUID, day string) {
	b.remindedMu.Lock()
	defer b.remindedMu.Unlock()
	b.remindedOn[userID] = day
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
