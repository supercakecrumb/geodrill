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

// dataV2AnswerPrefix is the callback prefix for TrainerV2's index-based
// answers: "v2a:<exercise-uuid>:<index>". The legacy "ans:"/"prac:"
// key-based prefixes are retired (isLegacyAnswerCallback in handlers.go
// now only recognizes their shape, to toast a stale button as expired) —
// v2 exercises have always answered through this separate index-based
// prefix instead. Budget: "v2a:"(4) + uuid(36) + ":"(1) + index(up to 3
// digits) = 44, comfortably under Telegram's 64-byte callback_data cap.
const dataV2AnswerPrefix = "v2a:"

// v2AnswerCallbackData builds one option button's payload.
func v2AnswerCallbackData(exerciseID uuid.UUID, index int) string {
	return dataV2AnswerPrefix + exerciseID.String() + ":" + strconv.Itoa(index)
}

// parseV2AnswerCallback parses a payload built by v2AnswerCallbackData. ok
// is false for anything malformed.
func parseV2AnswerCallback(data string) (exerciseID uuid.UUID, index int, ok bool) {
	return parseV2IndexCallback(dataV2AnswerPrefix, data)
}

// dataV2PracticePrefix is the callback prefix for TrainerV2's /practice
// answers: "v2p:<exercise-uuid>:<index>" — the practice counterpart of
// v2a:. A separate prefix (rather than reusing v2a: with an extra flag)
// lets handleCallback's routing alone decide which "next" step follows
// grading (sendNextPractice vs sendNextTrain), mirroring how the legacy
// ans:/prac: prefixes split scheduled from practice taps.
const dataV2PracticePrefix = "v2p:"

// v2PracticeCallbackData builds one practice option button's payload.
func v2PracticeCallbackData(exerciseID uuid.UUID, index int) string {
	return dataV2PracticePrefix + exerciseID.String() + ":" + strconv.Itoa(index)
}

// parseV2PracticeCallback parses a payload built by v2PracticeCallbackData.
// ok is false for anything malformed.
func parseV2PracticeCallback(data string) (exerciseID uuid.UUID, index int, ok bool) {
	return parseV2IndexCallback(dataV2PracticePrefix, data)
}

// parseV2IndexCallback parses a "<prefix><exercise-uuid>:<index>" payload —
// the shared shape behind both parseV2AnswerCallback and
// parseV2PracticeCallback. ok is false for anything malformed.
func parseV2IndexCallback(prefix, data string) (exerciseID uuid.UUID, index int, ok bool) {
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

// sendNextTrain sends the next due exercise for user via TrainerV2
// (Config.TrainerV2 is required from here on — the legacy trainer fallback
// is gone). Both /train and the reminder's "▶️ Start reviewing" button go
// through this.
func (b *Bot) sendNextTrain(ctx context.Context, s Session, user storage.User) error {
	p, err := b.trainerV2.NextExerciseV2(ctx, user.ID)
	if err != nil {
		return err
	}
	return b.sendPromptV2(s, user, p)
}

// sendNextPractice sends the next practice exercise for user via
// TrainerV2's v2 practice pool (across enabled+tier-unlocked topics) — the
// legacy unscheduled-practice fallback is gone.
func (b *Bot) sendNextPractice(ctx context.Context, s Session, user storage.User) error {
	p, err := b.trainerV2.NextPracticeV2(ctx, user.ID)
	if err != nil {
		return err
	}
	return b.sendPromptV2(s, user, p)
}

// sendPromptV2 renders a PromptV2 result, mirroring sendNextResult's
// Kind switch for the v2 exercise path.
func (b *Bot) sendPromptV2(s Session, user storage.User, p PromptV2) error {
	switch p.Kind {
	case PromptV2KindExercise:
		return b.sendExerciseV2(s, p)
	case PromptV2KindNothingDue:
		if !p.DueAt.IsZero() {
			loc := locationFor(user)
			return s.Send(fmt.Sprintf("Nothing due right now. Come back at %s.", p.DueAt.In(loc).Format("15:04")))
		}
		return s.Send(allCaughtUpText)
	case PromptV2KindNoContent:
		return s.Send(noContentText)
	case PromptV2KindNoTopics:
		return s.Send(noTopicsText)
	default:
		return s.Send(fallbackText)
	}
}

// sendExerciseV2 sends one ready PromptV2: a photo-from-birth or text
// message, with option buttons for ModeSingle/ModeSet or a bare "type your
// answer" prompt (no buttons) for ModeText. A practice exercise (p.Practice)
// gets a trailing "⏹ Stop practice" control, same as the legacy /practice
// prompt.
func (b *Bot) sendExerciseV2(s Session, p PromptV2) error {
	text := p.Text
	var rows [][]Btn
	if p.Mode == quiz.ModeText {
		text += "\n\n✏️ Type your answer."
	} else {
		rows = optionRowsV2(p.ExerciseID, p.Options, p.Practice)
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

// optionRowsV2 lays out a PromptV2's options two per row (a trailing odd
// option sits alone on the last row), mirroring buttonRows' layout for the
// legacy exercise path. practice selects which callback prefix each
// option's Data carries (v2p: vs v2a:), so handleCallback's routing alone
// decides whether grading advances via sendNextPractice or sendNextTrain.
func optionRowsV2(exerciseID uuid.UUID, options []OptionV2, practice bool) [][]Btn {
	if len(options) == 0 {
		return nil
	}
	callbackData := v2AnswerCallbackData
	if practice {
		callbackData = v2PracticeCallbackData
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

// ── v2a: / v2p: callbacks (button answer) ───────────────────────────────

// handleV2AnswerCallback grades one v2a: tap (a /train exercise) via
// TrainerV2.AnswerV2, edits the exercise in place, toasts the result, and
// sends the next due exercise.
func (b *Bot) handleV2AnswerCallback(ctx context.Context, s Session, data string) error {
	if b.trainerV2 == nil {
		return s.Respond("")
	}
	exerciseID, index, ok := parseV2AnswerCallback(data)
	if !ok {
		return s.Respond("")
	}
	return b.answerV2AndAdvance(ctx, s, exerciseID, index, (*Bot).sendNextTrain)
}

// handleV2PracticeAnswerCallback grades one v2p: tap (a /practice exercise)
// through the SAME AnswerV2 grading path as v2a: — the exercise row already
// carries practice=true (set at NextPracticeV2 time), so AnswerV2's
// underlying finishAnswer already knows to skip FSRS movement without this
// callback saying so again — then sends the next practice exercise instead
// of the next due one.
func (b *Bot) handleV2PracticeAnswerCallback(ctx context.Context, s Session, data string) error {
	if b.trainerV2 == nil {
		return s.Respond("")
	}
	exerciseID, index, ok := parseV2PracticeCallback(data)
	if !ok {
		return s.Respond("")
	}
	return b.answerV2AndAdvance(ctx, s, exerciseID, index, (*Bot).sendNextPractice)
}

// answerV2AndAdvance is the shared grading flow behind both v2a: and v2p:
// taps: grade via AnswerV2, edit the exercise message/caption in place,
// toast the result, then advance via next (sendNextTrain or
// sendNextPractice, picked by the caller per the tapped prefix).
func (b *Bot) answerV2AndAdvance(ctx context.Context, s Session, exerciseID uuid.UUID, index int, next func(*Bot, context.Context, Session, storage.User) error) error {
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	res, err := b.trainerV2.AnswerV2(ctx, user.ID, exerciseID, index)
	if err != nil {
		return err
	}
	if res.Stale {
		return s.Respond(staleToast)
	}

	b.applyV2AnswerEdit(s, res)
	if err := s.Respond(answerToastV2(res.Correct)); err != nil {
		return err
	}
	return next(b, ctx, s, user)
}

// ── free-typed answers (telebot.OnText) ─────────────────────────────────

// handleText routes a plain-text message to TrainerV2.AnswerText when
// TrainerV2 is wired and the message isn't a command (telebot still
// dispatches OnText for an UNREGISTERED "/foo" command after its command
// match fails — see telebot's update.go ProcessContext — so this guard is
// required, not defensive fluff). Grading, then sending the next exercise,
// mirrors handleV2AnswerCallback exactly, except there is no callback to
// Respond to: the toast becomes a plain follow-up message.
func (b *Bot) handleText(ctx context.Context, s Session) error {
	if b.trainerV2 == nil {
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
	res, ok, err := b.trainerV2.AnswerText(ctx, user.ID, text)
	if err != nil {
		return err
	}
	if !ok {
		return nil // no open ModeText exercise — leave the message alone
	}
	if res.Stale {
		return s.Send(staleToast)
	}

	b.applyV2AnswerEdit(s, res)
	if err := s.Send(answerToastV2(res.Correct)); err != nil {
		return err
	}
	// A free-typed reply carries no callback prefix to tell a practice
	// answer from a scheduled one, unlike v2a:/v2p: — res.Practice (echoed
	// from the graded exercise's own practice flag) is what decides here.
	if res.Practice {
		return b.sendNextPractice(ctx, s, user)
	}
	return b.sendNextTrain(ctx, s, user)
}

// ── shared grading render ────────────────────────────────────────────────

// applyV2AnswerEdit edits the exercise message/caption in place to res's
// graded re-render: EditCaption for a photo exercise, EditMessage for text
// — mirroring PromptV2's own Text/MediaPath split. Falls back to the
// session's current message id when res doesn't carry its own (the same
// HasMessage fallback handleAnswerCallback uses for the legacy path). Edit
// failures are logged, not fatal — grading has already happened.
func (b *Bot) applyV2AnswerEdit(s Session, res AnswerResultV2) {
	msgID := s.MessageID()
	if res.HasMessage {
		msgID = res.MessageID
	}
	rows := gradedOptionRowsV2(res.Options)
	text := html.EscapeString(res.Text)

	var err error
	if res.MediaPath != "" {
		err = s.EditCaption(msgID, text, rows)
	} else {
		err = s.EditMessage(msgID, text, rows)
	}
	if err != nil {
		b.logger.Warn("telegram: edit v2 answer", "error", err)
	}
}

// gradedOptionRowsV2 lays out graded v2 options two per row, decorated with
// ✅/❌ (DecorateLabel) and wired to the inert noop callback.
func gradedOptionRowsV2(options []GradedOptionV2) [][]Btn {
	if len(options) == 0 {
		return nil
	}
	graded := func(o GradedOptionV2) Btn {
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

// answerToastV2 picks the correct/wrong toast text, shared by the callback
// and free-text answer paths.
func answerToastV2(correct bool) string {
	if correct {
		return correctToast
	}
	return wrongToast
}
