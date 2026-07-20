package telegram

import (
	"context"
	"strings"
)

// FeedbackReporter files a user's feedback into an external issue tracker
// (snagbox, via internal/feedback). It is OPTIONAL and nil-safe: with no
// reporter wired, /feedback replies that feedback isn't available right now,
// the same "🚧 coming soon" posture every other optional dependency takes.
// The concrete reporter keeps the snagbox ingest token server-side
// ([[snagbox-integration]]); this package never sees the credential.
type FeedbackReporter interface {
	// ReportFeedback files text (with optional meta) as one issue. It returns
	// only an error — geodrill writes, it never reads the queue back.
	ReportFeedback(ctx context.Context, text string, meta map[string]any) error
}

// user-facing /feedback copy.
const (
	feedbackUnavailableText = "🚧 Feedback isn't available right now — try again later."
	feedbackUsageText       = "Tell me what's up and I'll pass it on. Send it inline, e.g.\n\n" +
		"/feedback the flags quiz showed the wrong country"
	feedbackThanksText = "🙏 Thanks — your feedback has been filed."
	feedbackFailedText = "😕 Couldn't file that just now. Please try again in a moment."
)

// handleFeedback serves /feedback: it files the argument text into snagbox via
// the wired FeedbackReporter so Aurora's snagbox agent can drain it into the
// vault later ([[snagbox-integration]]). The note is taken as the command's
// inline argument (s.Data(), the payload after "/feedback ") rather than a
// follow-up message, because the free-text OnText handler already owns plain
// messages for answer grading — a stateful "now type your feedback" flow would
// collide with an open exercise. A missing reporter or empty text replies with
// guidance; a report failure is logged and surfaced as a soft error, never
// fatal to the user (the guideline's "report failures are logged, not fatal"
// rule).
func (b *Bot) handleFeedback(ctx context.Context, s Session) error {
	if b.feedback == nil {
		return s.Send(feedbackUnavailableText)
	}

	text := strings.TrimSpace(s.Data())
	if text == "" {
		return s.Send(feedbackUsageText)
	}

	meta := map[string]any{"tg_user_id": s.UserID()}
	if username := s.Username(); username != "" {
		meta["tg_username"] = username
	}

	if err := b.feedback.ReportFeedback(ctx, text, meta); err != nil {
		b.logger.Error("telegram: report feedback", "error", err)
		return s.Send(feedbackFailedText)
	}
	return s.Send(feedbackThanksText)
}
