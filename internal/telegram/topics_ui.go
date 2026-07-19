package telegram

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/google/uuid"
)

// dataTopicsRoot is the "top:root" callback payload (architecture §5.4):
// go back to the top-level topic listing.
const dataTopicsRoot = "top:root"

// topicsDormantText is what /topics replies with when Config.TopicService
// is nil (no wave-4 wiring yet).
const topicsDormantText = "🚧 /topics is coming soon."

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

// ── topen:/topoff: callback (enable/disable toggle) ─────────────────────

// dataTopicEnablePrefix / dataTopicDisablePrefix are the callback prefixes
// for a quizzable TopicView's enable/disable toggle: "topen:<topic-uuid>" /
// "topoff:<topic-uuid>" — the /topics counterpart of the retired /decks'
// "deck:<uuid>" toggle.
const (
	dataTopicEnablePrefix  = "topen:"
	dataTopicDisablePrefix = "topoff:"
)

// topicToggleCallbackData builds one toggle row's payload: enable=true
// requests topen:, enable=false requests topoff:.
func topicToggleCallbackData(topicID uuid.UUID, enable bool) string {
	prefix := dataTopicDisablePrefix
	if enable {
		prefix = dataTopicEnablePrefix
	}
	return prefix + topicID.String()
}

// parseTopicToggleCallback parses a payload built by
// topicToggleCallbackData. ok is false for anything malformed.
func parseTopicToggleCallback(data string) (topicID uuid.UUID, enable bool, ok bool) {
	if rest, has := strings.CutPrefix(data, dataTopicEnablePrefix); has {
		id, err := uuid.Parse(rest)
		if err != nil {
			return uuid.UUID{}, false, false
		}
		return id, true, true
	}
	if rest, has := strings.CutPrefix(data, dataTopicDisablePrefix); has {
		id, err := uuid.Parse(rest)
		if err != nil {
			return uuid.UUID{}, false, false
		}
		return id, false, true
	}
	return uuid.UUID{}, false, false
}

// ── topen:g:/topoff:g: callback (group-level enable/disable toggle) ─────

// dataTopicGroupEnablePrefix / dataTopicGroupDisablePrefix are the callback
// prefixes for a container TopicView's group-level toggle:
// "topen:g:<topic-uuid>" / "topoff:g:<topic-uuid>". These deliberately
// nest under the same "topen:"/"topoff:" prefixes the single-topic toggle
// uses (dataTopicEnablePrefix/dataTopicDisablePrefix above) — the callback
// router already dispatches anything starting with "topen:"/"topoff:" to
// handleTopicToggle, so the group toggle rides that same wiring instead of
// needing a new callback prefix registered elsewhere; handleTopicToggle
// below tells the two apart by trying the more specific "g:" parse first.
const (
	dataTopicGroupEnablePrefix  = "topen:g:"
	dataTopicGroupDisablePrefix = "topoff:g:"
)

// topicGroupToggleCallbackData builds one group-toggle button's payload:
// enable=true requests topen:g:, enable=false requests topoff:g:.
func topicGroupToggleCallbackData(topicID uuid.UUID, enable bool) string {
	prefix := dataTopicGroupDisablePrefix
	if enable {
		prefix = dataTopicGroupEnablePrefix
	}
	return prefix + topicID.String()
}

// parseTopicGroupToggleCallback parses a payload built by
// topicGroupToggleCallbackData. ok is false for anything malformed
// (including a plain topen:/topoff: single-topic toggle, which lacks the
// "g:" marker).
func parseTopicGroupToggleCallback(data string) (topicID uuid.UUID, enable bool, ok bool) {
	if rest, has := strings.CutPrefix(data, dataTopicGroupEnablePrefix); has {
		id, err := uuid.Parse(rest)
		if err != nil {
			return uuid.UUID{}, false, false
		}
		return id, true, true
	}
	if rest, has := strings.CutPrefix(data, dataTopicGroupDisablePrefix); has {
		id, err := uuid.Parse(rest)
		if err != nil {
			return uuid.UUID{}, false, false
		}
		return id, false, true
	}
	return uuid.UUID{}, false, false
}

// handleTopicToggle applies a topen:/topoff: tap: either the single-topic
// toggle (flips one topic's user_topics.enabled via
// TopicService.SetTopicEnabled) or, when the payload carries the "g:"
// marker, the container's group-level toggle (flips EVERY quizzable topic
// in the subtree via TopicService.SetSubtreeEnabled) — either way the SAME
// topic view is re-rendered in place afterward (mirroring
// handleTopicCallback's edit-in-place pattern) so the toggle row(s) and
// their ✅/⬜ prefixes reflect the new state immediately.
func (b *Bot) handleTopicToggle(ctx context.Context, s Session, data string) error {
	if b.topics == nil {
		return s.Respond("")
	}

	if topicID, enable, ok := parseTopicGroupToggleCallback(data); ok {
		user, err := b.loadOrCreateUser(ctx, s)
		if err != nil {
			return err
		}
		if err := b.topics.SetSubtreeEnabled(ctx, user.ID, topicID, enable); err != nil {
			return err
		}
		return b.rerenderTopicView(ctx, s, user.ID, topicID)
	}

	topicID, enable, ok := parseTopicToggleCallback(data)
	if !ok {
		return s.Respond("")
	}

	user, err := b.loadOrCreateUser(ctx, s)
	if err != nil {
		return err
	}
	if err := b.topics.SetTopicEnabled(ctx, user.ID, topicID, enable); err != nil {
		return err
	}
	return b.rerenderTopicView(ctx, s, user.ID, topicID)
}

// rerenderTopicView re-fetches topicID's TopicView and edits the current
// message in place — the shared closer for both handleTopicToggle branches.
func (b *Bot) rerenderTopicView(ctx context.Context, s Session, userID, topicID uuid.UUID) error {
	view, err := b.topics.Children(ctx, userID, topicID)
	if err != nil {
		return err
	}
	if err := s.EditMessage(s.MessageID(), topicsBodyText(view), topicsViewRows(view)); err != nil {
		b.logger.Warn("telegram: edit topics view after toggle", "error", err)
	}
	return s.Respond("")
}

// handleTopicCallback re-renders the /topics message in place for a "top:"
// tap: drilling into a topic, going up to a parent, or back to the root
// listing all edit the SAME message (mirroring rerenderSettings), so
// browsing the tree never spams new messages.
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
// "top:<uuid>", plus a trailing «⬅️ Menu» row back to the hub. The root
// listing is where the ⬆️ "Up" navigation drilled-in views carry (see
// topicNavButton) would otherwise leave the tree entirely with no further
// way back — the hub-and-spoke rule closes that gap here.
func rootTopicRows(rows []TopicRow) [][]Btn {
	out := make([][]Btn, 0, len(rows)+1)
	for _, r := range rows {
		out = append(out, []Btn{{Label: topicRowLabel(r), Data: "top:" + r.TopicID.String()}})
	}
	out = append(out, []Btn{{Label: "⬅️ Menu", Data: dataMenuOpen}})
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
// or per-tier progress plus an enable/disable toggle (quizzable — the
// /topics counterpart of the retired /decks' per-deck toggle), plus a
// trailing navigation row. A container view (IsQuizzable == false) with at
// least one quizzable descendant (GroupTotalLeaves > 0) gets its
// group-level toggle button prepended to that trailing row, to the LEFT of
// «⬆️ Up» — [groupToggle][Up] — so turning off a whole group no longer
// means toggling every subtopic by hand.
func topicsViewRows(view TopicView) [][]Btn {
	rows := make([][]Btn, 0, len(view.Children)+len(view.Tiers)+2)
	if view.IsQuizzable {
		for _, t := range view.Tiers {
			rows = append(rows, []Btn{{Label: tierRowLabel(t), Data: DataNoop}})
		}
		rows = append(rows, []Btn{topicToggleButton(view)})
		rows = append(rows, []Btn{topicNavButton(view)})
		return rows
	}

	for _, c := range view.Children {
		rows = append(rows, []Btn{{Label: topicRowLabel(c), Data: "top:" + c.TopicID.String()}})
	}
	var lastRow []Btn
	if view.GroupTotalLeaves > 0 {
		lastRow = append(lastRow, groupToggleButton(view))
	}
	lastRow = append(lastRow, topicNavButton(view))
	rows = append(rows, lastRow)
	return rows
}

// topicToggleButton renders a quizzable TopicView's enable/disable row: tap
// to flip user_topics.enabled in place (architecture: /decks' per-deck
// on/off affordance retired onto /topics, so this is the ONLY place a
// quizzable topic's enabled flag can be changed from now on).
func topicToggleButton(view TopicView) Btn {
	if view.Enabled {
		return Btn{Label: "✅ Enabled — tap to disable", Data: topicToggleCallbackData(view.TopicID, false)}
	}
	return Btn{Label: "⬜ Disabled — tap to enable", Data: topicToggleCallbackData(view.TopicID, true)}
}

// groupToggleButton renders a container TopicView's group-level toggle row
// (task 2026-07-19: turning off a whole group of quizzable topics without
// toggling every subtopic by hand): if ANY descendant is currently enabled
// (GroupEnabledLeaves > 0 — covers both "all on" and "mixed"), tapping
// turns the WHOLE subtree off; only once every descendant is already off
// does it flip to turning the whole subtree back on. Only rendered when
// GroupTotalLeaves > 0 (topicsViewRows' caller).
func groupToggleButton(view TopicView) Btn {
	if view.GroupEnabledLeaves > 0 {
		return Btn{Label: "🔕 Turn group off", Data: topicGroupToggleCallbackData(view.TopicID, false)}
	}
	return Btn{Label: "🔔 Turn group on", Data: topicGroupToggleCallbackData(view.TopicID, true)}
}

// topicNavButton is the ⬆️ row: back to the parent topic's view, or to the
// root listing when this topic itself has no parent.
func topicNavButton(view TopicView) Btn {
	if view.ParentID == nil {
		return Btn{Label: "⬆️ All topics", Data: dataTopicsRoot}
	}
	return Btn{Label: "⬆️ Up", Data: "top:up:" + view.ParentID.String()}
}

// topicRowLabel renders one topic row: a ✅/⬜ enabled-flag prefix (the
// listing-level visibility the retired /decks picker gave for free), name,
// aggregate progress (when this topic has any items), a 🔒 badge for its
// lowest locked tier, and a 💡 badge when a TipProvider exists —
// architecture §5.2's "▸ Languages   tier: 42/50 · 48/50" mock (word
// "introduced" dropped 2026-07-19 — it made rows overflow/truncate in
// Telegram), folded into a single button label (Telegram button text is
// never markup-parsed, so no escaping is needed here, unlike topicsBodyText).
func topicRowLabel(row TopicRow) string {
	var b strings.Builder
	if row.Enabled {
		b.WriteString("✅ ")
	} else {
		b.WriteString("⬜ ")
	}
	b.WriteString(row.Name)
	if row.Total > 0 {
		fmt.Fprintf(&b, "  %d/%d · %d good", row.Introduced, row.Total, row.GoodShape)
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
	return fmt.Sprintf("Tier %d: %d/%d · %d good%s", t.Tier, t.Introduced, t.Total, t.GoodShape, lock)
}
