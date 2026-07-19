// stats.go implements telegram.Trainer's Stats method: the /stats view
// model computed over reviews/user_items.
package study

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/telegram"
)

// forecastDays is how many days /stats projects the due forecast over.
const forecastDays = 7

// maxConfusion is how many top confusion pairs /stats reports.
const maxConfusion = 5

// Stats implements telegram.Trainer.
//
// Reviews today/week and the accuracy/streak log intentionally read the
// WHOLE reviews table, not just item_id-tagged rows: reviews is the same
// table the legacy /train wrote to, so a user's activity from before this
// port still contributes to their streak and accuracy — only the
// item-linked views (per-topic accuracy, confusion) need the item_id IS NOT
// NULL restriction, since a legacy row carries no topic/item to attribute
// to.
func (s *Service) Stats(ctx context.Context, userID uuid.UUID) (telegram.Stats, error) {
	now := s.now()
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return telegram.Stats{}, err
	}
	loc := locationFor(user)
	epoch := time.Unix(0, 0).UTC()

	today, err := s.store.CountReviewsSince(ctx, userID, startOfDay(now, loc))
	if err != nil {
		return telegram.Stats{}, err
	}
	week, err := s.store.CountReviewsSince(ctx, userID, now.AddDate(0, 0, -7))
	if err != nil {
		return telegram.Stats{}, err
	}

	log, err := s.reviewLog(ctx, userID, epoch)
	if err != nil {
		return telegram.Stats{}, err
	}

	topicStats, err := s.store.ReviewStatsByTopic(ctx, userID, epoch)
	if err != nil {
		return telegram.Stats{}, err
	}
	byTopic := make([]telegram.TopicAccuracy, len(topicStats))
	for i, t := range topicStats {
		acc := 0.0
		if t.Total > 0 {
			acc = float64(t.Correct) / float64(t.Total)
		}
		byTopic[i] = telegram.TopicAccuracy{Name: t.Name, Total: t.Total, Correct: t.Correct, Accuracy: acc}
	}

	cardRows, err := s.store.ListUserItemCardsInFSRS(ctx, userID)
	if err != nil {
		return telegram.Stats{}, err
	}
	cards := make([]engram.CardState, len(cardRows))
	for i, c := range cardRows {
		cards[i] = cardStateFrom(c)
	}
	forecast := engram.DueForecast(cards, forecastDays, now, loc)

	attemptRows, err := s.store.ListAttemptsSince(ctx, userID, epoch)
	if err != nil {
		return telegram.Stats{}, err
	}
	attempts := make([]quiz.Attempt, len(attemptRows))
	for i, a := range attemptRows {
		attempts[i] = quiz.Attempt{
			ChosenKey:  a.Chosen,
			CorrectKey: a.CorrectAnswer,
			Correct:    a.Correct,
			AnsweredAt: a.AnsweredAt,
			ResponseMS: a.ResponseMS,
		}
	}
	pairs := quiz.Confusion(attempts)
	if len(pairs) > maxConfusion {
		pairs = pairs[:maxConfusion]
	}

	labels, err := s.store.ListAllItemKeyLabels(ctx)
	if err != nil {
		return telegram.Stats{}, err
	}
	confusion := make([]telegram.ConfusionRow, len(pairs))
	for i, p := range pairs {
		confusion[i] = telegram.ConfusionRow{
			TargetLabel: labelOrKey(labels, p.TargetKey),
			ChosenLabel: labelOrKey(labels, p.ChosenKey),
			Count:       p.Count,
			Share:       p.Share,
		}
	}

	introduced, err := s.store.CountIntroducedItems(ctx, userID)
	if err != nil {
		return telegram.Stats{}, err
	}
	known, err := s.store.CountKnownItems(ctx, userID)
	if err != nil {
		return telegram.Stats{}, err
	}

	tier, maxTier, err := s.CurrentTier(ctx, userID)
	if err != nil {
		return telegram.Stats{}, err
	}

	return telegram.Stats{
		ReviewsToday: today,
		ReviewsWeek:  week,
		Streak:       engram.Streak(log, now, loc),
		Accuracy:     engram.Accuracy(log),
		ByTopic:      byTopic,
		DueForecast:  forecast,
		Confusion:    confusion,
		Introduced:   introduced,
		Known:        known,
		Tier:         tier,
		MaxTier:      maxTier,
	}, nil
}

// DueCount implements telegram.Trainer: how many of userID's cards
// (user_items in lifecycle Introduced/Reviewing) are due right now, feeding
// the reminder loop's due-review count (architecture §5.3).
func (s *Service) DueCount(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.store.CountDueUserItems(ctx, userID, s.now())
}

// reviewLog returns userID's reviews at or after since as []engram.Review,
// for engram.Streak/Accuracy (formerly a storage-layer engram adapter's
// Log method — moved here since this package is its only remaining caller
// now that the legacy trainer is gone).
func (s *Service) reviewLog(ctx context.Context, userID uuid.UUID, since time.Time) ([]engram.Review, error) {
	recs, err := s.store.ListReviewsSince(ctx, userID, since)
	if err != nil {
		return nil, err
	}
	out := make([]engram.Review, len(recs))
	for i, r := range recs {
		out[i] = engram.Review{
			SkillID:          engram.SkillID(r.ItemID.String()),
			Rating:           engram.Rating(r.Rating),
			ReviewedAt:       r.ReviewedAt,
			StabilityBefore:  r.StabilityBefore,
			DifficultyBefore: r.DifficultyBefore,
			StabilityAfter:   r.StabilityAfter,
			DifficultyAfter:  r.DifficultyAfter,
			StateBefore:      engram.State(r.StateBefore),
			ScheduledDays:    r.ScheduledDays,
			ElapsedDays:      r.ElapsedDays,
		}
	}
	return out, nil
}

// labelOrKey returns labels[key], falling back to key itself when absent.
func labelOrKey(labels map[string]string, key string) string {
	if v, ok := labels[key]; ok {
		return v
	}
	return key
}
