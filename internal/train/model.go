package train

import (
	"time"

	"github.com/google/uuid"
)

// optionJSON is how an exercise's shown options are persisted (exercises.options
// jsonb) and re-read at answer time.
type optionJSON struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// NextKind describes the outcome of NextExercise.
type NextKind int

const (
	// KindExercise: a new exercise is ready (Prompt is set).
	KindExercise NextKind = iota
	// KindNothingDue: nothing is due now (DueAt may hold the next due time).
	KindNothingDue
	// KindNoDecks: the user has no enabled decks.
	KindNoDecks
	// KindNoContent: due skills exist but none have any ingested content.
	KindNoContent
)

// Button is one inline answer button.
type Button struct {
	Key          string // answer key (ISO-639-3), for presentation (e.g. flag)
	Label        string
	CallbackData string
}

// Prompt is a ready-to-send exercise: the sentence plus answer buttons.
type Prompt struct {
	ExerciseID uuid.UUID
	Text       string   // the sentence payload
	Source     string   // CC-BY attribution (e.g. "tatoeba#12345")
	Buttons    []Button // one per candidate language, shuffled
	Practice   bool     // true for /practice exercises (adds a Stop control)
}

// NextResult is the outcome of asking for the next thing to study.
type NextResult struct {
	Kind   NextKind
	Prompt *Prompt   // set when Kind == KindExercise
	DueAt  time.Time // set when Kind == KindNothingDue and a future due exists
}

// GradedButton is one answer button after grading, for editing in place.
type GradedButton struct {
	Key   string // answer key (ISO-639-3), for presentation (e.g. flag)
	Name  string // raw, undecorated language name (for re-rendering per label style)
	Label string // already decorated with ✅ / ❌ when relevant (fallback)
	Mark  Mark
}

// AnswerResult is the outcome of grading a tap.
type AnswerResult struct {
	Stale      bool // the exercise was already answered (or unknown) — show a toast
	Correct    bool // whether the tapped key was correct
	ChosenKey  string
	CorrectKey string
	Buttons    []GradedButton // full keyboard, decorated, to replace in place
	MessageID  int64          // the message to edit
	HasMessage bool

	SentenceText string // the sentence shown, re-fetched at grade time; "" if the content row is gone
	Tip          string // recognition tip (no 💡 prefix); "" = no tip available
}

// ConfusionRow is one "you mistake X for Y" line for /stats.
type ConfusionRow struct {
	TargetKey   string
	TargetLabel string
	ChosenKey   string
	ChosenLabel string
	Count       int
	Share       float64
}

// DeckAccuracy is per-deck accuracy for /stats.
type DeckAccuracy struct {
	Slug     string
	Name     string
	Total    int
	Correct  int
	Accuracy float64 // 0..1; 0 when Total == 0
}

// Stats is the /stats view model.
type Stats struct {
	ReviewsToday int
	ReviewsWeek  int
	Streak       int
	Accuracy     float64 // overall, 0..1
	ByDeck       []DeckAccuracy
	DueForecast  []int          // due counts for the next N days (index 0 = today)
	Confusion    []ConfusionRow // top pairs, most-confused first
}
