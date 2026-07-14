package train

import (
	"strings"

	"github.com/google/uuid"
)

// Callback data prefixes. Telegram callback data is capped at 64 bytes; the
// widest case is "ans:" (4) + uuid (36) + ":" (1) + ISO-639-3 key (3) = 44.
const (
	// PrefixAnswer marks a scheduled /train answer: ans:<exercise-uuid>:<key>.
	PrefixAnswer = "ans"
	// PrefixPractice marks an unscheduled /practice answer: prac:<uuid>:<key>.
	PrefixPractice = "prac"
	// DataNoop is the inert callback used on already-graded buttons.
	DataNoop = "noop"
)

// Callback is a parsed answer tap.
type Callback struct {
	Kind       string // PrefixAnswer | PrefixPractice
	ExerciseID uuid.UUID
	Key        string
	Practice   bool
}

// AnswerData builds the callback payload for a scheduled answer button.
func AnswerData(exerciseID uuid.UUID, key string) string {
	return PrefixAnswer + ":" + exerciseID.String() + ":" + key
}

// PracticeData builds the callback payload for a practice answer button.
func PracticeData(exerciseID uuid.UUID, key string) string {
	return PrefixPractice + ":" + exerciseID.String() + ":" + key
}

// ParseCallback parses an "ans:<uuid>:<key>" or "prac:<uuid>:<key>" payload.
// ok is false for anything else (including the inert "noop").
func ParseCallback(data string) (Callback, bool) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 {
		return Callback{}, false
	}
	kind := parts[0]
	if kind != PrefixAnswer && kind != PrefixPractice {
		return Callback{}, false
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return Callback{}, false
	}
	key := parts[2]
	if key == "" {
		return Callback{}, false
	}
	return Callback{
		Kind:       kind,
		ExerciseID: id,
		Key:        key,
		Practice:   kind == PrefixPractice,
	}, true
}

// Mark is the visual state of a graded answer button.
type Mark int

const (
	// MarkNone: this option was neither the answer nor the (wrong) tap.
	MarkNone Mark = iota
	// MarkCorrect: this option is the correct answer (✅).
	MarkCorrect
	// MarkWrong: this option is the wrong one the user tapped (❌).
	MarkWrong
)

// markFor returns how option optKey should be decorated given the tapped and
// correct keys.
func markFor(optKey, chosenKey, correctKey string) Mark {
	switch {
	case optKey == correctKey:
		return MarkCorrect
	case optKey == chosenKey:
		return MarkWrong
	default:
		return MarkNone
	}
}

// DecorateLabel prefixes a button label with its grade mark.
func DecorateLabel(label string, m Mark) string {
	switch m {
	case MarkCorrect:
		return "✅ " + label
	case MarkWrong:
		return "❌ " + label
	default:
		return label
	}
}
