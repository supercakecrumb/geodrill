// topics_service.go implements telegram.TopicService: the /topics tree
// browser (architecture §5.2).
package study

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

var _ telegram.TopicService = (*Service)(nil)

// Root implements telegram.TopicService.
func (s *Service) Root(ctx context.Context, userID uuid.UUID) ([]telegram.TopicRow, error) {
	roots, err := s.store.ListRootTopics(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.store.ListAllTopics(ctx)
	if err != nil {
		return nil, err
	}
	allowed, err := s.gating.AllowedTiers(ctx, userID)
	if err != nil {
		return nil, err
	}
	tree := buildTopicTree(all)
	roots = filterVisibleTopics(roots, tree)

	rows := make([]telegram.TopicRow, len(roots))
	for i, t := range roots {
		row, err := s.topicRow(ctx, userID, t, tree, allowedTierSet(allowed))
		if err != nil {
			return nil, err
		}
		rows[i] = row
	}
	return rows, nil
}

// Children implements telegram.TopicService.
func (s *Service) Children(ctx context.Context, userID, topicID uuid.UUID) (telegram.TopicView, error) {
	topic, found, err := s.store.GetTopicByID(ctx, topicID)
	if err != nil {
		return telegram.TopicView{}, err
	}
	if !found {
		return telegram.TopicView{}, fmt.Errorf("study: topic %s not found", topicID)
	}

	breadcrumb, err := s.breadcrumbFor(ctx, topic)
	if err != nil {
		return telegram.TopicView{}, err
	}

	enabled, err := s.store.GetUserTopicEnabled(ctx, userID, topicID)
	if err != nil {
		return telegram.TopicView{}, err
	}

	view := telegram.TopicView{
		TopicID:     topic.ID,
		Name:        topic.Name,
		IsQuizzable: topic.IsQuizzable,
		Breadcrumb:  breadcrumb,
		ParentID:    topic.ParentID,
		Enabled:     enabled,
	}

	if topic.IsQuizzable {
		allowed, err := s.gating.AllowedTiers(ctx, userID)
		if err != nil {
			return telegram.TopicView{}, err
		}
		allowedSet := allowedTierSet(allowed)
		breakdown, err := s.store.RecomputeTopicTierBreakdown(ctx, userID, topicID)
		if err != nil {
			return telegram.TopicView{}, err
		}
		tiers := make([]telegram.TierRow, len(breakdown))
		for i, b := range breakdown {
			tiers[i] = telegram.TierRow{
				Tier:       int(b.Tier),
				Total:      b.TotalItems,
				Introduced: b.IntroducedItems,
				GoodShape:  b.GoodShapeItems,
				Locked:     !allowedSet[b.Tier],
			}
		}
		view.Tiers = tiers
		return view, nil
	}

	children, err := s.store.ListChildTopics(ctx, topicID)
	if err != nil {
		return telegram.TopicView{}, err
	}
	all, err := s.store.ListAllTopics(ctx)
	if err != nil {
		return telegram.TopicView{}, err
	}
	allowed, err := s.gating.AllowedTiers(ctx, userID)
	if err != nil {
		return telegram.TopicView{}, err
	}
	tree := buildTopicTree(all)
	allowedSet := allowedTierSet(allowed)
	children = filterVisibleTopics(children, tree)

	rows := make([]telegram.TopicRow, len(children))
	for i, c := range children {
		row, err := s.topicRow(ctx, userID, c, tree, allowedSet)
		if err != nil {
			return telegram.TopicView{}, err
		}
		rows[i] = row
	}
	view.Children = rows
	return view, nil
}

// SetTopicEnabled implements telegram.TopicService: the /topics counterpart
// of the retired /decks' per-deck toggle (architecture: only a quizzable
// topic's flag has any gating effect — see disabledTopicSet/
// enabledQuizzableTopicIDs — toggling a container is accepted but inert).
func (s *Service) SetTopicEnabled(ctx context.Context, userID, topicID uuid.UUID, enabled bool) error {
	return s.store.SetUserTopicEnabled(ctx, userID, topicID, enabled)
}

// breadcrumbFor walks topic's parent chain (root-first) via GetTopicByID,
// ending with topic itself.
func (s *Service) breadcrumbFor(ctx context.Context, topic storage.Topic) ([]telegram.TopicCrumb, error) {
	chain := []telegram.TopicCrumb{{TopicID: topic.ID, Name: topic.Name}}
	cur := topic
	for cur.ParentID != nil {
		parent, found, err := s.store.GetTopicByID(ctx, *cur.ParentID)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		chain = append(chain, telegram.TopicCrumb{TopicID: parent.ID, Name: parent.Name})
		cur = parent
	}
	// chain was built leaf-first; reverse to root-first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// topicTree indexes a flat topic list by parent, for the recursive HasTips
// computation and the container-row builder.
type topicTree struct {
	byParent map[uuid.UUID][]storage.Topic // parent id -> children; root topics keyed under uuid.Nil
}

func buildTopicTree(all []storage.Topic) topicTree {
	tree := topicTree{byParent: make(map[uuid.UUID][]storage.Topic)}
	for _, t := range all {
		key := uuid.Nil
		if t.ParentID != nil {
			key = *t.ParentID
		}
		tree.byParent[key] = append(tree.byParent[key], t)
	}
	return tree
}

func (t topicTree) children(id uuid.UUID) []storage.Topic { return t.byParent[id] }

// filterVisibleTopics keeps only topics whose subtree has at least one
// quizzable topic (itself or some descendant) — the /topics browser's
// generic "hide subtrees with no quizzable descendants" rule
// (vibe/design-game-zone.md): when every topic under a container is
// is_quizzable=false (e.g. languages/guess-the-language, whose exercise
// moved into the game zone), that whole subtree simply disappears from the
// listing, with no slug-based special-casing anywhere in this package.
func filterVisibleTopics(topics []storage.Topic, tree topicTree) []storage.Topic {
	out := make([]storage.Topic, 0, len(topics))
	for _, t := range topics {
		if hasQuizzableDescendant(t, tree) {
			out = append(out, t)
		}
	}
	return out
}

// hasQuizzableDescendant reports whether t itself is quizzable, or (for a
// container) any topic in its subtree is.
func hasQuizzableDescendant(t storage.Topic, tree topicTree) bool {
	if t.IsQuizzable {
		return true
	}
	for _, c := range tree.children(t.ID) {
		if hasQuizzableDescendant(c, tree) {
			return true
		}
	}
	return false
}

// topicRow builds one TopicRow for t: subtree progress, the lowest locked
// tier under it (if any), and whether it (or, for a container, any
// descendant) has a TipProvider.
func (s *Service) topicRow(ctx context.Context, userID uuid.UUID, t storage.Topic, tree topicTree, allowed map[int16]bool) (telegram.TopicRow, error) {
	progress, err := s.store.RecomputeTopicProgress(ctx, userID, t.ID)
	if err != nil {
		return telegram.TopicRow{}, err
	}
	tiersUsed, err := s.store.ListDistinctTiersUnderTopic(ctx, t.ID)
	if err != nil {
		return telegram.TopicRow{}, err
	}
	anyLocked, lockedTier := lowestLockedTier(tiersUsed, allowed)
	enabled, err := s.store.GetUserTopicEnabled(ctx, userID, t.ID)
	if err != nil {
		return telegram.TopicRow{}, err
	}

	return telegram.TopicRow{
		TopicID:     t.ID,
		Name:        t.Name,
		IsQuizzable: t.IsQuizzable,
		Introduced:  progress.IntroducedItems,
		Total:       progress.TotalItems,
		GoodShape:   progress.GoodShapeItems,
		AnyLocked:   anyLocked,
		LockedTier:  int(lockedTier),
		HasTips:     s.hasTips(t, tree),
		Enabled:     enabled,
	}, nil
}

// lowestLockedTier reports whether any tier in tiersUsed is absent from
// allowed, and the lowest such tier.
func lowestLockedTier(tiersUsed []int16, allowed map[int16]bool) (anyLocked bool, lockedTier int16) {
	first := true
	for _, tr := range tiersUsed {
		if allowed[tr] {
			continue
		}
		if first || tr < lockedTier {
			lockedTier = tr
			first = false
		}
		anyLocked = true
	}
	return anyLocked, lockedTier
}

// hasTips reports whether t (a quizzable topic) or, for a container, ANY
// descendant quizzable topic has a recognition-tip TipProvider (architecture
// §5.2's "▸ Languages 💡 tips"). guess-the-language no longer registers a
// Generator at all (its exercise moved into the game zone,
// vibe/design-game-zone.md, and its tips content moved with it into
// internal/game), so this is now a plain, generic check with no
// slug/quiz_kind special-casing.
func (s *Service) hasTips(t storage.Topic, tree topicTree) bool {
	if !t.IsQuizzable {
		for _, c := range tree.children(t.ID) {
			if s.hasTips(c, tree) {
				return true
			}
		}
		return false
	}
	gen, ok := s.reg.Get(t.QuizKind)
	if !ok {
		return false
	}
	_, ok = gen.(topics.TipProvider)
	return ok
}
