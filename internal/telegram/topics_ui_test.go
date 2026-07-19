package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// stubTopicService implements TopicService with canned results.
type stubTopicService struct {
	root     []TopicRow
	children map[uuid.UUID]TopicView

	toggleCall struct {
		userID  uuid.UUID
		topicID uuid.UUID
		enabled bool
	}

	subtreeToggleCall struct {
		userID  uuid.UUID
		topicID uuid.UUID
		enabled bool
	}
}

func (s *stubTopicService) Root(ctx context.Context, userID uuid.UUID) ([]TopicRow, error) {
	return s.root, nil
}

func (s *stubTopicService) Children(ctx context.Context, userID, topicID uuid.UUID) (TopicView, error) {
	return s.children[topicID], nil
}

func (s *stubTopicService) SetTopicEnabled(ctx context.Context, userID, topicID uuid.UUID, enabled bool) error {
	s.toggleCall.userID = userID
	s.toggleCall.topicID = topicID
	s.toggleCall.enabled = enabled
	return nil
}

func (s *stubTopicService) SetSubtreeEnabled(ctx context.Context, userID, topicID uuid.UUID, enabled bool) error {
	s.subtreeToggleCall.userID = userID
	s.subtreeToggleCall.topicID = topicID
	s.subtreeToggleCall.enabled = enabled
	return nil
}

// ── parseTopicNav ────────────────────────────────────────────────────────

func TestParseTopicNav(t *testing.T) {
	id := uuid.New()

	nav, ok := parseTopicNav(dataTopicsRoot)
	if !ok || nav.kind != topicNavRoot {
		t.Fatalf("parseTopicNav(root) = %+v, %v", nav, ok)
	}

	nav, ok = parseTopicNav("top:up:" + id.String())
	if !ok || nav.kind != topicNavUp || nav.target != id {
		t.Fatalf("parseTopicNav(up) = %+v, %v, want target %v", nav, ok, id)
	}

	nav, ok = parseTopicNav("top:" + id.String())
	if !ok || nav.kind != topicNavDrill || nav.target != id {
		t.Fatalf("parseTopicNav(drill) = %+v, %v, want target %v", nav, ok, id)
	}
}

func TestParseTopicNav_Invalid(t *testing.T) {
	cases := []string{
		"",
		"noop",
		"deck:" + uuid.New().String(),
		"top:not-a-uuid",
		"top:up:not-a-uuid",
		"top:",
	}
	for _, data := range cases {
		if _, ok := parseTopicNav(data); ok {
			t.Fatalf("parseTopicNav(%q) unexpectedly succeeded", data)
		}
	}
}

// ── pure rendering ───────────────────────────────────────────────────────

func TestTopicRowLabel(t *testing.T) {
	row := TopicRow{
		Name: "Languages", Total: 50, Introduced: 48, GoodShape: 42,
		AnyLocked: true, LockedTier: 3, HasTips: true, Enabled: true,
	}
	got := topicRowLabel(row)
	for _, want := range []string{"✅", "Languages", "48/50", "42 good", "🔒 tier 3", "💡"} {
		if !strings.Contains(got, want) {
			t.Fatalf("topicRowLabel = %q, expected to contain %q", got, want)
		}
	}

	// A disabled topic's row must carry ⬜ instead of ✅.
	disabled := topicRowLabel(TopicRow{Name: "Roads", Enabled: false})
	if !strings.HasPrefix(disabled, "⬜") {
		t.Fatalf("expected a disabled topic's row to start with ⬜, got %q", disabled)
	}

	// A row with no items yet must not print a bogus 0/0 progress line.
	empty := topicRowLabel(TopicRow{Name: "Empty"})
	if strings.Contains(empty, "good") {
		t.Fatalf("expected no progress suffix for a zero-item row, got %q", empty)
	}
}

func TestTierRowLabel(t *testing.T) {
	got := tierRowLabel(TierRow{Tier: 2, Total: 10, Introduced: 6, GoodShape: 4, Locked: true})
	for _, want := range []string{"Tier 2", "6/10", "4 good", "🔒"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tierRowLabel = %q, expected to contain %q", got, want)
		}
	}
	if strings.Contains(tierRowLabel(TierRow{Tier: 0}), "🔒") {
		t.Fatalf("an unlocked tier must not carry the 🔒 badge")
	}
}

func TestTopicsBodyText_EscapesBreadcrumb(t *testing.T) {
	view := TopicView{Breadcrumb: []TopicCrumb{{Name: "Languages"}, {Name: "<script>"}}}
	got := topicsBodyText(view)
	if !strings.Contains(got, "Languages ▸ &lt;script&gt;") {
		t.Fatalf("expected an escaped, ▸-joined breadcrumb, got %q", got)
	}
}

func TestTopicNavButton(t *testing.T) {
	root := topicNavButton(TopicView{ParentID: nil})
	if root.Data != dataTopicsRoot {
		t.Fatalf("expected a root topic's ⬆️ to target %q, got %q", dataTopicsRoot, root.Data)
	}
	parentID := uuid.New()
	up := topicNavButton(TopicView{ParentID: &parentID})
	if up.Data != "top:up:"+parentID.String() {
		t.Fatalf("expected ⬆️ to target the parent, got %q", up.Data)
	}
}

func TestTopicsViewRows_ContainerVsQuizzable(t *testing.T) {
	childID := uuid.New()
	container := TopicView{
		IsQuizzable: false,
		Children:    []TopicRow{{TopicID: childID, Name: "Special characters"}},
	}
	rows := topicsViewRows(container)
	if len(rows) != 2 { // one child row + the ⬆️ row
		t.Fatalf("expected 2 rows (1 child + up), got %d", len(rows))
	}
	if rows[0][0].Data != "top:"+childID.String() {
		t.Fatalf("expected the child row to drill in via top:<uuid>, got %q", rows[0][0].Data)
	}

	quizzable := TopicView{
		IsQuizzable: true,
		Tiers:       []TierRow{{Tier: 0}, {Tier: 1}},
	}
	rows = topicsViewRows(quizzable)
	if len(rows) != 4 { // 2 tier rows + the toggle row + the ⬆️ row
		t.Fatalf("expected 4 rows (2 tiers + toggle + up), got %d", len(rows))
	}
	if rows[0][0].Data != "noop" || rows[1][0].Data != "noop" {
		t.Fatalf("expected tier rows to be inert (noop), got %+v", rows[:2])
	}
	if rows[2][0].Data != topicToggleCallbackData(quizzable.TopicID, true) {
		t.Fatalf("expected the toggle row right after the tier rows, got %+v", rows[2])
	}
}

// ── group-level toggle (container views) ────────────────────────────────

func TestTopicsViewRows_ContainerGroupToggle_LastRowLeftOfUp(t *testing.T) {
	childID := uuid.New()
	topicID := uuid.New()
	view := TopicView{
		TopicID:            topicID,
		IsQuizzable:        false,
		ParentID:           nil,
		Children:           []TopicRow{{TopicID: childID, Name: "Special characters"}},
		GroupEnabledLeaves: 3,
		GroupTotalLeaves:   5,
	}
	rows := topicsViewRows(view)
	if len(rows) != 2 { // one child row + the trailing [groupToggle][Up] row
		t.Fatalf("expected 2 rows (1 child + trailing), got %d", len(rows))
	}
	last := rows[len(rows)-1]
	if len(last) != 2 {
		t.Fatalf("expected the trailing row to carry exactly 2 buttons ([groupToggle][Up]), got %+v", last)
	}
	if last[0].Data != topicGroupToggleCallbackData(topicID, false) {
		t.Fatalf("expected the group toggle button first (left of Up), got %+v", last[0])
	}
	if last[1].Data != dataTopicsRoot {
		t.Fatalf("expected «⬆️ Up»/root nav button second (right of the group toggle), got %+v", last[1])
	}
}

func TestTopicsViewRows_ContainerNoQuizzableDescendants_NoGroupToggle(t *testing.T) {
	// GroupTotalLeaves == 0: the render side stays defensive even though
	// this shouldn't happen in practice (filterVisibleTopics already keeps
	// such subtrees out of every listing that could route here).
	view := TopicView{IsQuizzable: false, Children: []TopicRow{{Name: "Empty"}}}
	rows := topicsViewRows(view)
	last := rows[len(rows)-1]
	if len(last) != 1 {
		t.Fatalf("expected a bare [Up] trailing row with no group toggle, got %+v", last)
	}
}

func TestTopicsViewRows_QuizzableView_NoGroupToggle(t *testing.T) {
	// A leaf (quizzable) view must never render the group toggle, even if
	// GroupTotalLeaves were somehow populated — it only applies to
	// containers.
	view := TopicView{IsQuizzable: true, Tiers: []TierRow{{Tier: 0}}, GroupTotalLeaves: 5, GroupEnabledLeaves: 5}
	rows := topicsViewRows(view)
	for _, row := range rows {
		for _, btn := range row {
			if strings.Contains(btn.Data, "topen:g:") || strings.Contains(btn.Data, "topoff:g:") {
				t.Fatalf("a quizzable view must not render the group toggle, got %+v", row)
			}
		}
	}
}

func TestGroupToggleButton(t *testing.T) {
	id := uuid.New()

	// Any descendant enabled (including "all enabled") -> turn the group OFF.
	allOn := groupToggleButton(TopicView{TopicID: id, GroupEnabledLeaves: 5, GroupTotalLeaves: 5})
	if allOn.Data != topicGroupToggleCallbackData(id, false) {
		t.Fatalf("all-enabled group should offer to turn off, got %+v", allOn)
	}
	if !strings.Contains(allOn.Label, "off") {
		t.Fatalf("expected an 'off' label for a group with any enabled descendant, got %q", allOn.Label)
	}

	mixed := groupToggleButton(TopicView{TopicID: id, GroupEnabledLeaves: 2, GroupTotalLeaves: 5})
	if mixed.Data != topicGroupToggleCallbackData(id, false) {
		t.Fatalf("mixed group should offer to turn off, got %+v", mixed)
	}

	// All disabled -> turn the group ON.
	allOff := groupToggleButton(TopicView{TopicID: id, GroupEnabledLeaves: 0, GroupTotalLeaves: 5})
	if allOff.Data != topicGroupToggleCallbackData(id, true) {
		t.Fatalf("all-disabled group should offer to turn on, got %+v", allOff)
	}
	if !strings.Contains(allOff.Label, "on") {
		t.Fatalf("expected an 'on' label for an all-disabled group, got %q", allOff.Label)
	}
}

func TestTopicGroupToggleCallbackData_ParseRoundTrip(t *testing.T) {
	id := uuid.New()
	gotID, enable, ok := parseTopicGroupToggleCallback(topicGroupToggleCallbackData(id, true))
	if !ok || !enable || gotID != id {
		t.Fatalf("round trip (enable) = (%v,%v,%v), want (%v,true,true)", gotID, enable, ok, id)
	}
	gotID, enable, ok = parseTopicGroupToggleCallback(topicGroupToggleCallbackData(id, false))
	if !ok || enable || gotID != id {
		t.Fatalf("round trip (disable) = (%v,%v,%v), want (%v,false,true)", gotID, enable, ok, id)
	}
	// A plain single-topic toggle must NOT parse as a group toggle.
	for _, data := range []string{"topen:" + id.String(), "topoff:" + id.String(), "", "noop", "topen:g:not-a-uuid"} {
		if _, _, ok := parseTopicGroupToggleCallback(data); ok {
			t.Fatalf("parseTopicGroupToggleCallback(%q) unexpectedly succeeded", data)
		}
	}
}

func TestHandleTopicToggle_GroupFlipsSubtreeAndRerenders(t *testing.T) {
	topicID := uuid.New()
	b := newTestBot(&stubStore{user: newTestUser()})
	stub := &stubTopicService{
		children: map[uuid.UUID]TopicView{
			topicID: {TopicID: topicID, IsQuizzable: false, GroupEnabledLeaves: 0, GroupTotalLeaves: 3, Breadcrumb: []TopicCrumb{{TopicID: topicID, Name: "Languages"}}},
		},
	}
	b.topics = stub

	s := &fakeSession{userID: 1, messageID: 42, data: topicGroupToggleCallbackData(topicID, true)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.subtreeToggleCall.topicID != topicID || !stub.subtreeToggleCall.enabled {
		t.Fatalf("expected SetSubtreeEnabled(topicID, true), got %+v", stub.subtreeToggleCall)
	}
	if stub.toggleCall.topicID == topicID {
		t.Fatalf("a group toggle must not also call the single-topic SetTopicEnabled")
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 42 {
		t.Fatalf("expected the topic view re-rendered in place, got %+v", s.editedMsgs)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

func TestHandleTopicToggle_GroupNilTopicServiceIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: topicGroupToggleCallbackData(uuid.New(), true)}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

// ── topicToggleButton / topen:/topoff: callback ─────────────────────────

func TestTopicToggleButton(t *testing.T) {
	id := uuid.New()
	enabled := topicToggleButton(TopicView{TopicID: id, Enabled: true})
	if enabled.Data != "topoff:"+id.String() {
		t.Fatalf("an enabled topic's toggle must request topoff:, got %q", enabled.Data)
	}
	disabled := topicToggleButton(TopicView{TopicID: id, Enabled: false})
	if disabled.Data != "topen:"+id.String() {
		t.Fatalf("a disabled topic's toggle must request topen:, got %q", disabled.Data)
	}
}

func TestParseTopicToggleCallback(t *testing.T) {
	id := uuid.New()
	gotID, enable, ok := parseTopicToggleCallback("topen:" + id.String())
	if !ok || !enable || gotID != id {
		t.Fatalf("parseTopicToggleCallback(topen:) = (%v,%v,%v), want (%v,true,true)", gotID, enable, ok, id)
	}
	gotID, enable, ok = parseTopicToggleCallback("topoff:" + id.String())
	if !ok || enable || gotID != id {
		t.Fatalf("parseTopicToggleCallback(topoff:) = (%v,%v,%v), want (%v,false,true)", gotID, enable, ok, id)
	}
	for _, data := range []string{"", "noop", "top:" + id.String(), "topen:not-a-uuid", "topoff:not-a-uuid"} {
		if _, _, ok := parseTopicToggleCallback(data); ok {
			t.Fatalf("parseTopicToggleCallback(%q) unexpectedly succeeded", data)
		}
	}
}

func TestHandleTopicToggle_FlipsAndRerenders(t *testing.T) {
	topicID := uuid.New()
	b := newTestBot(&stubStore{user: newTestUser()})
	stub := &stubTopicService{
		children: map[uuid.UUID]TopicView{
			topicID: {TopicID: topicID, IsQuizzable: true, Enabled: true, Breadcrumb: []TopicCrumb{{TopicID: topicID, Name: "Roads"}}},
		},
	}
	b.topics = stub

	s := &fakeSession{userID: 1, messageID: 42, data: "topoff:" + topicID.String()}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if stub.toggleCall.topicID != topicID || stub.toggleCall.enabled {
		t.Fatalf("expected SetTopicEnabled(topicID, false), got %+v", stub.toggleCall)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].messageID != 42 {
		t.Fatalf("expected the topic view re-rendered in place, got %+v", s.editedMsgs)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

func TestHandleTopicToggle_NilTopicServiceIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: "topen:" + uuid.New().String()}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

func TestHandleTopicToggle_InvalidDataIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.topics = &stubTopicService{}
	s := &fakeSession{userID: 1, data: "topen:not-a-uuid"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

// ── /topics ──────────────────────────────────────────────────────────────

func TestHandleTopics_DormantWhenNil(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1}
	if err := b.handleTopics(context.Background(), s); err != nil {
		t.Fatalf("handleTopics: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != topicsDormantText {
		t.Fatalf("expected the dormant message, got %v", s.sent)
	}
}

func TestHandleTopics_SendsRootListing(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.topics = &stubTopicService{root: []TopicRow{{Name: "Languages"}}}

	s := &fakeSession{userID: 1}
	if err := b.handleTopics(context.Background(), s); err != nil {
		t.Fatalf("handleTopics: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != topicsRootText {
		t.Fatalf("expected the root listing sent, got %+v", s.keyboards)
	}
	// One row per root topic, plus the trailing «⬅️ Menu» row (hub-and-spoke
	// rule: the root listing is where "Up" navigation would otherwise leave
	// the tree with no way back).
	rows := s.keyboards[0].rows
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (1 topic + Menu), got %d", len(rows))
	}
	if rows[1][0].Data != dataMenuOpen {
		t.Fatalf("expected the trailing row to be «⬅️ Menu», got %+v", rows[1])
	}
}

// ── top: callback ────────────────────────────────────────────────────────

func TestHandleTopicCallback_Drill(t *testing.T) {
	topicID := uuid.New()
	b := newTestBot(&stubStore{user: newTestUser()})
	b.topics = &stubTopicService{
		children: map[uuid.UUID]TopicView{
			topicID: {
				TopicID:    topicID,
				Breadcrumb: []TopicCrumb{{TopicID: topicID, Name: "Special characters"}},
				Children:   []TopicRow{{Name: "ø"}},
			},
		},
	}

	s := &fakeSession{userID: 1, messageID: 42, data: "top:" + topicID.String()}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedMsgs) != 1 {
		t.Fatalf("expected one EditMessage call, got %d", len(s.editedMsgs))
	}
	if s.editedMsgs[0].messageID != 42 {
		t.Fatalf("expected the edit on message 42, got %d", s.editedMsgs[0].messageID)
	}
	if !strings.Contains(s.editedMsgs[0].text, "Special characters") {
		t.Fatalf("expected the breadcrumb in the edited text, got %q", s.editedMsgs[0].text)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
}

func TestHandleTopicCallback_Root(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	b.topics = &stubTopicService{root: []TopicRow{{Name: "Languages"}, {Name: "Roads"}}}

	s := &fakeSession{userID: 1, messageID: 7, data: dataTopicsRoot}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].text != topicsRootText {
		t.Fatalf("expected the root listing re-rendered in place, got %+v", s.editedMsgs)
	}
	// 2 topic rows + the trailing «⬅️ Menu» row.
	rows := s.editedMsgs[0].rows
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (2 topics + Menu), got %d", len(rows))
	}
	if rows[2][0].Data != dataMenuOpen {
		t.Fatalf("expected the trailing row to be «⬅️ Menu», got %+v", rows[2])
	}
}

func TestHandleTopicCallback_InvalidDataIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	stub := &stubTopicService{}
	b.topics = stub

	s := &fakeSession{userID: 1, data: "top:not-a-uuid"}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
	if len(s.editedMsgs) != 0 {
		t.Fatalf("expected no edit for malformed nav data, got %d", len(s.editedMsgs))
	}
}

func TestHandleTopicCallback_NilTopicServiceIsInert(t *testing.T) {
	b := newTestBot(&stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1, data: "top:" + uuid.New().String()}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.responses) != 1 || s.responses[0] != "" {
		t.Fatalf("expected a single inert ack, got %v", s.responses)
	}
	if len(s.editedMsgs) != 0 {
		t.Fatalf("expected no edit when TopicService is nil")
	}
}
