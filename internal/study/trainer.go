// trainer.go implements telegram.TrainerV2: the mode-aware v2 exercise path
// (architecture §1.6).
package study

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/storage/engramstore"
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
	"github.com/supercakecrumb/geodrill/internal/train"
)

var _ telegram.TrainerV2 = (*Service)(nil)

// ── NextExerciseV2 ───────────────────────────────────────────────────────

// NextExerciseV2 implements telegram.TrainerV2. Due candidates are
// Introduced/Reviewing items whose topic is enabled for the user
// (architecture §1.6 step 4) — no tier filter is needed here: an item only
// ever reaches lifecycle Introduced/Reviewing by passing the tier gate at
// introduction time (architecture §4.2), and tiers only ever unlock, never
// re-lock, so an already-introduced item stays valid regardless of the
// user's CURRENT unlocked-tier set.
func (s *Service) NextExerciseV2(ctx context.Context, userID uuid.UUID) (telegram.PromptV2, error) {
	now := s.now()
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return telegram.PromptV2{}, err
	}
	due, err := s.store.ListDueUserItems(ctx, userID, now)
	if err != nil {
		return telegram.PromptV2{}, err
	}
	userTopics, err := s.store.ListUserTopics(ctx, userID)
	if err != nil {
		return telegram.PromptV2{}, err
	}
	disabled := disabledTopicSet(userTopics)
	work := filterEnabledDue(due, disabled)

	for len(work) > 0 {
		picked, ok := engram.NextReview(toQueueItemsV2(work), now)
		if !ok {
			break
		}
		itemID, err := uuid.Parse(string(picked.Skill.ID))
		if err != nil {
			return telegram.PromptV2{}, err
		}
		prompt, built, err := s.buildExerciseV2(ctx, user, itemID, now)
		if err != nil {
			return telegram.PromptV2{}, err
		}
		if built {
			return prompt, nil
		}
		// This item's generator couldn't build ANY of its topic's configured
		// modes (topics.ErrNoContent or every mode mismatching its shape) —
		// skip it and try the next due candidate, mirroring how the legacy
		// train.Service.NextExercise handles buildExercise's found=false.
		work = removeDueItem(work, itemID)
	}

	dueAt, err := s.earliestFutureDue(ctx, userID, now)
	if err != nil {
		return telegram.PromptV2{}, err
	}
	if len(due) == 0 {
		return telegram.PromptV2{Kind: telegram.PromptV2KindNothingDue, DueAt: dueAt}, nil
	}
	// due was non-empty but every candidate was filtered out (disabled
	// topics) or failed to build an exercise for every configured mode.
	if dueAt.IsZero() {
		return telegram.PromptV2{Kind: telegram.PromptV2KindNoContent}, nil
	}
	return telegram.PromptV2{Kind: telegram.PromptV2KindNothingDue, DueAt: dueAt}, nil
}

// earliestFutureDue scans every Introduced/Reviewing item for userID (not
// just currently-due ones — ListDueUserItems only returns due <= now rows)
// for the earliest Due strictly after now, for PromptV2's "come back at ..."
// hint. Best-effort: unlike ListDueUserItems it can't cheaply exclude
// disabled topics (ListUserItemsByLifecycle carries no topic_id), so a
// disabled topic's item could rarely skew this cosmetic hint — acceptable,
// since DueAt is advisory text, not a gating decision.
func (s *Service) earliestFutureDue(ctx context.Context, userID uuid.UUID, now time.Time) (time.Time, error) {
	var best time.Time
	for _, lifecycle := range [...]int16{int16(engram.LifecycleIntroduced), int16(engram.LifecycleReviewing)} {
		items, err := s.store.ListUserItemsByLifecycle(ctx, userID, lifecycle)
		if err != nil {
			return time.Time{}, err
		}
		for _, ui := range items {
			if ui.Card.Due.After(now) && (best.IsZero() || ui.Card.Due.Before(best)) {
				best = ui.Card.Due
			}
		}
	}
	return best, nil
}

// buildExerciseV2 loads itemID's item/topic/generator/siblings and tries the
// topic's configured exercise modes in rotation order (modeRotationOrder)
// until one builds successfully. built=false means every configured mode
// failed (the item currently can't be quizzed) — the caller skips it.
func (s *Service) buildExerciseV2(ctx context.Context, user storage.User, itemID uuid.UUID, now time.Time) (telegram.PromptV2, bool, error) {
	item, found, err := s.store.GetItemByID(ctx, itemID)
	if err != nil {
		return telegram.PromptV2{}, false, err
	}
	if !found {
		return telegram.PromptV2{}, false, nil
	}
	topic, found, err := s.store.GetTopicByID(ctx, item.TopicID)
	if err != nil {
		return telegram.PromptV2{}, false, err
	}
	if !found {
		return telegram.PromptV2{}, false, nil
	}
	gen, ok := s.reg.Get(topic.QuizKind)
	if !ok {
		return telegram.PromptV2{}, false, fmt.Errorf("study: no generator registered for quiz_kind %q (topic %s)", topic.QuizKind, topic.Slug)
	}

	siblings, err := s.store.ListActiveItemsByTopic(ctx, item.TopicID)
	if err != nil {
		return telegram.PromptV2{}, false, err
	}
	siblings = removeItemFromSlice(siblings, item.ID)

	userItem, _, err := s.store.GetUserItem(ctx, user.ID, itemID)
	if err != nil {
		return telegram.PromptV2{}, false, err
	}

	for _, modeStr := range modeRotationOrder(topic.ExerciseModes, userItem.Card.Reps) {
		mode := modeFromString(modeStr)
		s.mu.Lock()
		ex, buildErr := gen.BuildExercise(ctx, s.rng, topics.ExerciseRequest{
			User: user, Topic: topic, Item: item, Siblings: siblings, Mode: mode,
		})
		s.mu.Unlock()
		if buildErr != nil {
			continue
		}
		prompt, err := s.persistExerciseV2(ctx, user.ID, item, ex, now)
		if err != nil {
			return telegram.PromptV2{}, false, err
		}
		return prompt, true, nil
	}
	return telegram.PromptV2{}, false, nil
}

// modeRotationOrder is the exercise-mode-choice rule for a multi-mode topic
// (architecture §1.6 step 4): rotate the starting mode by the item's own
// review count (reps), then try every configured mode in that rotated
// order. Rotating by reps means repeated reviews of the SAME item cycle
// through its topic's configured modes for variety (e.g.
// specialchars/languages/special-characters, exercise_modes
// {single,set,text}), while trying every mode (not just the rotated first
// one) means an item whose PAYLOAD SHAPE only fits a subset of its topic's
// modes (e.g. a char_language subgroup item only ever builds under
// ModeSet — see specialchars.Generator.BuildExercise) still gets a working
// exercise: this package has no shape-aware "which modes fit this item"
// query of its own (that knowledge is deliberately private to each
// Generator, per the topics package's "content access stays out of this
// package" contract), so trying the full rotated list and taking the first
// mode that builds is the generic way to respect a per-item shape
// constraint without this package parsing any topic's item payload.
// Falls back to {"single"} for a topic with no configured modes (shouldn't
// happen — the DB column defaults to {single} — but defends against it).
func modeRotationOrder(modes []string, reps int) []string {
	if len(modes) == 0 {
		return []string{"single"}
	}
	n := len(modes)
	start := ((reps % n) + n) % n // defensive against a hypothetical negative reps
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = modes[(start+i)%n]
	}
	return out
}

// modeFromString maps a topics.exercise_modes string to quiz.Mode, defaulting
// unrecognised values to ModeSingle.
func modeFromString(m string) quiz.Mode {
	switch m {
	case "set":
		return quiz.ModeSet
	case "text":
		return quiz.ModeText
	default:
		return quiz.ModeSingle
	}
}

// persistExerciseV2 serializes ex per its mode (model.go) and inserts the
// exercises row, returning the ready-to-send PromptV2.
func (s *Service) persistExerciseV2(ctx context.Context, userID uuid.UUID, item storage.Item, ex topics.Exercise, now time.Time) (telegram.PromptV2, error) {
	optionsJSON, correctAnswer, err := serializeExerciseV2(ex)
	if err != nil {
		return telegram.PromptV2{}, err
	}

	bridgeSkill, bridgeContent, err := s.bridgeIDs(ctx)
	if err != nil {
		return telegram.PromptV2{}, err
	}
	contentID := bridgeContent
	if ex.ContentID != nil {
		contentID = *ex.ContentID
	}

	exID, _, err := s.store.InsertExerciseV2(ctx, storage.InsertExerciseV2Params{
		UserID:        userID,
		SkillID:       bridgeSkill,
		ContentID:     contentID,
		ItemID:        item.ID,
		Mode:          int16(ex.Mode),
		Prompt:        ex.Prompt,
		Options:       optionsJSON,
		CorrectAnswer: correctAnswer,
		IsMedia:       ex.MediaPath != "",
		Practice:      false,
	})
	if err != nil {
		return telegram.PromptV2{}, err
	}

	return telegram.PromptV2{
		Kind:       telegram.PromptV2KindExercise,
		ExerciseID: exID,
		Text:       ex.Prompt,
		MediaPath:  ex.MediaPath,
		Mode:       ex.Mode,
		Options:    optionV2sFor(ex),
	}, nil
}

// canonicalSetString renders a set-choice option's key list into the same
// canonical, order-insensitive string both generation (see
// specialchars.buildSetMCQ) and grading use for set-equality comparison.
func canonicalSetString(keys []string) string {
	return strings.Join(quiz.CanonSet(keys...), ",")
}

// serializeExerciseV2 renders a topics.Exercise into its persisted
// exercises.options jsonb + correct_answer pair (architecture §2.5, model.go).
func serializeExerciseV2(ex topics.Exercise) ([]byte, string, error) {
	switch ex.Mode {
	case quiz.ModeSet:
		opts := make([]setOptionJSON, len(ex.OptionSets))
		for i, o := range ex.OptionSets {
			opts[i] = setOptionJSON{Keys: append([]string(nil), o.Keys...), Label: o.Label}
		}
		b, err := json.Marshal(opts)
		return b, ex.CorrectAnswer, err
	case quiz.ModeText:
		b, err := json.Marshal(textOptionsJSON{Accept: ex.Accept})
		return b, ex.CorrectAnswer, err
	default: // quiz.ModeSingle
		opts := make([]singleOptionJSON, len(ex.Options))
		for i, o := range ex.Options {
			opts[i] = singleOptionJSON{Key: o.Key, Label: o.Label}
		}
		b, err := json.Marshal(opts)
		return b, ex.CorrectAnswer, err
	}
}

// optionV2sFor renders a topics.Exercise's options into PromptV2's index-
// ordered OptionV2 list (empty for ModeText, which has no buttons).
func optionV2sFor(ex topics.Exercise) []telegram.OptionV2 {
	switch ex.Mode {
	case quiz.ModeSet:
		out := make([]telegram.OptionV2, len(ex.OptionSets))
		for i, o := range ex.OptionSets {
			out[i] = telegram.OptionV2{Index: i, Label: o.Label}
		}
		return out
	case quiz.ModeText:
		return nil
	default:
		out := make([]telegram.OptionV2, len(ex.Options))
		for i, o := range ex.Options {
			out[i] = telegram.OptionV2{Index: i, Label: o.Label}
		}
		return out
	}
}

// ── grading ──────────────────────────────────────────────────────────────

// gradeIndexed grades a tapped option position against a persisted v2
// exercise (architecture §1.6 step 5): ModeSingle compares the option's key
// to correct_answer; ModeSet compares the option's canonicalized key-set
// string to correct_answer (both sides canonicalized the same way,
// canonicalSetString). Pure given ex — no I/O — so index/set-canonicalization
// grading is unit-testable without a database. Out-of-range idx grades wrong
// (chosen == "") rather than erroring.
func gradeIndexed(ex storage.ExerciseV2, idx int) (correct bool, chosen string, graded []telegram.GradedOptionV2, err error) {
	switch quiz.Mode(ex.Mode) {
	case quiz.ModeSet:
		var opts []setOptionJSON
		if uerr := json.Unmarshal(ex.Options, &opts); uerr != nil {
			return false, "", nil, uerr
		}
		graded = make([]telegram.GradedOptionV2, len(opts))
		for i, o := range opts {
			graded[i] = telegram.GradedOptionV2{Index: i, Label: o.Label, Mark: markFor(canonicalSetString(o.Keys), ex.CorrectAnswer, i, idx)}
		}
		if idx >= 0 && idx < len(opts) {
			chosen = canonicalSetString(opts[idx].Keys)
		}
		correct = idx >= 0 && idx < len(opts) && canonicalSetString(opts[idx].Keys) == ex.CorrectAnswer
		return correct, chosen, graded, nil
	default: // quiz.ModeSingle (and a defensive fallback for ModeText, never routed here in practice)
		var opts []singleOptionJSON
		if uerr := json.Unmarshal(ex.Options, &opts); uerr != nil {
			return false, "", nil, uerr
		}
		graded = make([]telegram.GradedOptionV2, len(opts))
		for i, o := range opts {
			graded[i] = telegram.GradedOptionV2{Index: i, Label: o.Label, Mark: markFor(o.Key, ex.CorrectAnswer, i, idx)}
		}
		if idx >= 0 && idx < len(opts) {
			chosen = opts[idx].Key
		}
		correct = idx >= 0 && idx < len(opts) && opts[idx].Key == ex.CorrectAnswer
		return correct, chosen, graded, nil
	}
}

// matchTypedText grades a free-typed reply against a persisted ModeText
// exercise's options jsonb (architecture §1.6 step 6: quiz.TextMatcher,
// MaxEdits 2, over the accepted spellings the generator supplied at
// generation time — specialchars.buildText's Accept list). Pure given the
// raw jsonb bytes, so the text-matching path is unit-testable without a
// database.
func matchTypedText(optionsJSON []byte, typed string) (correct bool, err error) {
	var opts textOptionsJSON
	if err := json.Unmarshal(optionsJSON, &opts); err != nil {
		return false, err
	}
	correct, _ = (quiz.TextMatcher{Accept: opts.Accept, MaxEdits: 2}).Match(typed)
	return correct, nil
}

// markFor decorates one option position i: correct if its serialized key
// equals correctAnswer, wrong if it's the tapped index and not correct,
// otherwise unmarked. Mirrors train.markFor's shape for the v2 index-based
// path.
func markFor(optKey, correctAnswer string, i, chosenIdx int) train.Mark {
	switch {
	case optKey == correctAnswer:
		return train.MarkCorrect
	case i == chosenIdx:
		return train.MarkWrong
	default:
		return train.MarkNone
	}
}

// ── AnswerV2 / AnswerText ────────────────────────────────────────────────

// AnswerV2 implements telegram.TrainerV2.
func (s *Service) AnswerV2(ctx context.Context, userID, exerciseID uuid.UUID, optionIndex int) (telegram.AnswerResultV2, error) {
	ex, found, err := s.store.GetExerciseByIDV2(ctx, exerciseID)
	if err != nil {
		return telegram.AnswerResultV2{}, err
	}
	if !found || ex.ItemID == nil {
		return telegram.AnswerResultV2{Stale: true}, nil
	}
	correct, chosen, graded, err := gradeIndexed(ex, optionIndex)
	if err != nil {
		return telegram.AnswerResultV2{}, err
	}
	return s.finishAnswer(ctx, userID, ex, chosen, correct, graded, s.now())
}

// AnswerText implements telegram.TrainerV2: grades a free-typed reply
// against the caller's single open ModeText exercise (architecture §1.6
// step 6). ok=false means there is no such exercise.
func (s *Service) AnswerText(ctx context.Context, userID uuid.UUID, typed string) (telegram.AnswerResultV2, bool, error) {
	ex, found, err := s.store.GetOpenExerciseV2ByMode(ctx, userID, int16(quiz.ModeText))
	if err != nil {
		return telegram.AnswerResultV2{}, false, err
	}
	if !found || ex.ItemID == nil {
		return telegram.AnswerResultV2{}, false, nil
	}

	correct, err := matchTypedText(ex.Options, typed)
	if err != nil {
		return telegram.AnswerResultV2{}, true, err
	}

	res, err := s.finishAnswer(ctx, userID, ex, typed, correct, nil, s.now())
	if err != nil {
		return telegram.AnswerResultV2{}, true, err
	}
	return res, true, nil
}

// finishAnswer is the shared atomic write path for AnswerV2/AnswerText
// (architecture §1.6 steps 5/6, §5.5): single-use MarkExerciseAnswered
// guard, engram Scheduler.Next on the item's current card, PutUserItem
// (with lifecycle promotion via engram.LifecycleFor), InsertReviewV2, and
// the affected tier's progress recompute, all inside one transaction. A
// stale (already-answered) exercise is detected by the guard and returns
// AnswerResultV2{Stale: true} without touching anything else.
func (s *Service) finishAnswer(ctx context.Context, userID uuid.UUID, ex storage.ExerciseV2, chosen string, correct bool, gradedOptions []telegram.GradedOptionV2, now time.Time) (telegram.AnswerResultV2, error) {
	itemID := *ex.ItemID
	rating := engram.RatingForAnswer(correct)

	effectiveTier, err := s.store.GetItemEffectiveTier(ctx, itemID)
	if err != nil {
		return telegram.AnswerResultV2{}, err
	}
	bridgeSkill, _, err := s.bridgeIDs(ctx)
	if err != nil {
		return telegram.AnswerResultV2{}, err
	}

	var stale bool
	err = s.store.WithTxStore(ctx, func(tx *storage.Store) error {
		owned, err := tx.MarkExerciseAnswered(ctx, ex.ID, now)
		if err != nil {
			return err
		}
		if !owned {
			stale = true
			return nil
		}

		userItem, hasUI, err := tx.GetUserItem(ctx, userID, itemID)
		if err != nil {
			return err
		}
		cs := s.sched.NewCardState(now)
		introducedAt, knownAt := now, time.Time{}
		if hasUI {
			cs = engramstore.CardStateFrom(userItem.Card)
			introducedAt, knownAt = userItem.IntroducedAt, userItem.KnownAt
		}
		newCard, rev := s.sched.Next(cs, now, rating)
		lifecycleAfter := engram.LifecycleFor(newCard)
		if err := tx.PutUserItem(ctx, userID, itemID, int16(lifecycleAfter), engramstore.CardFieldsFrom(newCard), introducedAt, knownAt); err != nil {
			return err
		}

		ms := max0(int(now.Sub(ex.CreatedAt).Milliseconds()))
		exID := ex.ID
		if err := tx.InsertReviewV2(ctx, storage.ReviewInsertV2{
			ReviewInsert: storage.ReviewInsert{
				UserID: userID, SkillID: bridgeSkill, ExerciseID: &exID, ContentID: ex.ContentID,
				ChosenKey: chosen, CorrectKey: ex.CorrectAnswer, Correct: correct, Rating: int16(rating), ResponseMS: &ms,
				StabilityBefore: rev.StabilityBefore, DifficultyBefore: rev.DifficultyBefore,
				StabilityAfter: rev.StabilityAfter, DifficultyAfter: rev.DifficultyAfter,
				StateBefore: int16(rev.StateBefore), ScheduledDays: rev.ScheduledDays, ElapsedDays: rev.ElapsedDays,
				ReviewedAt: now, Practice: false,
			},
			ItemID: &itemID, Mode: ex.Mode, Chosen: chosen, CorrectAnswer: ex.CorrectAnswer,
		}); err != nil {
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
		return telegram.AnswerResultV2{}, err
	}
	if stale {
		return telegram.AnswerResultV2{Stale: true}, nil
	}

	text := ex.Prompt
	if tip := s.tipFor(ctx, itemID, ex, chosen, correct); tip != "" {
		text += "\n\n💡 " + tip
	}
	return telegram.AnswerResultV2{
		Correct: correct,
		Text:    text,
		Options: gradedOptions,
	}, nil
}

// tipFor asks the item's topic generator for a post-answer recognition tip
// (architecture §1.6 step 5), if it implements topics.TipProvider. Runs
// AFTER the answer's write path has already committed, and any failure
// degrades to "no tip" (returns "") rather than propagating — tips are
// decoration and must never fail the answer, mirroring
// internal/train.Service.Answer's same tip-is-best-effort contract.
func (s *Service) tipFor(ctx context.Context, itemID uuid.UUID, ex storage.ExerciseV2, chosen string, correct bool) string {
	item, found, err := s.store.GetItemByID(ctx, itemID)
	if err != nil || !found {
		return ""
	}
	topic, found, err := s.store.GetTopicByID(ctx, item.TopicID)
	if err != nil || !found {
		return ""
	}
	gen, ok := s.reg.Get(topic.QuizKind)
	if !ok {
		return ""
	}
	tp, ok := gen.(topics.TipProvider)
	if !ok {
		return ""
	}
	return tp.Tips().Tip(quiz.TipRequest{
		ContentPayload: ex.Prompt,
		CorrectKey:     ex.CorrectAnswer,
		CorrectLabel:   ex.CorrectAnswer,
		ChosenKey:      chosen,
		ChosenLabel:    chosen,
		Correct:        correct,
		Mode:           quiz.Mode(ex.Mode),
	})
}

// ── pure due-queue helpers ───────────────────────────────────────────────

// disabledTopicSet builds the set of topic ids a user has explicitly
// disabled (default is enabled with no user_topics row, so only explicit
// Enabled==false rows are collected).
func disabledTopicSet(uts []storage.UserTopic) map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool)
	for _, ut := range uts {
		if !ut.Enabled {
			out[ut.ID] = true
		}
	}
	return out
}

// filterEnabledDue removes due items whose topic is disabled.
func filterEnabledDue(due []storage.DueUserItem, disabled map[uuid.UUID]bool) []storage.DueUserItem {
	out := make([]storage.DueUserItem, 0, len(due))
	for _, d := range due {
		if disabled[d.TopicID] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// toQueueItemsV2 adapts due items to engram.QueueItem for engram.NextReview.
// Only Skill.ID (carrying the item id) and Card matter to NextReview.
func toQueueItemsV2(due []storage.DueUserItem) []engram.QueueItem {
	out := make([]engram.QueueItem, len(due))
	for i, d := range due {
		out[i] = engram.QueueItem{
			Skill: engram.Skill{ID: engram.SkillID(d.ItemID.String())},
			Card:  engramstore.CardStateFrom(d.Card),
		}
	}
	return out
}

// removeDueItem drops one item from a due-item slice (mirrors
// internal/train's removeSkill, for the "skip and try the next candidate"
// loop).
func removeDueItem(items []storage.DueUserItem, id uuid.UUID) []storage.DueUserItem {
	out := items[:0:0]
	for _, it := range items {
		if it.ItemID != id {
			out = append(out, it)
		}
	}
	return out
}

// removeItemFromSlice drops one item from a plain item slice (siblings
// exclusion — the topics.Generator "Siblings must NOT already include Item"
// contract, mirrors guesslang.Generator's own doc on this point).
func removeItemFromSlice(items []storage.Item, id uuid.UUID) []storage.Item {
	out := items[:0:0]
	for _, it := range items {
		if it.ID != id {
			out = append(out, it)
		}
	}
	return out
}
