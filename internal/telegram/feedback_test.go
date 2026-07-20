package telegram

import (
	"context"
	"errors"
	"testing"
)

// fakeReporter records ReportFeedback calls and can be told to fail, so
// handleFeedback's file/reply/degrade paths are testable without a network.
type fakeReporter struct {
	calls   []reportCall
	failErr error
}

type reportCall struct {
	text string
	meta map[string]any
}

func (f *fakeReporter) ReportFeedback(ctx context.Context, text string, meta map[string]any) error {
	f.calls = append(f.calls, reportCall{text: text, meta: meta})
	return f.failErr
}

// TestHandleFeedback_Files covers the happy path: the argument text is filed
// via the reporter, meta carries the sender's identity, and the user gets a
// thank-you.
func TestHandleFeedback_Files(t *testing.T) {
	b := newTestBot(&stubStore{})
	fr := &fakeReporter{}
	b.feedback = fr
	s := &fakeSession{userID: 42, username: "aurora", data: "the flags quiz showed the wrong country"}

	if err := b.handleFeedback(context.Background(), s); err != nil {
		t.Fatalf("handleFeedback: %v", err)
	}

	if len(fr.calls) != 1 {
		t.Fatalf("expected 1 report, got %d", len(fr.calls))
	}
	call := fr.calls[0]
	if call.text != "the flags quiz showed the wrong country" {
		t.Fatalf("unexpected filed text: %q", call.text)
	}
	if call.meta["tg_user_id"] != int64(42) {
		t.Fatalf("expected tg_user_id 42 in meta, got %v", call.meta["tg_user_id"])
	}
	if call.meta["tg_username"] != "aurora" {
		t.Fatalf("expected tg_username in meta, got %v", call.meta["tg_username"])
	}
	if len(s.sent) != 1 || s.sent[0] != feedbackThanksText {
		t.Fatalf("expected thanks reply, got %v", s.sent)
	}
}

// TestHandleFeedback_TrimsAndDropsEmptyUsername covers meta hygiene: leading/
// trailing whitespace is trimmed off the note, and an absent @username is
// simply omitted from meta rather than filed as an empty string.
func TestHandleFeedback_TrimsAndDropsEmptyUsername(t *testing.T) {
	b := newTestBot(&stubStore{})
	fr := &fakeReporter{}
	b.feedback = fr
	s := &fakeSession{userID: 7, data: "   spacing bug   "}

	if err := b.handleFeedback(context.Background(), s); err != nil {
		t.Fatalf("handleFeedback: %v", err)
	}
	if len(fr.calls) != 1 || fr.calls[0].text != "spacing bug" {
		t.Fatalf("expected trimmed text, got %+v", fr.calls)
	}
	if _, ok := fr.calls[0].meta["tg_username"]; ok {
		t.Fatalf("expected no tg_username in meta when username is empty")
	}
}

// TestHandleFeedback_EmptyText prompts for usage (and files nothing) when
// /feedback is sent with no argument.
func TestHandleFeedback_EmptyText(t *testing.T) {
	b := newTestBot(&stubStore{})
	fr := &fakeReporter{}
	b.feedback = fr
	s := &fakeSession{userID: 1, data: "   "}

	if err := b.handleFeedback(context.Background(), s); err != nil {
		t.Fatalf("handleFeedback: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected nothing filed for empty text, got %+v", fr.calls)
	}
	if len(s.sent) != 1 || s.sent[0] != feedbackUsageText {
		t.Fatalf("expected usage reply, got %v", s.sent)
	}
}

// TestHandleFeedback_Unavailable degrades to a "not available" reply (and
// files nothing) when no reporter is wired — the nil-safe posture every
// optional dependency takes.
func TestHandleFeedback_Unavailable(t *testing.T) {
	b := newTestBot(&stubStore{})
	s := &fakeSession{userID: 1, data: "anything"}

	if err := b.handleFeedback(context.Background(), s); err != nil {
		t.Fatalf("handleFeedback: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != feedbackUnavailableText {
		t.Fatalf("expected unavailable reply, got %v", s.sent)
	}
}

// TestHandleFeedback_ReportFailureIsSoft covers the guideline's "report
// failures are logged, not fatal" rule: a reporter error surfaces a soft
// apology to the user, not a returned error that would escape to the poller.
func TestHandleFeedback_ReportFailureIsSoft(t *testing.T) {
	b := newTestBot(&stubStore{})
	b.feedback = &fakeReporter{failErr: errors.New("snagbox down")}
	s := &fakeSession{userID: 1, data: "the button is broken"}

	if err := b.handleFeedback(context.Background(), s); err != nil {
		t.Fatalf("handleFeedback should not return the report error: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != feedbackFailedText {
		t.Fatalf("expected soft failure reply, got %v", s.sent)
	}
}
