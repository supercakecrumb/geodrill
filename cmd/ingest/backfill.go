package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// guessLangContainerPath is the topic path guesslang.Seed writes every group
// topic under (architecture §2.11/§3.4). Duplicated here as a literal rather
// than imported from internal/topics/guesslang to keep -backfill-v2 decoupled
// from that package's Kind/RootSlug/ContainerSlug constants changing
// underneath it; the path is part of the seeded data contract, not the Go API.
const guessLangContainerPath = "languages/guess-the-language"

// BackfillV2Result totals every step of runBackfillV2, both for the log
// line printed at each step and so tests can assert on exact counts (task
// W4.1's "log per-step counts" + integration-test requirements).
type BackfillV2Result struct {
	SkillsMapped             int
	UserItemsMigrated        int
	UserItemsSkippedUnmapped int
	IntroductionsSynthesized int
	ExercisesRowsUpdated     int64
	ReviewsRowsUpdated       int64
}

// runBackfillV2 maps every legacy skill/user_skill/exercise/review row onto
// the v2 topics/items/user_items/introductions framework, in the same
// database (architecture §3.4/§3.5, task W4.1). It requires the
// languages/guess-the-language topic tree to already be seeded (run
// -seed-topics, or guesslang.Seed directly, first) and is safe to re-run:
// every write is either an idempotent upsert-if-absent or an
// item_id-IS-NULL-guarded UPDATE, so a second run touches nothing a first
// run already migrated.
func runBackfillV2(ctx context.Context, logger *slog.Logger, store *storage.Store) (BackfillV2Result, error) {
	var res BackfillV2Result

	skillToItem, skillToKey, err := buildSkillItemMap(ctx, store)
	if err != nil {
		return res, fmt.Errorf("build skill->item map: %w", err)
	}
	res.SkillsMapped = len(skillToItem)
	logger.Info("backfill-v2: built skill->item map", "skills_mapped", res.SkillsMapped)

	res.UserItemsMigrated, res.UserItemsSkippedUnmapped, res.IntroductionsSynthesized, err = backfillUserItems(ctx, store, skillToItem, time.Now())
	if err != nil {
		return res, fmt.Errorf("backfill user_items: %w", err)
	}
	logger.Info("backfill-v2: user_items",
		"migrated", res.UserItemsMigrated,
		"skipped_unmapped_skill", res.UserItemsSkippedUnmapped,
		"introductions_synthesized", res.IntroductionsSynthesized)

	res.ExercisesRowsUpdated, err = backfillExercises(ctx, store, skillToItem, skillToKey)
	if err != nil {
		return res, fmt.Errorf("backfill exercises: %w", err)
	}
	logger.Info("backfill-v2: exercises", "rows_updated", res.ExercisesRowsUpdated)

	res.ReviewsRowsUpdated, err = backfillReviews(ctx, store, skillToItem)
	if err != nil {
		return res, fmt.Errorf("backfill reviews: %w", err)
	}
	logger.Info("backfill-v2: reviews", "rows_updated", res.ReviewsRowsUpdated)

	return res, nil
}

// buildSkillItemMap resolves every legacy skill to its v2 item: skills
// belonging to deck D map to items under
// languages/guess-the-language/<D.slug> with the same key (architecture
// §2.11 — decks become group topics, skills become items, both keyed
// identically). Returns skillID->itemID and skillID->key (the key is needed
// again by backfillExercises, which sets correct_answer to it). Fails loud
// (rather than skipping) on any deck/skill without a matching seeded
// topic/item: that means -seed-topics hasn't run yet, or the seed data has
// drifted from decks.yaml, either of which should stop the backfill rather
// than silently leave rows unmapped.
func buildSkillItemMap(ctx context.Context, store *storage.Store) (skillToItem map[uuid.UUID]uuid.UUID, skillToKey map[uuid.UUID]string, err error) {
	decks, err := store.ListDecks(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list decks: %w", err)
	}

	skillToItem = make(map[uuid.UUID]uuid.UUID)
	skillToKey = make(map[uuid.UUID]string)

	for _, d := range decks {
		groupPath := guessLangContainerPath + "/" + d.Slug
		group, found, err := store.GetTopicByPath(ctx, groupPath)
		if err != nil {
			return nil, nil, fmt.Errorf("get topic %q: %w", groupPath, err)
		}
		if !found {
			return nil, nil, fmt.Errorf("topic %q not found — run -seed-topics (or guesslang.Seed) before -backfill-v2", groupPath)
		}

		items, err := store.ListItemsByTopic(ctx, group.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("list items for %q: %w", groupPath, err)
		}
		itemIDByKey := make(map[string]uuid.UUID, len(items))
		for _, it := range items {
			itemIDByKey[it.Key] = it.ID
		}

		skills, err := store.ListSkillsByDeck(ctx, d.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("list skills for deck %q: %w", d.Slug, err)
		}
		for _, sk := range skills {
			itemID, ok := itemIDByKey[sk.Key]
			if !ok {
				return nil, nil, fmt.Errorf("skill %s/%s has no matching item under %q — seed data mismatch (re-run -seed-topics)", d.Slug, sk.Key, groupPath)
			}
			skillToItem[sk.ID] = itemID
			skillToKey[sk.ID] = sk.Key
		}
	}
	return skillToItem, skillToKey, nil
}

// backfillUserItems migrates every user_skills row into user_items
// (lifecycle derived from FSRS state, FSRS columns copied 1:1,
// introduced_at = COALESCE(last_review, now)) and synthesizes a matching
// first-exposure introductions row when none exists yet (architecture
// §3.5). skipped counts rows whose skill isn't in skillToItem (defensive —
// buildSkillItemMap already fails loud on any deck/skill it can't map, so
// this should stay 0 in practice, but a row is skipped rather than crashing
// the whole backfill if it ever happens).
func backfillUserItems(ctx context.Context, store *storage.Store, skillToItem map[uuid.UUID]uuid.UUID, now time.Time) (migrated, skipped, introsCreated int, err error) {
	rows, err := store.ListAllUserSkills(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list user_skills: %w", err)
	}

	for _, r := range rows {
		itemID, ok := skillToItem[r.SkillID]
		if !ok {
			skipped++
			continue
		}

		// engram.LifecycleFor semantics (architecture §1.1/§3.5): FSRS state
		// Review(2)/Relearning(3) graduated into durable review; anything
		// else (New/Learning) is still "introduced" but not yet graduated.
		lifecycle := int16(1)
		if r.Card.State == 2 || r.Card.State == 3 {
			lifecycle = 2
		}

		introducedAt := r.Card.LastReview
		if introducedAt.IsZero() {
			introducedAt = now
		}

		inserted, err := store.InsertUserItemIfAbsent(ctx, r.UserID, itemID, lifecycle, r.Card, introducedAt)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("insert user_item for user %s item %s: %w", r.UserID, itemID, err)
		}
		if inserted {
			migrated++
		}

		hasIntro, err := store.HasIntroductionForItem(ctx, r.UserID, itemID)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("check introductions for user %s item %s: %w", r.UserID, itemID, err)
		}
		if !hasIntro {
			if err := store.InsertSynthesizedIntroduction(ctx, r.UserID, itemID, introducedAt); err != nil {
				return 0, 0, 0, fmt.Errorf("insert synthesized introduction for user %s item %s: %w", r.UserID, itemID, err)
			}
			introsCreated++
		}
	}
	return migrated, skipped, introsCreated, nil
}

// backfillExercises attaches item_id/mode/correct_answer to every
// still-unmapped exercise row, one bulk UPDATE per legacy skill.
func backfillExercises(ctx context.Context, store *storage.Store, skillToItem map[uuid.UUID]uuid.UUID, skillToKey map[uuid.UUID]string) (int64, error) {
	var total int64
	for skillID, itemID := range skillToItem {
		n, err := store.BackfillExercisesForSkill(ctx, skillID, itemID, skillToKey[skillID])
		if err != nil {
			return total, fmt.Errorf("backfill exercises for skill %s: %w", skillID, err)
		}
		total += n
	}
	return total, nil
}

// backfillReviews attaches item_id/mode/chosen/correct_answer to every
// still-unmapped review row, one bulk UPDATE per legacy skill.
func backfillReviews(ctx context.Context, store *storage.Store, skillToItem map[uuid.UUID]uuid.UUID) (int64, error) {
	var total int64
	for skillID, itemID := range skillToItem {
		n, err := store.BackfillReviewsForSkill(ctx, skillID, itemID)
		if err != nil {
			return total, fmt.Errorf("backfill reviews for skill %s: %w", skillID, err)
		}
		total += n
	}
	return total, nil
}
