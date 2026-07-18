package telegram

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
)

// dataStudyStart / dataStudyNext are the "study:" callback payloads
// (architecture §5.4): both launch/continue the /study flow. dataStudyStart
// is the reminder loop's "✨ Introduce new" button; dataStudyNext is the
// architecture doc's reserved "fetch another" affordance — both route to
// the same handler today (see handleCallback in handlers.go).
const (
	dataStudyStart = "study:start"
	dataStudyNext  = "study:next"
)

// studyDormantText is what /study and /introduce reply with when
// Config.StudyService is nil (no wave-4 wiring yet) — the same nil-safe
// convention every optional v2 command follows.
const studyDormantText = "🚧 /study is coming with v2 wiring."

// ── /study, /introduce ───────────────────────────────────────────────────

// handleStudy serves /study and its /introduce alias: both pull the next
// introduction card (architecture §5.1) and send it as a new message.
func (b *Bot) handleStudy(ctx context.Context, s Session) error {
	if b.study == nil {
		return s.Send(studyDormantText)
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	card, err := b.study.NextIntro(ctx, user.ID)
	if err != nil {
		return err
	}
	return b.sendIntroCard(ctx, s, card)
}

// handleStudyCallback runs the /study flow from a callback button (the
// reminder loop's "✨ Introduce new", or study:next): it acks the tap and
// sends the next intro card, mirroring handleStartTrainCallback's shape for
// /train's "▶️ Start reviewing".
func (b *Bot) handleStudyCallback(ctx context.Context, s Session) error {
	if err := s.Respond(""); err != nil {
		return err
	}
	return b.handleStudy(ctx, s)
}

// sendIntroCard renders one IntroCard: a photo-from-birth or text card with
// its three outcome buttons when Reason == IntroOK, otherwise a plain
// closer message (nothing left to introduce, or today's budget spent).
func (b *Bot) sendIntroCard(ctx context.Context, s Session, card IntroCard) error {
	if card.Reason != IntroOK {
		return s.Send(introCloserText(card))
	}
	media := card.MediaPath != ""
	rows := introButtonRows(card.IntroID, media)
	if media {
		// SendPhoto is ModeHTML (see its Session doc comment) — escape like
		// any other dynamic text going through an HTML-parsed send/edit.
		_, err := s.SendPhoto(card.MediaPath, html.EscapeString(card.Text), rows)
		return err
	}
	_, err := s.SendKeyboard(card.Text, rows)
	return err
}

// introCloserText renders the terminal IntroCard states: nothing left to
// introduce right now, or today's daily cap already spent.
func introCloserText(card IntroCard) string {
	if card.Reason == IntroBudgetExhausted {
		return fmt.Sprintf("✨ Today's intro budget is spent — introduced %d today. "+
			"More tomorrow, or /train to review what you've got.", card.IntroducedToday)
	}
	return "🎉 Nothing new to introduce right now — /train to review, or check /topics for what's still locked."
}

// introButtonRows renders an intro card's three outcome buttons on one row,
// per architecture §5.1's mock: [✅ Got it] [🧠 I know this] [🎯 Test me].
func introButtonRows(introID uuid.UUID, media bool) [][]Btn {
	return [][]Btn{{
		{Label: "✅ Got it", Data: introCallbackData(introID, engram.IntroGotIt, media)},
		{Label: "🧠 I know this", Data: introCallbackData(introID, engram.IntroKnown, media)},
		{Label: "🎯 Test me", Data: introCallbackData(introID, engram.IntroTestMe, media)},
	}}
}

// ── intro: callback ──────────────────────────────────────────────────────

// handleIntroCallback grades one intro-card tap: it applies the outcome via
// StudyService.AnswerIntro, edits the card in place to the confirmation
// (EditCaption for a photo card, EditMessage for a text card — the media
// flag travels in the callback data itself, see parseIntroCallback), acks
// the tap, then sends the next card (or a closer) as a new message.
func (b *Bot) handleIntroCallback(ctx context.Context, s Session, data string) error {
	if b.study == nil {
		return s.Respond("")
	}
	introID, outcome, media, ok := parseIntroCallback(data)
	if !ok {
		return s.Respond("")
	}

	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	ack, err := b.study.AnswerIntro(ctx, user.ID, introID, outcome)
	if err != nil {
		return err
	}

	text := html.EscapeString(ack.Text)
	msgID := s.MessageID()
	var editErr error
	if media {
		editErr = s.EditCaption(msgID, text, nil)
	} else {
		editErr = s.EditMessage(msgID, text, nil)
	}
	if editErr != nil {
		b.logger.Warn("telegram: edit intro confirmation", "error", editErr)
	}

	if err := s.Respond(""); err != nil {
		return err
	}

	card, err := b.study.NextIntro(ctx, user.ID)
	if err != nil {
		return err
	}
	return b.sendIntroCard(ctx, s, card)
}

// introOutcomeChar/parseIntroOutcomeChar round-trip engram.IntroOutcome
// through the single character carried in an intro: callback.
func introOutcomeChar(o engram.IntroOutcome) byte {
	switch o {
	case engram.IntroKnown:
		return 'k'
	case engram.IntroTestMe:
		return 't'
	default: // engram.IntroGotIt
		return 'g'
	}
}

func parseIntroOutcomeChar(c byte) (engram.IntroOutcome, bool) {
	switch c {
	case 'g':
		return engram.IntroGotIt, true
	case 'k':
		return engram.IntroKnown, true
	case 't':
		return engram.IntroTestMe, true
	default:
		return 0, false
	}
}

// introCallbackData builds one intro-card button's payload:
// "intro:<uuid>:<g|k|t>:<0|1>". The trailing media flag lets
// handleIntroCallback choose EditCaption vs EditMessage for the
// confirmation edit without any extra server-side state. Budget:
// "intro:"(6) + uuid(36) + ":"(1) + outcome(1) + ":"(1) + media(1) = 46,
// comfortably under Telegram's 64-byte callback_data cap.
func introCallbackData(introID uuid.UUID, outcome engram.IntroOutcome, media bool) string {
	m := "0"
	if media {
		m = "1"
	}
	return "intro:" + introID.String() + ":" + string(introOutcomeChar(outcome)) + ":" + m
}

// parseIntroCallback parses a payload built by introCallbackData. ok is
// false for anything malformed (unknown outcome/media char, bad uuid, wrong
// shape) — callers treat that like the existing unknown-callback path (an
// inert Respond("")).
func parseIntroCallback(data string) (introID uuid.UUID, outcome engram.IntroOutcome, media, ok bool) {
	rest, hasPrefix := strings.CutPrefix(data, "intro:")
	if !hasPrefix {
		return uuid.UUID{}, 0, false, false
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 {
		return uuid.UUID{}, 0, false, false
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.UUID{}, 0, false, false
	}
	if len(parts[1]) != 1 {
		return uuid.UUID{}, 0, false, false
	}
	oc, ok := parseIntroOutcomeChar(parts[1][0])
	if !ok {
		return uuid.UUID{}, 0, false, false
	}
	switch parts[2] {
	case "0":
		return id, oc, false, true
	case "1":
		return id, oc, true, true
	default:
		return uuid.UUID{}, 0, false, false
	}
}
