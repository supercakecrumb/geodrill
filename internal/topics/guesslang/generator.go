// Package guesslang implements the topics.Generator for quiz_kind
// "language_id" (architecture §3.4/§6, topic path
// "languages/guess-the-language/<group>"): the original sentence -> guess
// the language multiple-choice quiz, carried onto the topic/item framework.
// Behavior is preserved (architecture §3.4): the same content sampling
// (SampleContent, falling back to SampleContentAny — internal/train's
// pre-v2 buildExercise did exactly this two-step sample), the same
// distractor-from-group-siblings generation via engram/quiz.Generate, and
// the same recognition tips (internal/tips).
package guesslang

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"
	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/tips"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// Kind is the topics.quiz_kind this package's Generator registers under.
const Kind = "language_id"

// ContentSampler samples quiz content (a sentence) for a language item key
// (an ISO-639-3 code). Its two methods mirror storage.Store's own
// SampleContent/SampleContentAny signatures exactly — the narrow interface
// this package needs (per the internal/topics package doc's "content access
// stays out of this package" contract) — so *storage.Store already
// satisfies ContentSampler with zero adapter code; cmd/bot wires the
// Generator via guesslang.New(store) directly (see New's doc).
type ContentSampler interface {
	// SampleContent samples a sentence for key, excluding the user's
	// recently-seen content (storage.Store.SampleContent semantics).
	// found=false means the excluded pool is empty.
	SampleContent(ctx context.Context, userID uuid.UUID, key string) (storage.Content, bool, error)
	// SampleContentAny samples a sentence for key with no exclusion.
	// found=false means the key has no content at all.
	SampleContentAny(ctx context.Context, key string) (storage.Content, bool, error)
}

// Generator implements topics.Generator for Kind and topics.TipProvider (the
// Romance-group recognition tips, architecture §3.4). It holds only the
// narrow ContentSampler dependency — everything else BuildExercise/BuildIntro
// need comes from their arguments (the item and its siblings).
type Generator struct {
	sampler ContentSampler
}

// New builds a language_id Generator over sampler. Because *storage.Store
// already implements ContentSampler directly (its SampleContent/
// SampleContentAny methods match the interface exactly), cmd/bot wires this
// with guesslang.New(store) — no adapter type is needed. New does NOT
// self-register via init(): wiring a Generator into the topics registry is
// deferred to cmd/bot, same as every other topic package.
func New(sampler ContentSampler) *Generator {
	return &Generator{sampler: sampler}
}

// Kind implements topics.Generator.
func (g *Generator) Kind() string { return Kind }

// Tips implements topics.TipProvider: the existing internal/tips language-tell
// provider, unchanged, so the curated recognition tips (Romance et al.) keep
// working under the topic/item framework (architecture §3.4).
func (g *Generator) Tips() quiz.TipProvider { return tips.Provider() }

var _ topics.TipProvider = (*Generator)(nil)

// BuildExercise implements topics.Generator (ModeSingle only — the
// guess-the-language mechanic has never had a set/text mode). Behavior-
// preserving (architecture §3.4): samples a sentence for the item's language
// exactly like the pre-v2 train.Service.buildExercise (SampleContent first,
// SampleContentAny fallback), then generates the MCQ via engram/quiz.Generate
// over the item plus its siblings adapted to engram.Skill — the same
// generation call the pre-v2 code made, now driven by topic items instead of
// deck skills. req.Siblings is the distractor pool and is expected to NOT
// already include req.Item (mirrors every other topic's Siblings contract);
// quiz.Generate appends the target itself if it's missing from the deck
// slice, so this holds even if a caller passes the target in Siblings too.
func (g *Generator) BuildExercise(ctx context.Context, rng *rand.Rand, req topics.ExerciseRequest) (topics.Exercise, error) {
	content, found, err := g.sampler.SampleContent(ctx, req.User.ID, req.Item.Key)
	if err != nil {
		return topics.Exercise{}, err
	}
	if !found {
		content, found, err = g.sampler.SampleContentAny(ctx, req.Item.Key)
		if err != nil {
			return topics.Exercise{}, err
		}
	}
	if !found {
		return topics.Exercise{}, fmt.Errorf("guesslang: item %s: %w", req.Item.Key, topics.ErrNoContent)
	}

	siblingSkills := make([]engram.Skill, len(req.Siblings))
	for i, sib := range req.Siblings {
		siblingSkills[i] = itemToSkill(sib)
	}
	qc := quiz.Content{ID: engram.ContentID(content.ID.String()), Payload: content.Payload}

	ex := quiz.Generate(rng, itemToSkill(req.Item), siblingSkills, qc)

	options := make([]topics.Option, len(ex.Options))
	for i, o := range ex.Options {
		options[i] = topics.Option{Key: o.Key, Label: o.Label}
	}

	contentID := content.ID
	return topics.Exercise{
		Mode:          quiz.ModeSingle,
		Prompt:        content.Payload,
		Options:       options,
		CorrectAnswer: req.Item.Key,
		ContentID:     &contentID,
		Source:        content.Source,
	}, nil
}

// itemToSkill adapts a topic item to an engram.Skill, the shape
// engram/quiz.Generate needs (mirrors storage/engramstore.SkillTo, adapted
// from storage.Skill to storage.Item): the item's uuid becomes the SkillID,
// its topic id becomes the DeckID (Generate never reads DeckID, but Skill
// requires one), and Key/Label carry through unchanged.
func itemToSkill(it storage.Item) engram.Skill {
	return engram.Skill{
		ID:     engram.SkillID(it.ID.String()),
		DeckID: engram.DeckID(it.TopicID.String()),
		Key:    it.Key,
		Label:  it.Label,
	}
}
