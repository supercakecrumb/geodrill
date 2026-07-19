// trainer.go implements telegram.Trainer: the mode-aware exercise path
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
	"github.com/supercakecrumb/geodrill/internal/telegram"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

var _ telegram.Trainer = (*Service)(nil)

// ── NextExercise ───────────────────────────────────────────────────────

// NextExercise implements telegram.Trainer. Due candidates are
// Introduced/Reviewing items whose topic is enabled for the user
// (architecture §1.6 step 4) — no tier filter is needed here: an item only
// ever reaches lifecycle Introduced/Reviewing by passing the tier gate at
// introduction time (architecture §4.2), and tiers only ever unlock, never
// re-lock, so an already-introduced item stays valid regardless of the
// user's CURRENT unlocked-tier set.
func (s *Service) NextExercise(ctx context.Context, userID uuid.UUID) (telegram.Prompt, error) {
	now := s.now()
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return telegram.Prompt{}, err
	}
	due, err := s.store.ListDueUserItems(ctx, userID, now)
	if err != nil {
		return telegram.Prompt{}, err
	}
	userTopics, err := s.store.ListUserTopics(ctx, userID)
	if err != nil {
		return telegram.Prompt{}, err
	}
	disabled := disabledTopicSet(userTopics)
	work := filterEnabledDue(due, disabled)

	for len(work) > 0 {
		picked, ok := engram.NextReview(toQueueItems(work), now)
		if !ok {
			break
		}
		itemID, err := uuid.Parse(string(picked.Skill.ID))
		if err != nil {
			return telegram.Prompt{}, err
		}
		prompt, built, err := s.buildExercise(ctx, user, itemID, now)
		if err != nil {
			return telegram.Prompt{}, err
		}
		if built {
			return prompt, nil
		}
		// This item's generator couldn't build ANY of its topic's configured
		// modes (topics.ErrNoContent or every mode mismatching its shape) —
		// skip it and try the next due candidate.
		work = removeDueItem(work, itemID)
	}

	dueAt, err := s.earliestFutureDue(ctx, userID, now)
	if err != nil {
		return telegram.Prompt{}, err
	}
	if len(due) == 0 {
		return telegram.Prompt{Kind: telegram.PromptKindNothingDue, DueAt: dueAt}, nil
	}
	// due was non-empty but every candidate was filtered out (disabled
	// topics) or failed to build an exercise for every configured mode.
	if dueAt.IsZero() {
		return telegram.Prompt{Kind: telegram.PromptKindNoContent}, nil
	}
	return telegram.Prompt{Kind: telegram.PromptKindNothingDue, DueAt: dueAt}, nil
}

// earliestFutureDue scans every Introduced/Reviewing item for userID (not
// just currently-due ones — ListDueUserItems only returns due <= now rows)
// for the earliest Due strictly after now, for Prompt's "come back at ..."
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

// buildExercise loads itemID's item and delegates to buildExerciseForItem
// for the /train path.
func (s *Service) buildExercise(ctx context.Context, user storage.User, itemID uuid.UUID, now time.Time) (telegram.Prompt, bool, error) {
	item, found, err := s.store.GetItemByID(ctx, itemID)
	if err != nil {
		return telegram.Prompt{}, false, err
	}
	if !found {
		return telegram.Prompt{}, false, nil
	}
	return s.buildExerciseForItem(ctx, user, item, now)
}

// buildExerciseForItem loads item's topic/generator/siblings and tries the
// topic's configured exercise modes in rotation order (modeRotationOrder)
// until one builds successfully, persisting the result. built=false means
// every configured mode failed (the item currently can't be quizzed) — the
// caller skips it.
func (s *Service) buildExerciseForItem(ctx context.Context, user storage.User, item storage.Item, now time.Time) (telegram.Prompt, bool, error) {
	topic, found, err := s.store.GetTopicByID(ctx, item.TopicID)
	if err != nil {
		return telegram.Prompt{}, false, err
	}
	if !found {
		return telegram.Prompt{}, false, nil
	}
	gen, ok := s.reg.Get(topic.QuizKind)
	if !ok {
		return telegram.Prompt{}, false, fmt.Errorf("study: no generator registered for quiz_kind %q (topic %s)", topic.QuizKind, topic.Slug)
	}

	siblings, err := s.store.ListActiveItemsByTopic(ctx, item.TopicID)
	if err != nil {
		return telegram.Prompt{}, false, err
	}
	siblings = removeItemFromSlice(siblings, item.ID)

	userItem, _, err := s.store.GetUserItem(ctx, user.ID, item.ID)
	if err != nil {
		return telegram.Prompt{}, false, err
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
		// Either signal is sufficient to render the "⌨️ Type your answer"
		// inline-query prefill button (topics.Exercise.Autocomplete's doc):
		// this turn's configured mode string was literally "autocomplete",
		// or the generator itself flagged the built Exercise. Meaningless
		// for anything but ModeText — sendExercise (internal/telegram)
		// gates on Mode == quiz.ModeText before ever looking at it.
		autocomplete := modeStr == "autocomplete" || ex.Autocomplete
		prompt, err := s.persistExercise(ctx, user.ID, item, ex, autocomplete, now)
		if err != nil {
			return telegram.Prompt{}, false, err
		}
		return prompt, true, nil
	}
	return telegram.Prompt{}, false, nil
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
// unrecognised values to ModeSingle. "autocomplete" maps to ModeText: it is
// presentation-only sugar over the exact same free-text grading path
// (vibe/spike-autocomplete-inline.md's verdict is that grading never
// special-cases how the text arrived), requesting the "⌨️ Type your answer"
// inline-query prefill button on top of it — buildExerciseForItem is what
// turns "was this turn's mode string literally 'autocomplete'" into
// Prompt.Autocomplete for internal/telegram to act on; this function only
// ever decides the underlying quiz.Mode.
func modeFromString(m string) quiz.Mode {
	switch m {
	case "set":
		return quiz.ModeSet
	case "text", "autocomplete":
		return quiz.ModeText
	default:
		return quiz.ModeSingle
	}
}

// persistExercise serializes ex per its mode (model.go) and inserts the
// exercises row, returning the ready-to-send Prompt. autocomplete is NOT
// persisted (there is no exercises column for it — it's a presentation-only
// hint, recomputed fresh every time an exercise is generated): it only sets
// the returned Prompt.Autocomplete, so internal/telegram can decide whether
// to attach the inline-query prefill button (see buildExerciseForItem's doc
// on how autocomplete is decided).
func (s *Service) persistExercise(ctx context.Context, userID uuid.UUID, item storage.Item, ex topics.Exercise, autocomplete bool, now time.Time) (telegram.Prompt, error) {
	optionsJSON, correctAnswer, err := serializeExercise(ex)
	if err != nil {
		return telegram.Prompt{}, err
	}

	exID, _, err := s.store.InsertExercise(ctx, storage.InsertExerciseParams{
		UserID:        userID,
		ContentID:     ex.ContentID,
		ItemID:        item.ID,
		Mode:          int16(ex.Mode),
		Prompt:        ex.Prompt,
		Options:       optionsJSON,
		CorrectAnswer: correctAnswer,
		IsMedia:       ex.MediaPath != "",
	})
	if err != nil {
		return telegram.Prompt{}, err
	}

	return telegram.Prompt{
		Kind:         telegram.PromptKindExercise,
		ExerciseID:   exID,
		Text:         ex.Prompt,
		MediaPath:    ex.MediaPath,
		Mode:         ex.Mode,
		Options:      optionsFor(ex),
		Autocomplete: autocomplete,
	}, nil
}

// canonicalSetString renders a set-choice option's key list into the same
// canonical, order-insensitive string both generation (see
// specialchars.buildSetMCQ) and grading use for set-equality comparison.
func canonicalSetString(keys []string) string {
	return strings.Join(quiz.CanonSet(keys...), ",")
}

// serializeExercise renders a topics.Exercise into its persisted
// exercises.options jsonb + correct_answer pair (architecture §2.5, model.go).
func serializeExercise(ex topics.Exercise) ([]byte, string, error) {
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

// optionsFor renders a topics.Exercise's options into Prompt's index-
// ordered Option list (empty for ModeText, which has no buttons).
func optionsFor(ex topics.Exercise) []telegram.Option {
	switch ex.Mode {
	case quiz.ModeSet:
		out := make([]telegram.Option, len(ex.OptionSets))
		for i, o := range ex.OptionSets {
			out[i] = telegram.Option{Index: i, Label: o.Label}
		}
		return out
	case quiz.ModeText:
		return nil
	default:
		out := make([]telegram.Option, len(ex.Options))
		for i, o := range ex.Options {
			out[i] = telegram.Option{Index: i, Label: o.Label}
		}
		return out
	}
}

// ── grading ──────────────────────────────────────────────────────────────

// gradeIndexed grades a tapped option position against a persisted
// exercise (architecture §1.6 step 5): ModeSingle compares the option's key
// to correct_answer; ModeSet compares the option's canonicalized key-set
// string to correct_answer (both sides canonicalized the same way,
// canonicalSetString). Pure given ex — no I/O — so index/set-canonicalization
// grading is unit-testable without a database. Out-of-range idx grades wrong
// (chosen == "") rather than erroring.
func gradeIndexed(ex storage.Exercise, idx int) (correct bool, chosen string, graded []telegram.GradedOption, err error) {
	switch quiz.Mode(ex.Mode) {
	case quiz.ModeSet:
		var opts []setOptionJSON
		if uerr := json.Unmarshal(ex.Options, &opts); uerr != nil {
			return false, "", nil, uerr
		}
		graded = make([]telegram.GradedOption, len(opts))
		for i, o := range opts {
			graded[i] = telegram.GradedOption{Index: i, Label: o.Label, Mark: markFor(canonicalSetString(o.Keys), ex.CorrectAnswer, i, idx)}
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
		graded = make([]telegram.GradedOption, len(opts))
		for i, o := range opts {
			graded[i] = telegram.GradedOption{Index: i, Label: o.Label, Mark: markFor(o.Key, ex.CorrectAnswer, i, idx)}
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
// otherwise unmarked.
func markFor(optKey, correctAnswer string, i, chosenIdx int) telegram.Mark {
	switch {
	case optKey == correctAnswer:
		return telegram.MarkCorrect
	case i == chosenIdx:
		return telegram.MarkWrong
	default:
		return telegram.MarkNone
	}
}

// ── Answer / AnswerText ────────────────────────────────────────────────

// Answer implements telegram.Trainer.
func (s *Service) Answer(ctx context.Context, userID, exerciseID uuid.UUID, optionIndex int) (telegram.AnswerResult, error) {
	ex, found, err := s.store.GetExerciseByID(ctx, exerciseID)
	if err != nil {
		return telegram.AnswerResult{}, err
	}
	if !found {
		return telegram.AnswerResult{Stale: true}, nil
	}
	correct, chosen, graded, err := gradeIndexed(ex, optionIndex)
	if err != nil {
		return telegram.AnswerResult{}, err
	}
	return s.finishAnswer(ctx, userID, ex, chosen, correct, graded, s.now())
}

// AnswerText implements telegram.Trainer: grades a free-typed reply
// against the caller's single open ModeText exercise (architecture §1.6
// step 6). ok=false means there is no such exercise.
func (s *Service) AnswerText(ctx context.Context, userID uuid.UUID, typed string) (telegram.AnswerResult, bool, error) {
	ex, found, err := s.store.GetOpenExerciseByMode(ctx, userID, int16(quiz.ModeText))
	if err != nil {
		return telegram.AnswerResult{}, false, err
	}
	if !found {
		return telegram.AnswerResult{}, false, nil
	}

	correct, err := matchTypedText(ex.Options, typed)
	if err != nil {
		return telegram.AnswerResult{}, true, err
	}

	res, err := s.finishAnswer(ctx, userID, ex, typed, correct, nil, s.now())
	if err != nil {
		return telegram.AnswerResult{}, true, err
	}
	return res, true, nil
}

// finishAnswer is the shared atomic write path for Answer/AnswerText
// (architecture §1.6 steps 5/6, §5.5). Both callers already guard
// found before calling in, so ex.ItemID is always valid here.
//
// Single-use MarkExerciseAnswered guard, engram Scheduler.Next on the
// item's current card, PutUserItem (with lifecycle promotion via
// engram.LifecycleFor), InsertReview, and the affected tier's progress
// recompute, all inside one transaction.
//
// A stale (already-answered) exercise is detected by the guard and returns
// AnswerResult{Stale: true} without touching anything else.
func (s *Service) finishAnswer(ctx context.Context, userID uuid.UUID, ex storage.Exercise, chosen string, correct bool, gradedOptions []telegram.GradedOption, now time.Time) (telegram.AnswerResult, error) {
	itemID := ex.ItemID
	rating := engram.RatingForAnswer(correct)

	effectiveTier, err := s.store.GetItemEffectiveTier(ctx, itemID)
	if err != nil {
		return telegram.AnswerResult{}, err
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
			cs = cardStateFrom(userItem.Card)
			introducedAt, knownAt = userItem.IntroducedAt, userItem.KnownAt
		}
		newCard, rev := s.sched.Next(cs, now, rating)
		lifecycleAfter := engram.LifecycleFor(newCard)
		if err := tx.PutUserItem(ctx, userID, itemID, int16(lifecycleAfter), cardFieldsFrom(newCard), introducedAt, knownAt); err != nil {
			return err
		}

		ms := max0(int(now.Sub(ex.CreatedAt).Milliseconds()))
		exID := ex.ID
		if err := tx.InsertReview(ctx, storage.ReviewInsert{
			UserID: userID, ItemID: itemID, ExerciseID: &exID, ContentID: ex.ContentID,
			Mode: ex.Mode, Chosen: chosen, CorrectAnswer: ex.CorrectAnswer,
			Correct: correct, Rating: int16(rating), ResponseMS: &ms,
			StabilityBefore: rev.StabilityBefore, DifficultyBefore: rev.DifficultyBefore,
			StabilityAfter: rev.StabilityAfter, DifficultyAfter: rev.DifficultyAfter,
			StateBefore: int16(rev.StateBefore), ScheduledDays: rev.ScheduledDays, ElapsedDays: rev.ElapsedDays,
			ReviewedAt: now, Practice: false,
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
		return telegram.AnswerResult{}, err
	}
	if stale {
		return telegram.AnswerResult{Stale: true}, nil
	}

	text := ex.Prompt
	if tip := s.tipFor(ctx, itemID, ex, chosen, correct); tip != "" {
		text += "\n\n💡 " + tip
	}
	return telegram.AnswerResult{
		Correct: correct,
		Text:    text,
		Options: gradedOptions,
	}, nil
}

// tipFor asks the item's topic generator for a post-answer recognition tip
// (architecture §1.6 step 5), if it implements topics.TipProvider. Runs
// AFTER the answer's write path has already committed, and any failure
// degrades to "no tip" (returns "") rather than propagating — tips are
// decoration and must never fail the answer.
func (s *Service) tipFor(ctx context.Context, itemID uuid.UUID, ex storage.Exercise, chosen string, correct bool) string {
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

// toQueueItems adapts due items to engram.QueueItem for engram.NextReview.
// Only Skill.ID (carrying the item id) and Card matter to NextReview.
func toQueueItems(due []storage.DueUserItem) []engram.QueueItem {
	out := make([]engram.QueueItem, len(due))
	for i, d := range due {
		out[i] = engram.QueueItem{
			Skill: engram.Skill{ID: engram.SkillID(d.ItemID.String())},
			Card:  cardStateFrom(d.Card),
		}
	}
	return out
}

// removeDueItem drops one item from a due-item slice, for the "skip and
// try the next candidate" loop.
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
