package study

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// Bridge deck/skill/content identifiers (see the package doc's "contract
// friction" section): a single, idempotently-upserted placeholder row that
// satisfies exercises/reviews' still-NOT-NULL legacy skill_id/content_id
// columns for v2 topics with no natural skill or sampled content of their
// own.
const (
	bridgeDeckSlug    = "_v2_bridge"
	bridgeDeckName    = "v2 bridge (internal)"
	bridgeSkillKey    = "_v2_bridge"
	bridgeSkillLabel  = "v2 bridge (internal)"
	bridgeContentKind = "_v2_bridge"
	bridgeContentKey  = "_v2_bridge"
)

// bridgeIDs returns the (skillID, contentID) placeholder pair, creating it
// on first use and caching the result for the lifetime of the Service.
// UpsertDeck/UpsertSkill/InsertContent are all idempotent upserts, so this
// converges even if two Service instances race to create it (e.g. during
// tests).
func (s *Service) bridgeIDs(ctx context.Context) (skillID, contentID uuid.UUID, err error) {
	s.bridgeOnce.Do(func() {
		s.bridgeSkillID, s.bridgeContentID, s.bridgeErr = ensureBridge(ctx, s.store)
	})
	return s.bridgeSkillID, s.bridgeContentID, s.bridgeErr
}

// ensureBridge idempotently creates (or fetches) the placeholder skill and
// content row.
func ensureBridge(ctx context.Context, store *storage.Store) (skillID, contentID uuid.UUID, err error) {
	deck, err := store.UpsertDeck(ctx, bridgeDeckSlug, bridgeDeckName)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("study: ensure bridge deck: %w", err)
	}
	skill, err := store.UpsertSkill(ctx, deck.ID, bridgeSkillKey, bridgeSkillLabel)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("study: ensure bridge skill: %w", err)
	}
	if err := store.InsertContent(ctx, bridgeContentKind, bridgeContentKey, "", "internal", 0); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("study: ensure bridge content: %w", err)
	}
	content, found, err := store.GetContentByKindKey(ctx, bridgeContentKind, bridgeContentKey)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("study: fetch bridge content: %w", err)
	}
	if !found {
		return uuid.Nil, uuid.Nil, fmt.Errorf("study: bridge content missing right after insert")
	}
	return skill.ID, content.ID, nil
}
