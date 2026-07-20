package telegram

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// dataAnswerPrefix is the callback prefix for Trainer's index-based
// answers: "ans:<exercise-uuid>:<index>". Budget: "ans:"(4) + uuid(36) +
// ":"(1) + index(up to 3 digits) = 44, comfortably under Telegram's
// 64-byte callback_data cap.
const dataAnswerPrefix = "ans:"

// answerCallbackData builds one option button's payload.
func answerCallbackData(exerciseID uuid.UUID, index int) string {
	return dataAnswerPrefix + exerciseID.String() + ":" + strconv.Itoa(index)
}

// parseAnswerCallback parses a payload built by answerCallbackData. ok
// is false for anything malformed.
func parseAnswerCallback(data string) (exerciseID uuid.UUID, index int, ok bool) {
	return parseIndexCallback(dataAnswerPrefix, data)
}

// parseIndexCallback parses a "<prefix><exercise-uuid>:<index>" payload —
// the shape behind parseAnswerCallback. ok is false for anything malformed.
func parseIndexCallback(prefix, data string) (exerciseID uuid.UUID, index int, ok bool) {
	rest, hasPrefix := strings.CutPrefix(data, prefix)
	if !hasPrefix {
		return uuid.UUID{}, 0, false
	}
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return uuid.UUID{}, 0, false
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.UUID{}, 0, false
	}
	idx, err := strconv.Atoi(parts[1])
	if err != nil || idx < 0 {
		return uuid.UUID{}, 0, false
	}
	return id, idx, true
}

// dataIdkPrefix is the callback prefix for the "🤷 I don't know" button:
// "idk:<exercise-uuid>". Budget: "idk:"(4) + uuid(36) = 40, comfortably
// under Telegram's 64-byte callback_data cap.
const dataIdkPrefix = "idk:"

// idkCallbackData builds the "I don't know" button's payload for an exercise.
func idkCallbackData(exerciseID uuid.UUID) string {
	return dataIdkPrefix + exerciseID.String()
}

// parseIdkCallback parses a payload built by idkCallbackData. ok is false
// for anything malformed.
func parseIdkCallback(data string) (exerciseID uuid.UUID, ok bool) {
	rest, hasPrefix := strings.CutPrefix(data, dataIdkPrefix)
	if !hasPrefix {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

// ── /train ───────────────────────────────────────────────────────────────

// sendNextTrain sends the next due exercise for user via Trainer
// (Config.Trainer is required from here on — the legacy trainer fallback
// is gone). Both /train and the reminder's "▶️ Start reviewing" button go
// through this.
func (b *Bot) sendNextTrain(ctx context.Context, s Session, user storage.User) error {
	p, err := b.trainer.NextExercise(ctx, user.ID)
	if err != nil {
		return err
	}
	return b.sendPrompt(s, user, p)
}

// sendPrompt renders a Prompt result, mirroring sendNextResult's
// Kind switch for the exercise path. Every non-exercise branch is /train's
// terminal/idle screen (hub-and-spoke rule): none carried a keyboard
// before, so a one-button «⬅️ Menu» keyboard is the minimal fix. The
// PromptKindNothingDue branch also carries real progress stats
// (nothingDueText) instead of a bare "nothing due" line.
func (b *Bot) sendPrompt(s Session, user storage.User, p Prompt) error {
	switch p.Kind {
	case PromptKindExercise:
		return b.sendExercise(s, p)
	case PromptKindNothingDue:
		_, err := s.SendKeyboard(nothingDueText(user, p), menuBackRow())
		return err
	case PromptKindNoContent:
		_, err := s.SendKeyboard(noContentText, menuBackRow())
		return err
	default:
		_, err := s.SendKeyboard(fallbackText, menuBackRow())
		return err
	}
}

// nothingDueText renders /train's "nothing due" idle screen: the review-
// pipeline stats Prompt.Summary carries, plus either a self-explanatory
// next-review time (DueAt localized to user's timezone — the same
// locationFor call the old one-liner used) or, when DueAt is zero (nothing
// scheduled AND nothing left to learn — see study.Service.NextExercise),
// a plain "all caught up" closer instead of a bogus time.
func nothingDueText(user storage.User, p Prompt) string {
	stats := fmt.Sprintf("Reviewed today: %d\n%d reviews scheduled\n%d left to learn",
		p.Summary.ReviewsToday, p.Summary.ReviewsScheduled, p.Summary.LeftToLearn)

	if p.DueAt.IsZero() {
		return "You're all caught up — nothing scheduled and nothing new to learn right now.\n\n" + stats
	}

	loc := locationFor(user)
	nextAt := p.DueAt.In(loc).Format("15:04")
	return fmt.Sprintf("Nothing due right now.\n\n%s\n\nYour next review unlocks at %s (that's when your soonest item is scheduled to come back).", stats, nextAt)
}

// autocompleteButtonLabel is the inline-query prefill button shown on a
// ModeText exercise that supports autocomplete (Prompt.Autocomplete,
// vibe/spike-autocomplete-inline.md): tapping it prefills
// "@<bot-username> " (switch_inline_query_current_chat, empty query) into
// the current chat's input field, ready for typing.
const autocompleteButtonLabel = "⌨️ Type your answer"

// dontKnowButtonLabel is the explicit "give up" button shown on every
// /train question: tapping it registers the card as NOT KNOWN (a FAIL,
// same outcome as a wrong answer), reveals the correct answer, and advances
// to the next card (handleIdkCallback / Trainer.AnswerDontKnow).
const dontKnowButtonLabel = "🤷 I don't know"

// sendExercise sends one ready Prompt: a photo-from-birth or text
// message, with option buttons for ModeSingle/ModeSet or a bare "type your
// answer" prompt (no buttons, unless Autocomplete adds its own prefill
// button) for ModeText.
func (b *Bot) sendExercise(s Session, p Prompt) error {
	text := p.Text
	var rows [][]Btn
	if p.Mode == quiz.ModeText {
		text += "\n\n✏️ Type your answer."
		if p.Autocomplete {
			rows = append(rows, []Btn{{Label: autocompleteButtonLabel, InlineQueryChat: true}})
		}
	} else {
		rows = optionRows(p.ExerciseID, p.Options)
	}
	// Every question carries an explicit "I don't know" escape hatch (a FAIL,
	// same outcome as a wrong answer): after the autocomplete row for ModeText,
	// after the option rows for indexed modes.
	rows = append(rows, []Btn{{Label: dontKnowButtonLabel, Data: idkCallbackData(p.ExerciseID)}})
	if p.MediaPath != "" {
		// SendPhoto is ModeHTML (see its Session doc comment) — escape like
		// any other dynamic text going through an HTML-parsed send/edit.
		_, err := s.SendPhoto(p.MediaPath, html.EscapeString(text), rows)
		return err
	}
	_, err := s.SendKeyboard(text, rows)
	return err
}

// optionRows lays out a Prompt's options two per row (a trailing odd
// option sits alone on the last row), mirroring buttonRows' layout for the
// legacy exercise path.
func optionRows(exerciseID uuid.UUID, options []Option) [][]Btn {
	if len(options) == 0 {
		return nil
	}
	rows := make([][]Btn, 0, (len(options)+1)/2)
	for i := 0; i < len(options); i += 2 {
		row := []Btn{{Label: options[i].Label, Data: answerCallbackData(exerciseID, options[i].Index)}}
		if i+1 < len(options) {
			next := options[i+1]
			row = append(row, Btn{Label: next.Label, Data: answerCallbackData(exerciseID, next.Index)})
		}
		rows = append(rows, row)
	}
	return rows
}

// ── ans: callbacks (button answer) ───────────────────────────────────────

// handleAnswerCallback grades one ans: tap (a /train exercise) via
// Trainer.Answer, edits the exercise in place, toasts the result, and
// sends the next due exercise.
func (b *Bot) handleAnswerCallback(ctx context.Context, s Session, data string) error {
	if b.trainer == nil {
		return s.Respond("")
	}
	exerciseID, index, ok := parseAnswerCallback(data)
	if !ok {
		return s.Respond("")
	}
	return b.answerAndAdvance(ctx, s, exerciseID, index)
}

// answerAndAdvance is the shared grading flow behind an ans: tap: grade via
// Answer, edit the exercise message/caption in place, toast the result,
// then send the next due exercise.
func (b *Bot) answerAndAdvance(ctx context.Context, s Session, exerciseID uuid.UUID, index int) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	res, err := b.trainer.Answer(ctx, user.ID, exerciseID, index)
	if err != nil {
		return err
	}
	if res.Stale {
		return s.Respond(staleToast)
	}

	b.applyAnswerEdit(s, res)
	if err := s.Respond(answerToast(res.Correct)); err != nil {
		return err
	}
	return b.sendNextTrain(ctx, s, user)
}

// ── idk: callbacks ("🤷 I don't know") ────────────────────────────────────

// handleIdkCallback grades one idk: tap as a FAIL (not-known) via
// Trainer.AnswerDontKnow, edits the exercise in place to reveal the answer,
// and sends the next due exercise — mirroring handleAnswerCallback, except
// the outcome is always wrong. For ModeText it also sends the correct
// spelling as a follow-up (the in-place reveal edit is scrolled out of view,
// exactly the wrongReveal case handleText handles).
func (b *Bot) handleIdkCallback(ctx context.Context, s Session, data string) error {
	if b.trainer == nil {
		return s.Respond("")
	}
	exerciseID, ok := parseIdkCallback(data)
	if !ok {
		return s.Respond("")
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	res, err := b.trainer.AnswerDontKnow(ctx, user.ID, exerciseID)
	if err != nil {
		return err
	}
	if res.Stale {
		return s.Respond(staleToast)
	}

	if err := s.Respond(""); err != nil {
		return err
	}
	b.applyAnswerEdit(s, res)
	if res.CorrectAnswer != "" {
		if err := s.Send(wrongReveal(res.CorrectAnswer)); err != nil {
			return err
		}
	}
	return b.sendNextTrain(ctx, s, user)
}

// ── free-typed answers (telebot.OnText) ─────────────────────────────────

// stripBotMention strips a LEADING "@<botUsername>" token (and any
// surrounding whitespace) from text, returning the remainder. It exists
// because Telegram's inline-mode autocomplete (spike-autocomplete-inline.md)
// inserts the bot's own @mention ahead of the tapped suggestion when a user
// replies via that autocomplete in a group chat, so the raw message text
// becomes "@geodriller_bot Japan" instead of "Japan" — grading that raw
// text against "Japan" fails even though the answer is correct (the
// "Nagoya→Japan marked wrong" bug; it also masqueraded as a case-insensitive
// matching bug, since quiz.TextMatcher already casefolds via quiz.Normalize
// once the mention is out of the way).
//
// The username comparison is case-insensitive (Telegram usernames are
// case-insensitive), but only a mention at the very START of the text is
// stripped — a "@bot" appearing mid-answer is left alone, since that's part
// of whatever the user actually typed, not autocomplete noise. An empty
// botUsername (unresolved b.username) or a non-matching leading token
// leaves text unchanged apart from trimming. Pure (no I/O), so it's
// unit-tested directly without a bot token or Session.
func stripBotMention(text, botUsername string) string {
	trimmed := strings.TrimSpace(text)
	if botUsername == "" {
		return trimmed
	}
	mention := "@" + botUsername
	if !strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(mention)) {
		return trimmed
	}
	return strings.TrimSpace(trimmed[len(mention):])
}

// handleText routes a plain-text message to Trainer.AnswerText when
// Trainer is wired and the message isn't a command (telebot still
// dispatches OnText for an UNREGISTERED "/foo" command after its command
// match fails — see telebot's update.go ProcessContext — so this guard is
// required, not defensive fluff). Grading, then sending the next exercise,
// mirrors handleAnswerCallback exactly, except there is no callback to
// Respond to: the toast becomes a plain follow-up message.
func (b *Bot) handleText(ctx context.Context, s Session) error {
	if b.trainer == nil {
		return nil
	}
	text := s.MessageText()
	if text == "" || strings.HasPrefix(strings.TrimSpace(text), "/") {
		return nil
	}
	text = stripBotMention(text, b.username)

	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	res, ok, err := b.trainer.AnswerText(ctx, user.ID, text)
	if err != nil {
		return err
	}
	if !ok {
		return nil // no open ModeText exercise — leave the message alone
	}
	if res.Stale {
		return s.Send(staleToast)
	}

	b.applyAnswerEdit(s, res)
	// On a wrong free-text answer the in-place edit that reveals the correct
	// answer is scrolled up out of view, so the bare wrong toast would leave
	// the user with no answer. Fold the correct spelling into the sent reply
	// (ModeText only — res.CorrectAnswer is empty for button modes, which
	// already ✅-mark the right option).
	reply := answerToast(res.Correct)
	if !res.Correct && res.CorrectAnswer != "" {
		reply = wrongReveal(res.CorrectAnswer)
	}
	if err := s.Send(reply); err != nil {
		return err
	}
	return b.sendNextTrain(ctx, s, user)
}

// wrongReveal is the free-text wrong-answer reply that includes the correct
// spelling, since the in-place message edit revealing it is scrolled out of
// view by the time the follow-up arrives. Pure, so it's unit-testable.
func wrongReveal(correctAnswer string) string {
	return "❌ Wrong — the answer is " + correctAnswer
}

// ── shared grading render ────────────────────────────────────────────────

// applyAnswerEdit edits the exercise message/caption in place to res's
// graded re-render: EditCaption for a photo exercise, EditMessage for text
// — mirroring Prompt's own Text/MediaPath split. Falls back to the
// session's current message id when res doesn't carry its own (the same
// HasMessage fallback handleAnswerCallback uses for the legacy path). Edit
// failures are logged, not fatal — grading has already happened.
func (b *Bot) applyAnswerEdit(s Session, res AnswerResult) {
	msgID := s.MessageID()
	if res.HasMessage {
		msgID = res.MessageID
	}
	rows := gradedOptionRows(res.Options)
	text := html.EscapeString(res.Text)

	var err error
	if res.MediaPath != "" {
		err = s.EditCaption(msgID, text, rows)
	} else {
		err = s.EditMessage(msgID, text, rows)
	}
	if err != nil {
		b.logger.Warn("telegram: edit answer", "error", err)
	}
}

// gradedOptionRows lays out graded options two per row, decorated with
// ✅/❌ (DecorateLabel) and wired to the inert noop callback.
func gradedOptionRows(options []GradedOption) [][]Btn {
	if len(options) == 0 {
		return nil
	}
	graded := func(o GradedOption) Btn {
		return Btn{Label: DecorateLabel(o.Label, o.Mark), Data: DataNoop}
	}
	rows := make([][]Btn, 0, (len(options)+1)/2)
	for i := 0; i < len(options); i += 2 {
		row := []Btn{graded(options[i])}
		if i+1 < len(options) {
			row = append(row, graded(options[i+1]))
		}
		rows = append(rows, row)
	}
	return rows
}

// answerToast picks the correct/wrong toast text, shared by the callback
// and free-text answer paths.
func answerToast(correct bool) string {
	if correct {
		return correctToast
	}
	return wrongToast
}
