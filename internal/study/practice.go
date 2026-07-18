// practice.go implements TrainerV2's /practice path: an endless, unscheduled
// practice pool across a user's enabled+tier-unlocked topics (/practice is
// a survivor per architecture §5, retargeted from legacy decks/skills onto
// the v2 topic/item model). Answers route through the SAME AnswerV2/
// AnswerText grading path as /train — the exercise row's own practice=true
// flag (set here, at creation time, via buildExerciseForItem/
// persistExerciseV2) is what makes finishAnswer (trainer.go) skip FSRS
// movement, mirroring how the legacy Callback.Practice flag worked.
package study

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/telegram"
)

// NextPracticeV2 implements telegram.TrainerV2: a random active item across
// the user's enabled+quizzable topics, restricted to their currently-
// unlocked tiers (the same tier gate every other v2 read applies). Unlike
// NextExerciseV2, due status and lifecycle never matter here — practice
// never touches scheduling, so any active, tier-unlocked item is fair game
// regardless of whether (or how recently) it's been reviewed.
func (s *Service) NextPracticeV2(ctx context.Context, userID uuid.UUID) (telegram.PromptV2, error) {
	now := s.now()
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return telegram.PromptV2{}, err
	}

	userTopics, err := s.store.ListUserTopics(ctx, userID)
	if err != nil {
		return telegram.PromptV2{}, err
	}
	topicIDs := enabledQuizzableTopicIDs(userTopics)
	if len(topicIDs) == 0 {
		return telegram.PromptV2{Kind: telegram.PromptV2KindNoTopics}, nil
	}

	allowed, err := s.gating.AllowedTiers(ctx, userID)
	if err != nil {
		return telegram.PromptV2{}, err
	}
	items, err := s.store.ListActiveItemsForPractice(ctx, topicIDs, toInt16Slice(allowed))
	if err != nil {
		return telegram.PromptV2{}, err
	}
	if len(items) == 0 {
		return telegram.PromptV2{Kind: telegram.PromptV2KindNoContent}, nil
	}

	// Try items in a random order until one builds an exercise ("try until
	// one has content" loop).
	for _, idx := range s.perm(len(items)) {
		prompt, built, err := s.buildExerciseForItem(ctx, user, items[idx], true, now)
		if err != nil {
			return telegram.PromptV2{}, err
		}
		if built {
			return prompt, nil
		}
	}
	return telegram.PromptV2{Kind: telegram.PromptV2KindNoContent}, nil
}

// perm returns a random permutation of [0,n) using the guarded rng
// (math/rand.Rand is not concurrency-safe).
func (s *Service) perm(n int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Perm(n)
}

// enabledQuizzableTopicIDs returns the ids of every quizzable topic the user
// hasn't explicitly disabled (default-on, mirroring disabledTopicSet's own
// "no row = enabled" convention) — the /practice topic pool. Container
// topics are excluded: they hold no items of their own, so
// ListActiveItemsForPractice would never match them anyway, but filtering
// here keeps the topic-id list itself meaningful.
func enabledQuizzableTopicIDs(userTopics []storage.UserTopic) []uuid.UUID {
	var out []uuid.UUID
	for _, ut := range userTopics {
		if ut.IsQuizzable && ut.Enabled {
			out = append(out, ut.ID)
		}
	}
	return out
}
