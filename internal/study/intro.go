// intro.go implements telegram.StudyService: /study's introduction flow
// (architecture §5.1).
package study

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

var _ telegram.StudyService = (*Service)(nil)

// candidatesFor returns userID's current introduction candidates: active
// items in their currently-unlocked tiers, not yet introduced (architecture
// §1.4/§4.2), ordered tier -> topic position -> item position.
func (s *Service) candidatesFor(ctx context.Context, userID uuid.UUID) ([]storage.IntroCandidate, error) {
	allowed, err := s.gating.AllowedTiers(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.store.ListCandidateIntroItems(ctx, userID, toInt16Slice(allowed))
}

// introBudget returns userID's daily intro cap and how many items have
// already been introduced (first-exposure, answered) in their local day so
// far (architecture §2.4/§2.10) — the reusable pair NextIntro/IntroSummary
// both need.
func (s *Service) introBudget(ctx context.Context, userID uuid.UUID) (cap, introducedToday int, err error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	from, to := dayBounds(s.now(), locationFor(user))
	introducedToday, err = s.store.CountIntroductionsToday(ctx, userID, from, to)
	if err != nil {
		return 0, 0, err
	}
	return user.DailyIntroCap, introducedToday, nil
}

// NextIntro implements telegram.StudyService.
func (s *Service) NextIntro(ctx context.Context, userID uuid.UUID) (telegram.IntroCard, error) {
	candidates, err := s.candidatesFor(ctx, userID)
	if err != nil {
		return telegram.IntroCard{}, err
	}
	cap, introducedToday, err := s.introBudget(ctx, userID)
	if err != nil {
		return telegram.IntroCard{}, err
	}

	introCandidates := make([]topics.IntroCandidate, len(candidates))
	for i, c := range candidates {
		introCandidates[i] = topics.IntroCandidate{
			Item:      storage.Item{ID: c.ItemID, TopicID: c.TopicID, Key: c.Key, Label: c.Label},
			Lifecycle: int16(engram.LifecycleNew),
		}
	}
	picked := topics.SelectIntroductions(introCandidates, introducedToday, cap)
	if len(picked) == 0 {
		return telegram.IntroCard{Reason: introReasonForEmpty(len(candidates)), IntroducedToday: introducedToday}, nil
	}

	winnerID := picked[0].ID
	item, found, err := s.store.GetItemByID(ctx, winnerID)
	if err != nil {
		return telegram.IntroCard{}, err
	}
	if !found {
		return telegram.IntroCard{}, fmt.Errorf("study: intro candidate item %s vanished", winnerID)
	}
	topic, found, err := s.store.GetTopicByID(ctx, item.TopicID)
	if err != nil {
		return telegram.IntroCard{}, err
	}
	if !found {
		return telegram.IntroCard{}, fmt.Errorf("study: item %s references missing topic %s", item.ID, item.TopicID)
	}
	gen, ok := s.reg.Get(topic.QuizKind)
	if !ok {
		return telegram.IntroCard{}, fmt.Errorf("study: no generator registered for quiz_kind %q (topic %s)", topic.QuizKind, topic.Slug)
	}
	card, err := gen.BuildIntro(ctx, item)
	if err != nil {
		return telegram.IntroCard{}, err
	}

	now := s.now()
	seq, err := s.store.NextIntroSeq(ctx, userID, item.ID)
	if err != nil {
		return telegram.IntroCard{}, err
	}
	row, err := s.store.InsertIntroduction(ctx, userID, item.ID, seq, now)
	if err != nil {
		return telegram.IntroCard{}, err
	}

	budgetLeft := engram.RemainingIntroBudget(cap, introducedToday)
	return telegram.IntroCard{
		IntroID:         row.ID,
		ItemID:          item.ID,
		Text:            card.Text,
		MediaPath:       card.MediaPath,
		Remaining:       max0(budgetLeft - 1),
		Reason:          telegram.IntroOK,
		IntroducedToday: introducedToday,
	}, nil
}

// AnswerIntro implements telegram.StudyService: applies outcome atomically
// (architecture §5.5) — the introduction's single-use answer guard, the
// user_items lifecycle+card upsert, and the affected tier's progress
// recompute all commit together or not at all. A second tap on an
// already-answered card (stale, architecture §5.1) is detected by the
// atomic guard and returns a "no-op" ack rather than double-applying the
// lifecycle transition.
func (s *Service) AnswerIntro(ctx context.Context, userID, introID uuid.UUID, outcome engram.IntroOutcome) (telegram.IntroAck, error) {
	intro, found, err := s.store.GetIntroductionByID(ctx, introID)
	if err != nil {
		return telegram.IntroAck{}, err
	}
	if !found {
		return telegram.IntroAck{Text: "That card is no longer available."}, nil
	}

	// item_tiers is static (topics.base_tier / items.tier only), so reading
	// the effective tier outside the transaction is safe regardless of
	// concurrent writes to this or any other user's data.
	effectiveTier, err := s.store.GetItemEffectiveTier(ctx, intro.ItemID)
	if err != nil {
		return telegram.IntroAck{}, err
	}

	now := s.now()
	life, cs, hasCard := s.sched.Introduce(outcome, now)

	var introducedAt, knownAt time.Time
	switch life {
	case engram.LifecycleKnown:
		knownAt = now
	default:
		introducedAt = now
	}
	card := storage.CardFields{}
	if hasCard {
		card = cardFieldsFrom(cs)
	}

	var stale bool
	err = s.store.WithTxStore(ctx, func(tx *storage.Store) error {
		_, owned, err := tx.AnswerIntroductionOnce(ctx, introID, int16(outcome), now)
		if err != nil {
			return err
		}
		if !owned {
			stale = true
			return nil
		}
		if err := tx.PutUserItem(ctx, userID, intro.ItemID, int16(life), card, introducedAt, knownAt); err != nil {
			return err
		}
		progress, ok, err := tx.RecomputeTierProgressForTier(ctx, userID, effectiveTier)
		if err != nil {
			return err
		}
		if ok {
			progress.Complete = tierComplete(progress)
			if err := tx.UpsertTierProgress(ctx, progress); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return telegram.IntroAck{}, err
	}
	if stale {
		return telegram.IntroAck{Text: "Already recorded ✅"}, nil
	}
	return telegram.IntroAck{Text: introAckText(outcome)}, nil
}

// introReasonForEmpty decides the terminal Reason when SelectIntroductions
// picks nothing: IntroNoneAvailable when there were no candidates at all
// (nothing unlocked left to introduce), IntroBudgetExhausted when candidates
// existed but today's daily cap is already spent (architecture §5.1). Pure
// (no I/O), so the reason logic is unit-testable without a database.
func introReasonForEmpty(candidateCount int) telegram.IntroReason {
	if candidateCount > 0 {
		return telegram.IntroBudgetExhausted
	}
	return telegram.IntroNoneAvailable
}

// introAckText renders the confirmation shown in place of an answered intro
// card (architecture §5.1).
func introAckText(o engram.IntroOutcome) string {
	switch o {
	case engram.IntroKnown:
		return "🧠 Marked as known — won't be quizzed."
	case engram.IntroTestMe:
		return "🎯 In the queue with a long first interval."
	default: // engram.IntroGotIt
		return "✅ Added to your review queue."
	}
}

// IntroSummary implements telegram.StudyService.
func (s *Service) IntroSummary(ctx context.Context, userID uuid.UUID) (available, budgetLeft int, err error) {
	candidates, err := s.candidatesFor(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	cap, introducedToday, err := s.introBudget(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	return len(candidates), engram.RemainingIntroBudget(cap, introducedToday), nil
}
