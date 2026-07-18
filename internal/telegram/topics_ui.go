package telegram

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/train"
)

// dataTopicsRoot is the "top:root" callback payload (architecture §5.4):
// go back to the top-level topic listing.
const dataTopicsRoot = "top:root"

// topicsDormantText is what /topics replies with when Config.TopicService
// is nil (no wave-4 wiring yet).
const topicsDormantText = "🚧 /topics is coming with v2 wiring."

// topicsRootText is the static header sent with the root topic listing.
const topicsRootText = "🌍 Topics — tap a row to drill in."

// ── /topics ──────────────────────────────────────────────────────────────

// handleTopics serves /topics: it sends the top-level topic listing as a
// new message (architecture §5.2).
func (b *Bot) handleTopics(ctx context.Context, s Session) error {
	if b.topics == nil {
		return s.Send(topicsDormantText)
	}
	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	rows, err := b.topics.Root(ctx, user.ID)
	if err != nil {
		return err
	}
	_, err = s.SendKeyboard(topicsRootText, rootTopicRows(rows))
	return err
}

// ── top: callback ────────────────────────────────────────────────────────

// topicNavKind is the parsed shape of a "top:" callback (architecture
// §5.4): drill into a child, go up to a known parent, or jump to the root
// listing.
type topicNavKind int8

const (
	topicNavDrill topicNavKind = iota // top:<uuid>
	topicNavUp                        // top:up:<uuid>
	topicNavRoot                      // top:root
)

// topicNav is a parsed "top:" callback payload.
type topicNav struct {
	kind   topicNavKind
	target uuid.UUID // meaningful for topicNavDrill/topicNavUp
}

// parseTopicNav parses one of "top:root", "top:up:<uuid>", or "top:<uuid>".
// ok is false for anything malformed — callers treat that like the existing
// unknown-callback path (an inert Respond("")).
func parseTopicNav(data string) (topicNav, bool) {
	if data == dataTopicsRoot {
		return topicNav{kind: topicNavRoot}, true
	}
	if rest, ok := strings.CutPrefix(data, "top:up:"); ok {
		id, err := uuid.Parse(rest)
		if err != nil {
			return topicNav{}, false
		}
		return topicNav{kind: topicNavUp, target: id}, true
	}
	if rest, ok := strings.CutPrefix(data, "top:"); ok {
		id, err := uuid.Parse(rest)
		if err != nil {
			return topicNav{}, false
		}
		return topicNav{kind: topicNavDrill, target: id}, true
	}
	return topicNav{}, false
}

// handleTopicCallback re-renders the /topics message in place for a "top:"
// tap: drilling into a topic, going up to a parent, or back to the root
// listing all edit the SAME message (mirroring rerenderDeckPicker /
// rerenderSettings), so browsing the tree never spams new messages.
func (b *Bot) handleTopicCallback(ctx context.Context, s Session, data string) error {
	if b.topics == nil {
		return s.Respond("")
	}
	nav, ok := parseTopicNav(data)
	if !ok {
		return s.Respond("")
	}

	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}

	var (
		text string
		rows [][]Btn
	)
	if nav.kind == topicNavRoot {
		rootRows, err := b.topics.Root(ctx, user.ID)
		if err != nil {
			return err
		}
		text, rows = topicsRootText, rootTopicRows(rootRows)
	} else {
		view, err := b.topics.Children(ctx, user.ID, nav.target)
		if err != nil {
			return err
		}
		text, rows = topicsBodyText(view), topicsViewRows(view)
	}

	if err := s.EditMessage(s.MessageID(), text, rows); err != nil {
		b.logger.Warn("telegram: edit topics view", "error", err)
	}
	return s.Respond("")
}

// ── pure rendering ───────────────────────────────────────────────────────

// rootTopicRows renders one row per root TopicRow, drilling in via
// "top:<uuid>".
func rootTopicRows(rows []TopicRow) [][]Btn {
	out := make([][]Btn, 0, len(rows))
	for _, r := range rows {
		out = append(out, []Btn{{Label: topicRowLabel(r), Data: "top:" + r.TopicID.String()}})
	}
	return out
}

// topicsBodyText renders a TopicView's header: the breadcrumb (HTML-escaped
// — this is sent via EditMessage's ModeHTML) plus a static instruction line.
func topicsBodyText(view TopicView) string {
	var b strings.Builder
	b.WriteString("🌍 ")
	for i, c := range view.Breadcrumb {
		if i > 0 {
			b.WriteString(" ▸ ")
		}
		b.WriteString(html.EscapeString(c.Name))
	}
	b.WriteString("\n(tap a row to drill in)")
	return b.String()
}

// topicsViewRows renders a TopicView's body rows: child topics (container)
// or per-tier progress (quizzable, non-interactive — noop), plus a trailing
// ⬆️ navigation row.
func topicsViewRows(view TopicView) [][]Btn {
	rows := make([][]Btn, 0, len(view.Children)+len(view.Tiers)+1)
	if view.IsQuizzable {
		for _, t := range view.Tiers {
			rows = append(rows, []Btn{{Label: tierRowLabel(t), Data: train.DataNoop}})
		}
	} else {
		for _, c := range view.Children {
			rows = append(rows, []Btn{{Label: topicRowLabel(c), Data: "top:" + c.TopicID.String()}})
		}
	}
	rows = append(rows, []Btn{topicNavButton(view)})
	return rows
}

// topicNavButton is the ⬆️ row: back to the parent topic's view, or to the
// root listing when this topic itself has no parent.
func topicNavButton(view TopicView) Btn {
	if view.ParentID == nil {
		return Btn{Label: "⬆️ All topics", Data: dataTopicsRoot}
	}
	return Btn{Label: "⬆️ Up", Data: "top:up:" + view.ParentID.String()}
}

// topicRowLabel renders one topic row: name, aggregate progress (when this
// topic has any items), a 🔒 badge for its lowest locked tier, and a 💡
// badge when a TipProvider exists — architecture §5.2's
// "▸ Languages   tier: 42/50 · introduced 48/50" mock, folded into a single
// button label (Telegram button text is never markup-parsed, so no
// escaping is needed here, unlike topicsBodyText).
func topicRowLabel(row TopicRow) string {
	var b strings.Builder
	b.WriteString(row.Name)
	if row.Total > 0 {
		fmt.Fprintf(&b, "  %d/%d introduced · %d good", row.Introduced, row.Total, row.GoodShape)
	}
	if row.AnyLocked {
		fmt.Fprintf(&b, " · 🔒 tier %d", row.LockedTier)
	}
	if row.HasTips {
		b.WriteString(" 💡")
	}
	return b.String()
}

// tierRowLabel renders one per-tier progress line for a quizzable topic.
func tierRowLabel(t TierRow) string {
	lock := ""
	if t.Locked {
		lock = " 🔒"
	}
	return fmt.Sprintf("Tier %d: %d/%d introduced · %d good%s", t.Tier, t.Introduced, t.Total, t.GoodShape, lock)
}
