// Package train orchestrates geodrill's core loop over the engram engine: it
// builds the due queue, generates multiple-choice exercises from ingested
// content, grades taps, and applies FSRS scheduling. It is the only place that
// wires engram + quiz to the storage layer, keeping internal/telegram thin.
package train

import (
	"context"
	"encoding/json"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/storage/engramstore"
)

// forecastDays is how many days /stats projects the due forecast over.
const forecastDays = 7

// maxConfusion is how many top confusion pairs /stats reports.
const maxConfusion = 5

// Service ties the engram scheduler + quiz generator to storage.
type Service struct {
	store *storage.Store
	sched *engram.Scheduler
	tips  quiz.TipProvider // optional post-answer explanations; may be nil

	mu  sync.Mutex // guards rng (math/rand.Rand is not concurrency-safe)
	rng *rand.Rand

	now func() time.Time // injectable clock (defaults to time.Now)
}

// NewService builds a Service. seed seeds the shuffle RNG; pass a fixed value
// for reproducible tests. If now is nil, time.Now is used. tips may be nil
// (answers are then graded without recognition tips).
func NewService(store *storage.Store, sched *engram.Scheduler, tips quiz.TipProvider, seed int64, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: store,
		sched: sched,
		tips:  tips,
		rng:   rand.New(rand.NewSource(seed)),
		now:   now,
	}
}

// Now returns the service clock.
func (s *Service) Now() time.Time { return s.now() }

// ── /train ──────────────────────────────────────────────────────────────────

// NextExercise selects the next thing to study for user and, if something is
// due, generates and persists a fresh exercise.
func (s *Service) NextExercise(ctx context.Context, user storage.User, now time.Time) (NextResult, error) {
	cards, err := s.store.ListEnabledSkillCards(ctx, user.ID)
	if err != nil {
		return NextResult{}, err
	}
	if len(cards) == 0 {
		return NextResult{Kind: KindNoDecks}, nil
	}

	items := make([]engram.QueueItem, 0, len(cards))
	skillByID := make(map[engram.SkillID]storage.Skill, len(cards))
	deckSkills := make(map[uuid.UUID][]storage.Skill)
	for _, sc := range cards {
		card := s.sched.NewCardState(now)
		if sc.HasCard {
			card = engramstore.CardStateFrom(sc.Card)
		}
		es := engramstore.SkillTo(sc.Skill)
		items = append(items, engram.QueueItem{Skill: es, Card: card})
		skillByID[es.ID] = sc.Skill
		deckSkills[sc.Skill.DeckID] = append(deckSkills[sc.Skill.DeckID], sc.Skill)
	}

	loc := locationOf(user)
	logToday, err := engramstore.New(s.store, user.ID).Log(ctx, startOfDay(now, loc))
	if err != nil {
		return NextResult{}, err
	}
	newIntro := engram.CountNewIntroduced(logToday, now, loc)
	cfg := engram.QueueConfig{MaxNewPerDay: user.DailyNewCap}

	work := items
	removedForContent := false
	for len(work) > 0 {
		item, ok := engram.NextDue(work, newIntro, cfg, now)
		if !ok {
			return NextResult{Kind: KindNothingDue, DueAt: earliestFutureDue(items, now)}, nil
		}
		target := skillByID[item.Skill.ID]
		prompt, found, err := s.buildExercise(ctx, user, target, deckSkills[target.DeckID], false, now)
		if err != nil {
			return NextResult{}, err
		}
		if found {
			return NextResult{Kind: KindExercise, Prompt: prompt}, nil
		}
		work = removeSkill(work, item.Skill.ID)
		removedForContent = true
	}
	if removedForContent {
		return NextResult{Kind: KindNoContent}, nil
	}
	return NextResult{Kind: KindNothingDue, DueAt: earliestFutureDue(items, now)}, nil
}

// ── /practice (endless, unscheduled) ────────────────────────────────────────

// NextPractice generates an unscheduled exercise from a random enabled skill.
func (s *Service) NextPractice(ctx context.Context, user storage.User, now time.Time) (NextResult, error) {
	skills, err := s.store.ListEnabledSkills(ctx, user.ID)
	if err != nil {
		return NextResult{}, err
	}
	if len(skills) == 0 {
		return NextResult{Kind: KindNoDecks}, nil
	}
	byDeck := make(map[uuid.UUID][]storage.Skill)
	for _, sk := range skills {
		byDeck[sk.DeckID] = append(byDeck[sk.DeckID], sk)
	}

	// Try skills in a random order until one has content.
	order := s.perm(len(skills))
	for _, idx := range order {
		target := skills[idx]
		prompt, found, err := s.buildExercise(ctx, user, target, byDeck[target.DeckID], true, now)
		if err != nil {
			return NextResult{}, err
		}
		if found {
			return NextResult{Kind: KindExercise, Prompt: prompt}, nil
		}
	}
	return NextResult{Kind: KindNoContent}, nil
}

// ── answering ───────────────────────────────────────────────────────────────

// Answer grades a tap (scheduled or practice). For scheduled answers it applies
// FSRS scheduling and appends a review; practice answers only grade. A stale
// tap (already answered / unknown exercise) returns AnswerResult{Stale: true}.
func (s *Service) Answer(ctx context.Context, cb Callback, now time.Time) (AnswerResult, error) {
	ex, found, err := s.store.GetExercise(ctx, cb.ExerciseID)
	if err != nil {
		return AnswerResult{}, err
	}
	if !found {
		return AnswerResult{Stale: true}, nil
	}

	owned, err := s.store.MarkExerciseAnswered(ctx, cb.ExerciseID, now)
	if err != nil {
		return AnswerResult{}, err
	}
	if !owned {
		return AnswerResult{Stale: true}, nil
	}

	sk, ok, err := s.store.GetSkillByID(ctx, ex.SkillID)
	if err != nil {
		return AnswerResult{}, err
	}
	if !ok {
		return AnswerResult{Stale: true}, nil
	}
	correctKey := sk.Key
	correct := cb.Key == correctKey
	rating := engram.RatingForAnswer(correct)

	if !cb.Practice {
		if err := s.schedule(ctx, ex, correctKey, cb.Key, correct, rating, now); err != nil {
			return AnswerResult{}, err
		}
	} else {
		if err := s.recordPractice(ctx, ex, correctKey, cb.Key, correct, rating, now); err != nil {
			return AnswerResult{}, err
		}
	}

	buttons, err := gradedButtons(ex.Options, cb.Key, correctKey)
	if err != nil {
		return AnswerResult{}, err
	}
	res := AnswerResult{
		Correct:    correct,
		ChosenKey:  cb.Key,
		CorrectKey: correctKey,
		Buttons:    buttons,
		MessageID:  ex.MessageID,
		HasMessage: ex.HasMessage,
	}

	// The tip is decoration and the review is already committed above, so any
	// failure here degrades to "no tip" rather than failing the answer.
	if s.tips != nil {
		if content, found, cerr := s.store.GetContentByID(ctx, ex.ContentID); cerr == nil && found {
			res.SentenceText = content.Payload
			res.Tip = s.tips.Tip(quiz.TipRequest{
				ContentPayload: content.Payload,
				CorrectKey:     correctKey,
				CorrectLabel:   sk.Label,
				ChosenKey:      cb.Key,
				ChosenLabel:    chosenName(buttons, cb.Key),
				Correct:        correct,
			})
		}
	}
	return res, nil
}

// chosenName resolves the display label the user actually tapped from the
// persisted options, falling back to the raw key.
func chosenName(buttons []GradedButton, chosenKey string) string {
	for _, b := range buttons {
		if b.Key == chosenKey && b.Name != "" {
			return b.Name
		}
	}
	return chosenKey
}

// schedule applies FSRS and appends the full review row.
func (s *Service) schedule(ctx context.Context, ex storage.Exercise, correctKey, chosenKey string, correct bool, rating engram.Rating, now time.Time) error {
	card, has, err := s.store.GetCard(ctx, ex.UserID, ex.SkillID)
	if err != nil {
		return err
	}
	cs := s.sched.NewCardState(now)
	if has {
		cs = engramstore.CardStateFrom(card)
	}
	newCard, rev := s.sched.Next(cs, now, rating)
	if err := s.store.PutCard(ctx, ex.UserID, ex.SkillID, engramstore.CardFieldsFrom(newCard)); err != nil {
		return err
	}

	ms := int(now.Sub(ex.CreatedAt).Milliseconds())
	if ms < 0 {
		ms = 0
	}
	exID := ex.ID
	contentID := ex.ContentID
	return s.store.InsertReview(ctx, storage.ReviewInsert{
		UserID:           ex.UserID,
		SkillID:          ex.SkillID,
		ExerciseID:       &exID,
		ContentID:        &contentID,
		ChosenKey:        chosenKey,
		CorrectKey:       correctKey,
		Correct:          correct,
		Rating:           int16(rating),
		ResponseMS:       &ms,
		StabilityBefore:  rev.StabilityBefore,
		DifficultyBefore: rev.DifficultyBefore,
		StabilityAfter:   rev.StabilityAfter,
		DifficultyAfter:  rev.DifficultyAfter,
		StateBefore:      int16(rev.StateBefore),
		ScheduledDays:    rev.ScheduledDays,
		ElapsedDays:      rev.ElapsedDays,
		ReviewedAt:       now,
		Practice:         false,
	})
}

// recordPractice appends a review row for an unscheduled /practice answer.
// Unlike schedule, it never reads or writes user_skills (FSRS card state) —
// practice answers must not affect scheduling. The FSRS-specific columns are
// meaningless here and are zeroed; Practice is set so stats queries can
// distinguish these rows if they ever need to.
func (s *Service) recordPractice(ctx context.Context, ex storage.Exercise, correctKey, chosenKey string, correct bool, rating engram.Rating, now time.Time) error {
	ms := int(now.Sub(ex.CreatedAt).Milliseconds())
	if ms < 0 {
		ms = 0
	}
	exID := ex.ID
	contentID := ex.ContentID
	return s.store.InsertReview(ctx, storage.ReviewInsert{
		UserID:           ex.UserID,
		SkillID:          ex.SkillID,
		ExerciseID:       &exID,
		ContentID:        &contentID,
		ChosenKey:        chosenKey,
		CorrectKey:       correctKey,
		Correct:          correct,
		Rating:           int16(rating),
		ResponseMS:       &ms,
		StabilityBefore:  0,
		DifficultyBefore: 0,
		StabilityAfter:   0,
		DifficultyAfter:  0,
		StateBefore:      0,
		ScheduledDays:    0,
		ElapsedDays:      0,
		ReviewedAt:       now,
		Practice:         true,
	})
}

// ── /stats ──────────────────────────────────────────────────────────────────

// DueCount returns how many of the user's cards are due at or before now.
func (s *Service) DueCount(ctx context.Context, user storage.User, now time.Time) (int, error) {
	return s.store.CountDueSkills(ctx, user.ID, now)
}

// Stats builds the /stats view model.
func (s *Service) Stats(ctx context.Context, user storage.User, now time.Time) (Stats, error) {
	loc := locationOf(user)
	epoch := time.Unix(0, 0).UTC()

	today, err := s.store.CountReviewsSince(ctx, user.ID, startOfDay(now, loc))
	if err != nil {
		return Stats{}, err
	}
	week, err := s.store.CountReviewsSince(ctx, user.ID, now.AddDate(0, 0, -7))
	if err != nil {
		return Stats{}, err
	}

	// The review log includes practice rows (practice=true) — that's intended,
	// they should count toward reviews/accuracy/streak here. A future
	// FSRS-weight optimizer trained on this log must filter to practice=false,
	// since practice rows carry zeroed FSRS fields and were never scheduled.
	log, err := engramstore.New(s.store, user.ID).Log(ctx, epoch)
	if err != nil {
		return Stats{}, err
	}

	deckStats, err := s.store.ReviewStatsByDeck(ctx, user.ID, epoch)
	if err != nil {
		return Stats{}, err
	}
	byDeck := make([]DeckAccuracy, len(deckStats))
	for i, d := range deckStats {
		acc := 0.0
		if d.Total > 0 {
			acc = float64(d.Correct) / float64(d.Total)
		}
		byDeck[i] = DeckAccuracy{Slug: d.Slug, Name: d.Name, Total: d.Total, Correct: d.Correct, Accuracy: acc}
	}

	cardRows, err := s.store.ListCardsForUser(ctx, user.ID)
	if err != nil {
		return Stats{}, err
	}
	cards := make([]engram.CardState, len(cardRows))
	for i, c := range cardRows {
		cards[i] = engramstore.CardStateFrom(c)
	}
	forecast := engram.DueForecast(cards, forecastDays, now, loc)

	attemptRows, err := s.store.ListAttemptsSince(ctx, user.ID, epoch)
	if err != nil {
		return Stats{}, err
	}
	attempts := make([]quiz.Attempt, len(attemptRows))
	for i, a := range attemptRows {
		attempts[i] = quiz.Attempt{
			SkillID:    engram.SkillID(a.SkillID.String()),
			ChosenKey:  a.ChosenKey,
			CorrectKey: a.CorrectKey,
			Correct:    a.Correct,
			AnsweredAt: a.AnsweredAt,
			ResponseMS: a.ResponseMS,
		}
	}
	pairs := quiz.Confusion(attempts)
	if len(pairs) > maxConfusion {
		pairs = pairs[:maxConfusion]
	}

	labels, err := s.labelMap(ctx)
	if err != nil {
		return Stats{}, err
	}
	confusion := make([]ConfusionRow, len(pairs))
	for i, p := range pairs {
		confusion[i] = ConfusionRow{
			TargetKey:   p.TargetKey,
			TargetLabel: labelOr(labels, p.TargetKey),
			ChosenKey:   p.ChosenKey,
			ChosenLabel: labelOr(labels, p.ChosenKey),
			Count:       p.Count,
			Share:       p.Share,
		}
	}

	return Stats{
		ReviewsToday: today,
		ReviewsWeek:  week,
		Streak:       engram.Streak(log, now, loc),
		Accuracy:     engram.Accuracy(log),
		ByDeck:       byDeck,
		DueForecast:  forecast,
		Confusion:    confusion,
	}, nil
}

// ── internals ───────────────────────────────────────────────────────────────

// buildExercise samples content for target and, if any exists, generates and
// persists an exercise. found=false means target's content pool is empty.
func (s *Service) buildExercise(ctx context.Context, user storage.User, target storage.Skill, deckSkills []storage.Skill, practice bool, now time.Time) (*Prompt, bool, error) {
	content, found, err := s.store.SampleContent(ctx, user.ID, target.Key)
	if err != nil {
		return nil, false, err
	}
	if !found {
		content, found, err = s.store.SampleContentAny(ctx, target.Key)
		if err != nil {
			return nil, false, err
		}
	}
	if !found {
		return nil, false, nil
	}

	engSkills := make([]engram.Skill, len(deckSkills))
	for i, sk := range deckSkills {
		engSkills[i] = engramstore.SkillTo(sk)
	}
	qc := quiz.Content{ID: engram.ContentID(content.ID.String()), Payload: content.Payload}

	s.mu.Lock()
	ex := quiz.Generate(s.rng, engramstore.SkillTo(target), engSkills, qc)
	s.mu.Unlock()

	opts := make([]optionJSON, len(ex.Options))
	for i, o := range ex.Options {
		opts[i] = optionJSON{Key: o.Key, Label: o.Label}
	}
	optionsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, false, err
	}

	exerciseID, err := s.store.InsertExercise(ctx, user.ID, target.ID, content.ID, optionsJSON)
	if err != nil {
		return nil, false, err
	}

	buttons := make([]Button, len(ex.Options))
	for i, o := range ex.Options {
		data := AnswerData(exerciseID, o.Key)
		if practice {
			data = PracticeData(exerciseID, o.Key)
		}
		buttons[i] = Button{Key: o.Key, Label: o.Label, CallbackData: data}
	}
	return &Prompt{
		ExerciseID: exerciseID,
		Text:       content.Payload,
		Source:     content.Source,
		Buttons:    buttons,
		Practice:   practice,
	}, true, nil
}

// perm returns a random permutation of [0,n) using the guarded rng.
func (s *Service) perm(n int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Perm(n)
}

// labelMap returns key→label across every skill (for confusion display).
func (s *Service) labelMap(ctx context.Context) (map[string]string, error) {
	skills, err := s.store.ListAllSkills(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(skills))
	for _, sk := range skills {
		m[sk.Key] = sk.Label
	}
	return m, nil
}

// gradedButtons decorates the persisted options with grade marks.
func gradedButtons(optionsJSON []byte, chosenKey, correctKey string) ([]GradedButton, error) {
	var opts []optionJSON
	if err := json.Unmarshal(optionsJSON, &opts); err != nil {
		return nil, err
	}
	out := make([]GradedButton, len(opts))
	for i, o := range opts {
		m := markFor(o.Key, chosenKey, correctKey)
		out[i] = GradedButton{Key: o.Key, Name: o.Label, Label: DecorateLabel(o.Label, m), Mark: m}
	}
	return out, nil
}

func removeSkill(items []engram.QueueItem, id engram.SkillID) []engram.QueueItem {
	out := items[:0:0]
	for _, it := range items {
		if it.Skill.ID != id {
			out = append(out, it)
		}
	}
	return out
}

func earliestFutureDue(items []engram.QueueItem, now time.Time) time.Time {
	var best time.Time
	for _, it := range items {
		d := it.Card.Due
		if d.After(now) && (best.IsZero() || d.Before(best)) {
			best = d
		}
	}
	return best
}

func locationOf(user storage.User) *time.Location {
	if user.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func labelOr(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return key
}
