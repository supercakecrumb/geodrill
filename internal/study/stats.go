// stats.go implements telegram.TrainerV2's Stats method: the /stats view
// model computed over v2 reviews/user_items (the v2 counterpart of the
// legacy internal/train.Service.Stats).
package study

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage/engramstore"
	"github.com/supercakecrumb/geodrill/internal/telegram"
)

// forecastDaysV2 is how many days /stats projects the due forecast over
// (mirrors internal/train.Service's forecastDays).
const forecastDaysV2 = 7

// maxConfusionV2 is how many top confusion pairs /stats reports (mirrors
// internal/train.Service's maxConfusion).
const maxConfusionV2 = 5

// Stats implements telegram.TrainerV2.
//
// Reviews today/week and the accuracy/streak log intentionally read the
// WHOLE reviews table, not just item_id-tagged v2 rows: reviews is the same
// table the legacy /train wrote to, so a user's activity from before this
// port still contributes to their streak and accuracy — only the
// item-linked views (per-topic accuracy, confusion) need the item_id IS NOT
// NULL restriction, since a legacy row carries no topic/item to attribute
// to.
func (s *Service) Stats(ctx context.Context, userID uuid.UUID) (telegram.StatsV2, error) {
	now := s.now()
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return telegram.StatsV2{}, err
	}
	loc := locationFor(user)
	epoch := time.Unix(0, 0).UTC()

	today, err := s.store.CountReviewsSince(ctx, userID, startOfDay(now, loc))
	if err != nil {
		return telegram.StatsV2{}, err
	}
	week, err := s.store.CountReviewsSince(ctx, userID, now.AddDate(0, 0, -7))
	if err != nil {
		return telegram.StatsV2{}, err
	}

	log, err := engramstore.New(s.store, userID).Log(ctx, epoch)
	if err != nil {
		return telegram.StatsV2{}, err
	}

	topicStats, err := s.store.ReviewStatsByTopic(ctx, userID, epoch)
	if err != nil {
		return telegram.StatsV2{}, err
	}
	byTopic := make([]telegram.TopicAccuracyV2, len(topicStats))
	for i, t := range topicStats {
		acc := 0.0
		if t.Total > 0 {
			acc = float64(t.Correct) / float64(t.Total)
		}
		byTopic[i] = telegram.TopicAccuracyV2{Name: t.Name, Total: t.Total, Correct: t.Correct, Accuracy: acc}
	}

	cardRows, err := s.store.ListUserItemCardsInFSRS(ctx, userID)
	if err != nil {
		return telegram.StatsV2{}, err
	}
	cards := make([]engram.CardState, len(cardRows))
	for i, c := range cardRows {
		cards[i] = engramstore.CardStateFrom(c)
	}
	forecast := engram.DueForecast(cards, forecastDaysV2, now, loc)

	attemptRows, err := s.store.ListAttemptsSinceV2(ctx, userID, epoch)
	if err != nil {
		return telegram.StatsV2{}, err
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
	if len(pairs) > maxConfusionV2 {
		pairs = pairs[:maxConfusionV2]
	}

	labels, err := s.store.ListAllItemKeyLabels(ctx)
	if err != nil {
		return telegram.StatsV2{}, err
	}
	confusion := make([]telegram.ConfusionRowV2, len(pairs))
	for i, p := range pairs {
		confusion[i] = telegram.ConfusionRowV2{
			TargetLabel: labelOrKey(labels, p.TargetKey),
			ChosenLabel: labelOrKey(labels, p.ChosenKey),
			Count:       p.Count,
			Share:       p.Share,
		}
	}

	introduced, err := s.store.CountIntroducedItems(ctx, userID)
	if err != nil {
		return telegram.StatsV2{}, err
	}
	known, err := s.store.CountKnownItems(ctx, userID)
	if err != nil {
		return telegram.StatsV2{}, err
	}

	return telegram.StatsV2{
		ReviewsToday: today,
		ReviewsWeek:  week,
		Streak:       engram.Streak(log, now, loc),
		Accuracy:     engram.Accuracy(log),
		ByTopic:      byTopic,
		DueForecast:  forecast,
		Confusion:    confusion,
		Introduced:   introduced,
		Known:        known,
	}, nil
}

// labelOrKey returns labels[key], falling back to key itself when absent
// (mirrors internal/train's labelOr).
func labelOrKey(labels map[string]string, key string) string {
	if v, ok := labels[key]; ok {
		return v
	}
	return key
}
