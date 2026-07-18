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

// dataPracticePrefix is the callback prefix for Trainer's /practice
// answers: "prac:<exercise-uuid>:<index>" — the practice counterpart of
// ans:. A separate prefix (rather than reusing ans: with an extra flag)
// lets handleCallback's routing alone decide which "next" step follows
// grading (sendNextPractice vs sendNextTrain).
const dataPracticePrefix = "prac:"

// practiceCallbackData builds one practice option button's payload.
func practiceCallbackData(exerciseID uuid.UUID, index int) string {
	return dataPracticePrefix + exerciseID.String() + ":" + strconv.Itoa(index)
}

// parsePracticeCallback parses a payload built by practiceCallbackData.
// ok is false for anything malformed.
func parsePracticeCallback(data string) (exerciseID uuid.UUID, index int, ok bool) {
	return parseIndexCallback(dataPracticePrefix, data)
}

// parseIndexCallback parses a "<prefix><exercise-uuid>:<index>" payload —
// the shared shape behind both parseAnswerCallback and
// parsePracticeCallback. ok is false for anything malformed.
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

// sendNextPractice sends the next practice exercise for user via
// Trainer's practice pool (across enabled+tier-unlocked topics) — the
// legacy unscheduled-practice fallback is gone.
func (b *Bot) sendNextPractice(ctx context.Context, s Session, user storage.User) error {
	p, err := b.trainer.NextPractice(ctx, user.ID)
	if err != nil {
		return err
	}
	return b.sendPrompt(s, user, p)
}

// sendPrompt renders a Prompt result, mirroring sendNextResult's
// Kind switch for the exercise path.
func (b *Bot) sendPrompt(s Session, user storage.User, p Prompt) error {
	switch p.Kind {
	case PromptKindExercise:
		return b.sendExercise(s, p)
	case PromptKindNothingDue:
		if !p.DueAt.IsZero() {
			loc := locationFor(user)
			return s.Send(fmt.Sprintf("Nothing due right now. Come back at %s.", p.DueAt.In(loc).Format("15:04")))
		}
		return s.Send(allCaughtUpText)
	case PromptKindNoContent:
		return s.Send(noContentText)
	case PromptKindNoTopics:
		return s.Send(noTopicsText)
	default:
		return s.Send(fallbackText)
	}
}

// sendExercise sends one ready Prompt: a photo-from-birth or text
// message, with option buttons for ModeSingle/ModeSet or a bare "type your
// answer" prompt (no buttons) for ModeText. A practice exercise (p.Practice)
// gets a trailing "⏹ Stop practice" control, same as the legacy /practice
// prompt.
func (b *Bot) sendExercise(s Session, p Prompt) error {
	text := p.Text
	var rows [][]Btn
	if p.Mode == quiz.ModeText {
		text += "\n\n✏️ Type your answer."
	} else {
		rows = optionRows(p.ExerciseID, p.Options, p.Practice)
	}
	if p.Practice {
		rows = append(rows, []Btn{{Label: "⏹ Stop practice", Data: dataStopPractice}})
	}
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
// legacy exercise path. practice selects which callback prefix each
// option's Data carries (prac: vs ans:), so handleCallback's routing alone
// decides whether grading advances via sendNextPractice or sendNextTrain.
func optionRows(exerciseID uuid.UUID, options []Option, practice bool) [][]Btn {
	if len(options) == 0 {
		return nil
	}
	callbackData := answerCallbackData
	if practice {
		callbackData = practiceCallbackData
	}
	rows := make([][]Btn, 0, (len(options)+1)/2)
	for i := 0; i < len(options); i += 2 {
		row := []Btn{{Label: options[i].Label, Data: callbackData(exerciseID, options[i].Index)}}
		if i+1 < len(options) {
			next := options[i+1]
			row = append(row, Btn{Label: next.Label, Data: callbackData(exerciseID, next.Index)})
		}
		rows = append(rows, row)
	}
	return rows
}

// ── ans: / prac: callbacks (button answer) ───────────────────────────────

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
	return b.answerAndAdvance(ctx, s, exerciseID, index, (*Bot).sendNextTrain)
}

// handlePracticeAnswerCallback grades one prac: tap (a /practice exercise)
// through the SAME Answer grading path as ans: — the exercise row already
// carries practice=true (set at NextPractice time), so Answer's
// underlying finishAnswer already knows to skip FSRS movement without this
// callback saying so again — then sends the next practice exercise instead
// of the next due one.
func (b *Bot) handlePracticeAnswerCallback(ctx context.Context, s Session, data string) error {
	if b.trainer == nil {
		return s.Respond("")
	}
	exerciseID, index, ok := parsePracticeCallback(data)
	if !ok {
		return s.Respond("")
	}
	return b.answerAndAdvance(ctx, s, exerciseID, index, (*Bot).sendNextPractice)
}

// answerAndAdvance is the shared grading flow behind both ans: and prac:
// taps: grade via Answer, edit the exercise message/caption in place,
// toast the result, then advance via next (sendNextTrain or
// sendNextPractice, picked by the caller per the tapped prefix).
func (b *Bot) answerAndAdvance(ctx context.Context, s Session, exerciseID uuid.UUID, index int, next func(*Bot, context.Context, Session, storage.User) error) error {
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
	return next(b, ctx, s, user)
}

// ── free-typed answers (telebot.OnText) ─────────────────────────────────

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
	if err := s.Send(answerToast(res.Correct)); err != nil {
		return err
	}
	// A free-typed reply carries no callback prefix to tell a practice
	// answer from a scheduled one, unlike ans:/prac: — res.Practice (echoed
	// from the graded exercise's own practice flag) is what decides here.
	if res.Practice {
		return b.sendNextPractice(ctx, s, user)
	}
	return b.sendNextTrain(ctx, s, user)
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
