package storage

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func itemFrom(i db.Item) Item {
	return Item{
		ID:        i.ID,
		TopicID:   i.TopicID,
		Key:       i.Key,
		Label:     i.Label,
		Tier:      int2Ptr(i.Tier),
		Payload:   i.Payload,
		CountryID: i.CountryID,
		Position:  int(i.Position),
		Active:    i.Active,
		CreatedAt: tsTime(i.CreatedAt),
	}
}

// UpsertItem inserts or updates an item by (topic_id, key). tier nil inherits
// the topic's base_tier (architecture §2.2).
func (s *Store) UpsertItem(ctx context.Context, topicID uuid.UUID, key, label string, tier *int16, payload []byte, countryID *uuid.UUID, position int, active bool) (Item, error) {
	i, err := s.q.UpsertItem(ctx, db.UpsertItemParams{
		TopicID:   topicID,
		Key:       key,
		Label:     label,
		Tier:      pgInt2(tier),
		Payload:   payload,
		CountryID: countryID,
		Position:  int32(position),
		Active:    active,
	})
	if err != nil {
		return Item{}, err
	}
	return itemFrom(i), nil
}

// GetItemByID looks up an item by primary key.
func (s *Store) GetItemByID(ctx context.Context, id uuid.UUID) (Item, bool, error) {
	i, err := s.q.GetItemByID(ctx, id)
	if IsNotFound(err) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	return itemFrom(i), true, nil
}

// ListItemsByTopic returns every item (active or not) in a topic.
func (s *Store) ListItemsByTopic(ctx context.Context, topicID uuid.UUID) ([]Item, error) {
	rows, err := s.q.ListItemsByTopic(ctx, topicID)
	if err != nil {
		return nil, err
	}
	out := make([]Item, len(rows))
	for i, r := range rows {
		out[i] = itemFrom(r)
	}
	return out, nil
}

// ListActiveItemsByTopic returns only active items in a topic.
func (s *Store) ListActiveItemsByTopic(ctx context.Context, topicID uuid.UUID) ([]Item, error) {
	rows, err := s.q.ListActiveItemsByTopic(ctx, topicID)
	if err != nil {
		return nil, err
	}
	out := make([]Item, len(rows))
	for i, r := range rows {
		out[i] = itemFrom(r)
	}
	return out, nil
}

// ListItemsWithTierByTopic returns a topic's items with their effective tier
// (COALESCE(items.tier, topics.base_tier), via the item_tiers view).
func (s *Store) ListItemsWithTierByTopic(ctx context.Context, topicID uuid.UUID) ([]ItemWithTier, error) {
	rows, err := s.q.ListItemsWithTierByTopic(ctx, topicID)
	if err != nil {
		return nil, err
	}
	out := make([]ItemWithTier, len(rows))
	for idx, r := range rows {
		out[idx] = ItemWithTier{
			Item: Item{
				ID:        r.ID,
				TopicID:   r.TopicID,
				Key:       r.Key,
				Label:     r.Label,
				Tier:      int2Ptr(r.Tier),
				Payload:   r.Payload,
				CountryID: r.CountryID,
				Position:  int(r.Position),
				Active:    r.Active,
				CreatedAt: tsTime(r.CreatedAt),
			},
			EffectiveTier: r.EffectiveTier,
		}
	}
	return out, nil
}

// GetItemEffectiveTier returns COALESCE(items.tier, topics.base_tier) for one
// item, via the item_tiers view.
func (s *Store) GetItemEffectiveTier(ctx context.Context, itemID uuid.UUID) (int16, error) {
	return s.q.GetItemEffectiveTier(ctx, itemID)
}

// ListActiveItemsForPractice returns active items across topicIDs restricted
// to allowedTiers — the /practice candidate pool (internal/study.Service.
// NextPracticeV2): enabled+quizzable topics, tier-gated like every other v2
// read.
func (s *Store) ListActiveItemsForPractice(ctx context.Context, topicIDs []uuid.UUID, allowedTiers []int16) ([]Item, error) {
	rows, err := s.q.ListActiveItemsForPractice(ctx, db.ListActiveItemsForPracticeParams{
		Column1: topicIDs,
		Column2: allowedTiers,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Item, len(rows))
	for i, r := range rows {
		out[i] = itemFrom(r)
	}
	return out, nil
}

// ListAllItemKeyLabels returns a global key->label map across every item
// (best-effort; see the underlying query's doc) for /stats confusion display.
func (s *Store) ListAllItemKeyLabels(ctx context.Context) (map[string]string, error) {
	rows, err := s.q.ListAllItemKeyLabels(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Label
	}
	return out, nil
}
