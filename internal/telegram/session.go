package telegram

// Btn is one inline-keyboard button: either a callback button (Label +
// Data) or a switch_inline_query_current_chat prefill button (Label, with
// InlineQueryChat set true and Data left empty) — see buildMarkup (bot.go)
// for how each maps onto telebot's wire types. Never set both Data and
// InlineQueryChat on the same button.
type Btn struct {
	Label string
	Data  string

	// InlineQueryChat, when true, renders this as a
	// switch_inline_query_current_chat button (empty prefill) instead of a
	// callback button: tapping it inserts "@<bot-username> " into the
	// current chat's input field, cursor ready, rather than firing a
	// callback (vibe/spike-autocomplete-inline.md §3). Data is ignored when
	// this is true — geodrill has no use yet for a non-empty prefill query,
	// so there is no field to carry one.
	InlineQueryChat bool
}

// Session is the minimal surface handler logic needs from a Telegram
// update. It exists so handlers (handlers.go) are unit-testable with a fake
// implementation — no bot token, no telebot import, no network. The live
// implementation (tbSession, in bot.go) adapts a telebot.Context to this
// interface.
type Session interface {
	// UserID is the Telegram user id of the sender.
	UserID() int64
	// Username is the Telegram @username of the sender (may be empty).
	Username() string
	// MessageID is the id of the message an inline keyboard is attached to,
	// when handling a callback; 0 outside a callback.
	MessageID() int64
	// Send sends a plain text message to the user.
	Send(text string) error
	// SendKeyboard sends text with an inline keyboard (one row per entry of
	// rows) and returns the id of the sent message.
	SendKeyboard(text string, rows [][]Btn) (int64, error)
	// EditKeyboard replaces the inline keyboard attached to messageID in
	// place, leaving the message text untouched.
	EditKeyboard(messageID int64, rows [][]Btn) error
	// EditMessage replaces both the text and the inline keyboard of
	// messageID in place (editMessageText). text is HTML (Telegram HTML
	// parse mode); the caller must escape any user-supplied content.
	EditMessage(messageID int64, text string, rows [][]Btn) error
	// SendPhoto sends the local image file at path as a photo message with
	// caption and an inline keyboard, and returns the id of the sent
	// message. Introduction/exercise media renders as a photo message from
	// birth (architecture §5.1 decision 6) rather than text-then-media.
	// caption is HTML (Telegram HTML parse mode), exactly like EditMessage
	// — the caller must escape any user-supplied content. Kept in the same
	// parse mode as EditCaption so a card's caption never changes rendering
	// between its initial send and a later in-place edit.
	SendPhoto(path, caption string, rows [][]Btn) (int64, error)
	// EditCaption replaces both the caption and the inline keyboard of the
	// photo message at messageID in place (editMessageCaption) — the photo
	// counterpart to EditMessage. caption is HTML, caller-escaped, exactly
	// like EditMessage.
	EditCaption(messageID int64, caption string, rows [][]Btn) error
	// Respond answers a callback query with a transient toast (no alert).
	Respond(toast string) error
	// Data is the callback payload; empty outside a callback.
	Data() string
	// MessageText is the raw text of an incoming plain-text message (a
	// telebot.OnText update, never a callback) — used to route free-typed
	// answers to Trainer.AnswerText. Empty outside a plain text message.
	MessageText() string
}
