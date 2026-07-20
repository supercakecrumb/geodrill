// Package feedback adapts the snagbox issue-intake client
// (github.com/supercakecrumb/snagbox/client) to the small ReportFeedback
// surface geodrill's Telegram layer needs, so the /feedback command can file
// a user's note into geodrill's snagbox project without importing the SDK
// directly ([[snagbox-integration]]).
//
// The snagbox ingest token is write-only and scoped to geodrill's one
// project: it can file issues but cannot read the queue or touch any other
// project. Even so it's a bearer credential — this package is only ever
// constructed on the server (cmd/bot), never shipped to a client.
package feedback

import (
	"context"

	"github.com/supercakecrumb/snagbox/client"
)

// Reporter files geodrill feedback into snagbox over the ingest API.
type Reporter struct {
	sb *client.Client
}

// New builds a Reporter for a snagbox base URL and ingest token. It performs
// no network I/O — construction is cheap and reusable, so the caller builds
// one at startup and shares it (the same one-client-per-process pattern the
// snagbox INTEGRATION guide prescribes).
func New(baseURL, token string) *Reporter {
	return &Reporter{sb: client.New(baseURL, token)}
}

// ReportFeedback files text (with optional meta) as one issue in geodrill's
// snagbox project. The created issue is discarded — geodrill only writes; a
// snagbox agent drains the queue later — so this returns just the error,
// matching the telegram package's FeedbackReporter interface.
func (r *Reporter) ReportFeedback(ctx context.Context, text string, meta map[string]any) error {
	_, err := r.sb.ReportIssue(ctx, client.ReportRequest{Text: text, Meta: meta})
	return err
}
