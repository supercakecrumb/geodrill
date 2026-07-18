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
}

func (s *stubTopicService) Root(ctx context.Context, userID uuid.UUID) ([]TopicRow, error) {
	return s.root, nil
}

func (s *stubTopicService) Children(ctx context.Context, userID, topicID uuid.UUID) (TopicView, error) {
	return s.children[topicID], nil
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
		AnyLocked: true, LockedTier: 3, HasTips: true,
	}
	got := topicRowLabel(row)
	for _, want := range []string{"Languages", "48/50 introduced", "42 good", "🔒 tier 3", "💡"} {
		if !strings.Contains(got, want) {
			t.Fatalf("topicRowLabel = %q, expected to contain %q", got, want)
		}
	}

	// A row with no items yet must not print a bogus 0/0 progress line.
	empty := topicRowLabel(TopicRow{Name: "Empty"})
	if strings.Contains(empty, "introduced") {
		t.Fatalf("expected no progress suffix for a zero-item row, got %q", empty)
	}
}

func TestTierRowLabel(t *testing.T) {
	got := tierRowLabel(TierRow{Tier: 2, Total: 10, Introduced: 6, GoodShape: 4, Locked: true})
	for _, want := range []string{"Tier 2", "6/10 introduced", "4 good", "🔒"} {
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
	if len(rows) != 3 { // 2 tier rows + the ⬆️ row
		t.Fatalf("expected 3 rows (2 tiers + up), got %d", len(rows))
	}
	if rows[0][0].Data != "noop" || rows[1][0].Data != "noop" {
		t.Fatalf("expected tier rows to be inert (noop), got %+v", rows[:2])
	}
}

// ── /topics ──────────────────────────────────────────────────────────────

func TestHandleTopics_DormantWhenNil(t *testing.T) {
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
	s := &fakeSession{userID: 1}
	if err := b.handleTopics(context.Background(), s); err != nil {
		t.Fatalf("handleTopics: %v", err)
	}
	if len(s.sent) != 1 || s.sent[0] != topicsDormantText {
		t.Fatalf("expected the dormant message, got %v", s.sent)
	}
}

func TestHandleTopics_SendsRootListing(t *testing.T) {
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
	b.topics = &stubTopicService{root: []TopicRow{{Name: "Languages"}}}

	s := &fakeSession{userID: 1}
	if err := b.handleTopics(context.Background(), s); err != nil {
		t.Fatalf("handleTopics: %v", err)
	}
	if len(s.keyboards) != 1 || s.keyboards[0].text != topicsRootText {
		t.Fatalf("expected the root listing sent, got %+v", s.keyboards)
	}
	if len(s.keyboards[0].rows) != 1 {
		t.Fatalf("expected one row for one root topic, got %d", len(s.keyboards[0].rows))
	}
}

// ── top: callback ────────────────────────────────────────────────────────

func TestHandleTopicCallback_Drill(t *testing.T) {
	topicID := uuid.New()
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
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
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
	b.topics = &stubTopicService{root: []TopicRow{{Name: "Languages"}, {Name: "Roads"}}}

	s := &fakeSession{userID: 1, messageID: 7, data: dataTopicsRoot}
	if err := b.handleCallback(context.Background(), s); err != nil {
		t.Fatalf("handleCallback: %v", err)
	}
	if len(s.editedMsgs) != 1 || s.editedMsgs[0].text != topicsRootText {
		t.Fatalf("expected the root listing re-rendered in place, got %+v", s.editedMsgs)
	}
	if len(s.editedMsgs[0].rows) != 2 {
		t.Fatalf("expected 2 rows for 2 root topics, got %d", len(s.editedMsgs[0].rows))
	}
}

func TestHandleTopicCallback_InvalidDataIsInert(t *testing.T) {
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
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
	b := newTestBot(&stubTrainer{}, &stubStore{user: newTestUser()})
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
