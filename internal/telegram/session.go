package telegram

// Btn is one inline-keyboard button: a label and its callback payload.
type Btn struct {
	Label string
	Data  string
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
	// Respond answers a callback query with a transient toast (no alert).
	Respond(toast string) error
	// Data is the callback payload; empty outside a callback.
	Data() string
}
