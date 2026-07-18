// convert.go holds the pure storage.CardFields <-> engram.CardState
// converters this package needs (formerly a storage-layer engram adapter's
// CardStateFrom/CardFieldsFrom — moved here since this package is their
// only remaining caller now that the legacy trainer is gone).
package study

import (
	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

// cardStateFrom maps storage.CardFields to engram.CardState.
func cardStateFrom(cf storage.CardFields) engram.CardState {
	return engram.CardState{
		Due:        cf.Due,
		Stability:  cf.Stability,
		Difficulty: cf.Difficulty,
		Reps:       cf.Reps,
		Lapses:     cf.Lapses,
		State:      engram.State(cf.State),
		LastReview: cf.LastReview,
	}
}

// cardFieldsFrom maps engram.CardState to storage.CardFields.
func cardFieldsFrom(cs engram.CardState) storage.CardFields {
	return storage.CardFields{
		Due:        cs.Due,
		Stability:  cs.Stability,
		Difficulty: cs.Difficulty,
		Reps:       cs.Reps,
		Lapses:     cs.Lapses,
		State:      int16(cs.State),
		LastReview: cs.LastReview,
	}
}
